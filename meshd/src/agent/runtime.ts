// runtime.ts：单个 agent 的 worker 循环。
//
// 负责：
//   1. 启动时拉自己的 agent 配置（含 system_prompt）
//   2. 用 Claude Agent SDK 创建 query session（注入 system_prompt + mesh tools）
//   3. inbox 长轮询：拿到事件就格式化成自然语言喂给 SDK
//   4. SDK 自主推理 + 调用 mesh tools 与队友协作
//
// 设计要点：
//   - 一个 AgentRuntime = 一个 agent worker；meshd 进程内可同时跑多个
//   - inbox 事件按到达顺序串行处理：避免并发 query 抢同一个 SDK session 状态
//   - SDK query 是流式的，但我们只关心终止；中间的 tool 调用 SDK 自己处理
//   - 启动后 agent 就"活着"，等 inbox 事件触发推理；没事件就空跑

import { query } from '@anthropic-ai/claude-agent-sdk'
import type { GatewayClient, InboxEvent } from '../gateway/client.ts'
import type { Logger } from '../log.ts'
import { buildMeshToolsServer, type TurnContext } from '../tools/mesh-tools.ts'
import { CursorStore } from './cursor.ts'
import { computeChatScore, isChatterStreak, newChatScoreContext, type ChatScoreContext } from './chat-score.ts'
import { FanOutCollector, type FanOutGroup } from './fan-out.ts'
import { InboxKafkaConsumer, type InboxKafkaEvent } from '../kafka/consumer.ts'
import { DedupStore } from './dedup.ts'
import { join, dirname } from 'node:path'

interface AgentRuntimeOpts {
  client: GatewayClient
  agentID: string
  model: string
  pollWaitSec: number
  cursorPath: string // ${stateDir}/${agentID}/cursor
  kafkaBrokers?: string // 非空时启用 Kafka consumer
  log: Logger
}

export class AgentRuntime {
  private cursor = 0
  private stopped = false
  private systemPrompt = ''
  private agentName = ''
  private workspacePath = '' // 用户配置的工作目录（为空时用默认 ~/.agent-mesh/cursor/workspace/{agentID}）
  private teammatesSummary = '' // 队友摘要（启动时生成，注入 system prompt）
  private notesSummary = '' // 个人笔记摘要（启动时从 notes/ 读取）
  private pendingPlans: string[] = [] // 未完成的计划文件名（启动时扫描）
  private pendingPlansInjected = false // 是否已注入过恢复提示
  private cursorStore: CursorStore
  /** 每个 task_id 独立的 chat_score 上下文 */
  private chatScoreCtxs = new Map<string, ChatScoreContext>()
  /** Fan-out 回复收集器 */
  private fanOut!: FanOutCollector
  /** 已 resolve 的 fan-out 组，等待下次 pollLoop 迭代时喂 SDK */
  private pendingAggregations: FanOutGroup[] = []
  /** 是否已有 session（true = 用 continue 继续对话） */
  private sessionStarted = false
  /** Kafka consumer（Phase 2，替代长轮询） */
  private kafkaConsumer: InboxKafkaConsumer | null = null
  /** 消费者侧去重（防止 Kafka 重复投递触发重复推理） */
  private dedup!: DedupStore

  constructor(private opts: AgentRuntimeOpts) {
    this.cursorStore = new CursorStore(opts.cursorPath, opts.log)
  }

  async start(): Promise<void> {
    // 1) 加载 cursor（首次启动是 0）
    this.cursor = await this.cursorStore.load()

    // 1.2) 检查 agent workspace 是否已有 session（SDK 的 continue 模式用）
    // 每个 agent 有独立的 cwd（~/.agent-mesh/cursor/workspace/{agentID}），
    // SDK 在该目录下自动管理 session 文件。如果目录下已有 .claude/ 项目数据，
    // 说明之前跑过，用 continue:true 继续；否则 continue:false 开新对话。
    const agentWorkspace = join(dirname(this.opts.cursorPath), 'workspace', this.opts.agentID)
    const { mkdir: mkdirWs, readdir } = await import('node:fs/promises')
    await mkdirWs(agentWorkspace, { recursive: true })
    try {
      const files = await readdir(join(agentWorkspace, '.claude'))
      this.sessionStarted = files.length > 0
      if (this.sessionStarted) {
        this.opts.log.info('session: will continue existing conversation', { workspace: agentWorkspace })
      }
    } catch {
      this.sessionStarted = false
      this.opts.log.info('session: fresh start (no prior conversation)', { workspace: agentWorkspace })
    }

    // 1.5) 初始化 fan-out collector
    const fanOutPath = join(dirname(this.opts.cursorPath), 'fanout', `${this.opts.agentID}.json`)
    this.fanOut = new FanOutCollector(fanOutPath, (group) => {
      this.pendingAggregations.push(group)
    }, this.opts.log)
    await this.fanOut.restore()

    // 1.6) 初始化消费者去重
    const dedupPath = join(dirname(this.opts.cursorPath), 'dedup', this.opts.agentID)
    this.dedup = new DedupStore(dedupPath, this.opts.log)
    await this.dedup.load()

    // 2) 拉自己配置
    const me = await this.opts.client.getMe()
    this.systemPrompt = me.system_prompt ?? ''
    this.agentName = me.name || me.agent_id
    this.workspacePath = me.workspace_path || '' // 用户配置的工作目录
    this.opts.log.info('agent runtime started', {
      agent_id: me.agent_id,
      name: this.agentName,
      has_prompt: this.systemPrompt.length > 0,
      cursor: this.cursor,
    })

    // 2.5) 拉取队友信息（好友 + 同群成员的 headline），用于注入 system prompt
    this.teammatesSummary = await this.buildTeammatesSummary()
    if (this.teammatesSummary) {
      this.opts.log.info('teammates loaded', { lines: this.teammatesSummary.split('\n').length })
    }

    // 2.6) 初始化 workspace：确保 notes/ 目录和 CLAUDE.md 存在 + 读取已有笔记
    await this.initWorkspace()

    // 3) 进入消息消费循环
    if (this.opts.kafkaBrokers) {
      // Kafka 模式：实时消费，延迟 ~10ms
      this.opts.log.info('using Kafka consumer for inbox', { brokers: this.opts.kafkaBrokers })
      this.kafkaConsumer = new InboxKafkaConsumer({
        brokers: this.opts.kafkaBrokers,
        agentID: this.opts.agentID,
        topic: 'inbox.events',
        log: this.opts.log,
      })
      await this.kafkaConsumer.start(async (ev) => {
        await this.handleKafkaEvent(ev)
      })
    } else {
      // Fallback：HTTP 长轮询（向后兼容，Kafka 不可用时降级）
      await this.pollLoop()
    }
  }

  stop(): void {
    this.stopped = true
    if (this.kafkaConsumer) {
      void this.kafkaConsumer.stop()
    }
  }

  /**
   * 处理从 Kafka 收到的事件。转换成 InboxEvent 格式后复用 handleEvent。
   * Kafka 消息的 value 是 Gateway 双写的 inbox event payload。
   */
  private async handleKafkaEvent(ev: InboxKafkaEvent): Promise<void> {
    // ── 幂等去重：同一条消息只处理一次 ──
    const msgId = ev.payload?.message_id || ev.payload?.ref_id || ''
    if (msgId && this.dedup.has(msgId)) {
      this.opts.log.debug('dedup: skip duplicate', { message_id: msgId })
      return
    }

    // 构造兼容 InboxEvent 的结构
    const inboxEvent: InboxEvent = {
      id: 0, // Kafka 模式下不用 cursor ID
      agent_id: this.opts.agentID,
      kind: ev.kind as any,
      task_id: ev.task_id || ev.payload?.task_id || '',
      ref_id: ev.payload?.message_id || ev.payload?.ref_id || '',
      payload: ev.payload,
      enqueued_at: ev.payload?.created_at || new Date().toISOString(),
    }

    // 跳过无 task_id 的事件（如 timeline_update 的 fan-out 元数据）
    if (!inboxEvent.task_id) return

    await this.handleEvent(inboxEvent)

    // Kafka 模式下也要处理 fan-out sweep + pending aggregations
    this.fanOut.sweepExpired()
    while (this.pendingAggregations.length > 0) {
      const group = this.pendingAggregations.shift()!
      const prompt = this.fanOut.formatAggregatedPrompt(group)
      this.opts.log.info('fan-out: feeding aggregated prompt to SDK', { fan_out_id: group.fanOutID })
      await this.feedPromptToSDK(prompt)
    }

    // ── 处理完成，标记已处理（持久化到磁盘）──
    if (msgId) {
      await this.dedup.mark(msgId)
    }
  }

  private async pollLoop(): Promise<void> {
    let backoff = 1000
    while (!this.stopped) {
      try {
        const res = await this.opts.client.pollInbox(this.cursor, this.opts.pollWaitSec)
        backoff = 1000
        for (const ev of res.events) {
          // 幂等去重
          const refId = ev.ref_id || ev.payload?.message_id || ''
          if (refId && this.dedup.has(refId)) {
            this.opts.log.debug('dedup: skip duplicate (poll)', { ref_id: refId })
          } else {
            await this.handleEvent(ev)
            if (refId) await this.dedup.mark(refId)
          }
          // 每条事件处理完立即 advance + 持久化 cursor。
          // 这样即使 daemon 中途 crash，下次启动从下一个事件开始。
          // 故意不用 max_id 一次性 advance —— 防止"批量事件中间崩"导致重复处理。
          if (ev.id > this.cursor) {
            this.cursor = ev.id
            await this.cursorStore.save(this.cursor)
          }
        }
        // 如果没事件但服务端返了 max_id（比如 timeline_update 都过滤掉了），
        // 也要把 cursor 推进，否则下次还会拉到。
        if (res.max_id > this.cursor) {
          this.cursor = res.max_id
          await this.cursorStore.save(this.cursor)
        }

        // Fan-out：检查超时 + 投递已汇总的 prompt
        this.fanOut.sweepExpired()
        while (this.pendingAggregations.length > 0) {
          const group = this.pendingAggregations.shift()!
          const prompt = this.fanOut.formatAggregatedPrompt(group)
          this.opts.log.info('fan-out: feeding aggregated prompt to SDK', { fan_out_id: group.fanOutID })
          await this.feedPromptToSDK(prompt)
        }
      } catch (e) {
        this.opts.log.warn('inbox poll failed', { err: String(e), retry_in_ms: backoff })
        await sleep(backoff)
        backoff = Math.min(backoff * 2, 30 * 1000)
      }
    }
  }

  /**
   * 处理一个 inbox 事件：把它格式化成 prompt 给 SDK，让 LLM 决策响应。
   *
   * timeline_update 事件不直接喂 LLM（信息量低、噪音高），只在 LLM 需要时
   * 通过 mesh_get_timeline 主动拉取。
   */
  private async handleEvent(ev: InboxEvent): Promise<void> {
    if (ev.kind === 'timeline_update') {
      this.opts.log.debug('skip timeline_update', { id: ev.id })
      return
    }

    // ── Fan-out 拦截：如果这条消息属于某个 broadcast 的 child task，缓冲不喂 SDK ──
    if (ev.kind === 'message' && ev.task_id) {
      const text = extractText(ev.payload?.parts) || ''
      const sender = ev.payload?.from_agent_id || ev.payload?.role || ''
      const result = this.fanOut.tryAbsorb(ev.task_id, text, sender)
      if (result === 'absorbed') {
        this.opts.log.info('fan-out: reply absorbed, not feeding SDK', { task_id: ev.task_id, sender })
        return
      }
      if (result === 'resolved') {
        this.opts.log.info('fan-out: group resolved by this reply', { task_id: ev.task_id })
        return // onResolved callback already queued the aggregated prompt
      }
      // 'not_mine' → fall through to normal handling
    }

    let userMsg = formatEventAsPrompt(ev, this.agentName)
    if (!userMsg) {
      return
    }

    // ── 未完成计划恢复提示（只在第一次事件时注入）──
    if (!this.pendingPlansInjected && this.pendingPlans.length > 0) {
      this.pendingPlansInjected = true
      const planList = this.pendingPlans.map(p => `  - ${p}`).join('\n')
      userMsg += `\n\n📋 你有未完成的计划文件：\n${planList}\n请先 Read 计划文件了解进度，然后继续执行未完成的步骤。`
    }

    // ── chat_score 检测：闲聊连击时附加 hint 引导 LLM close task ──
    if (ev.kind === 'message' && ev.task_id) {
      const taskID = ev.task_id
      if (!this.chatScoreCtxs.has(taskID)) {
        this.chatScoreCtxs.set(taskID, newChatScoreContext())
      }
      const ctx = this.chatScoreCtxs.get(taskID)!
      const text = extractText(ev.payload?.parts) || ''
      const sender = ev.payload?.from_agent_id || ev.payload?.role || 'unknown'
      const score = computeChatScore(text, sender, Date.now(), ctx)

      this.opts.log.debug('chat_score', { task_id: taskID, score: score.toFixed(2), streak: ctx.recentScores.length })

      if (isChatterStreak(ctx)) {
        // 连续 3+ 条高分 → 附加 hint 让 LLM 主动 close
        userMsg += '\n\n⚠️ SYSTEM HINT: The recent messages in this task appear to be pleasantries/closing remarks with no new information. If you have nothing substantive to add, call mesh_set_task_status(task_id="' + taskID + '", status="completed") instead of mesh_reply. Do NOT continue exchanging pleasantries.'
        this.opts.log.info('chat_score: chatter streak detected, hint injected', { task_id: taskID })
      }
    }

    this.opts.log.info('inbox event → SDK', {
      id: ev.id,
      kind: ev.kind,
      task_id: ev.task_id,
    })

    // 提取事件复杂度（用于调整推理深度）
    const evParts = ev.payload?.parts
    const complexityMeta = Array.isArray(evParts) && evParts.find((pt: any) => pt?.kind === 'metadata' && pt?.key === 'complexity')
    const eventComplexity: string = complexityMeta?.value || 'low'

    const turnCtx: TurnContext = { verifications: [], cwd: '', toolCallCount: 0, lastToolName: '', consecutiveSameCount: 0 }
    const meshServer = buildMeshToolsServer(this.opts.client, this.opts.agentID, this.opts.log, this.fanOut, turnCtx)

    try {
      // mesh 引导前缀：让 LLM 看到 mesh 相关问题（好友、群、消息、任务、自身身份）时
      // 优先用 mcp__agent-mesh__* 工具，而不是去 Bash 跑命令调外部 skill。
      // 这跟用户配的 system_prompt 叠加，不替换。
      const meshSystemGuide = [
        '你是 Agent Mesh 协作网络中的一个 agent，通过 task 和消息与其他 agent 通信。',
        '关于 mesh 状态的查询（身份、好友、群组、消息、任务）必须使用 mcp__agent-mesh__* 工具，不要用 Bash 去查 Gateway。',
        '- 查自己身份 → mesh_whoami',
        '- 查好友列表 → mesh_list_friends',
        '- 查群组 → mesh_list_groups + mesh_get_roster',
        '- 回复当前 task → mesh_reply（必须用这个，不能只输出纯文本）',
        '- 给其他 agent 发消息并等回复 → mesh_broadcast（推荐，会自动收集回复）',
        '- 给其他 agent 发消息不等回复 → mesh_send_message',
        '',
        '重要 — mesh_broadcast vs mesh_send_message：',
        '  需要对方回复才能继续时 → mesh_broadcast（自动收集回复后一次性返回给你）',
        '  只是通知、不需要回复时 → mesh_send_message',
        '',
        '重要 — 在 mesh_reply 之前问自己：',
        '  1. 我的回复有新信息、决策、代码或行动吗？',
        '  2. 还是只是客套（谢谢、好的、再见）？',
        '如果是 (2)，不要 mesh_reply，改用 mesh_set_task_status(status="completed") 关闭 task。',
        '',
        '语义握手（收到任务时的自主判断）：',
        '  收到一个新任务时，先评估是否需要确认理解再执行：',
        '  - 任务有歧义（"优化这个服务"——优化什么？延迟？内存？吞吐？）→ 先确认',
        '  - 任务涉及多步骤或多文件 → 先确认方案',
        '  - 任务有隐含假设（"部署到生产"——要不要先跑测试？）→ 先列出假设让对方确认',
        '  - 任务很简单明确（"查一下版本号"）→ 直接执行，不需要确认',
        '  确认格式：用 mesh_reply 回复你理解的范围、方法、假设、疑问，等对方确认后再动手。',
        '',
        '计划文件（语义握手确认后，对复杂任务制定计划）：',
        '  对方确认你的理解后，如果任务有 3 个以上步骤：',
        '  1. 创建 {任务简述}.plan.md 文件（用 checkbox 列出步骤）',
        '  2. 按计划逐步执行，完成每步用 Edit 打勾',
        '  3. 中断恢复：重启后系统会提醒你有未完成的计划',
        '  注意：先握手确认，再写计划。不要收到任务就直接写计划。',
        '',
        '主动协作：',
        '  你可以主动联系好友寻求帮助。当你在工作中遇到以下情况时，应该主动发起协作：',
        '  - 需要其他领域的专业知识（用 mesh_list_friends 查看谁能帮忙）',
        '  - 需要确认业务规则或技术细节',
        '  - 需要其他 agent 配合完成某个步骤',
        '  使用 mesh_send_message 或 mesh_broadcast 联系相关 agent，不要等待指令。',
        '',
        '群组通知：',
        '  完成重要工作后，可以用 mesh_notify_group 通知群组成员（不需要回复的单向通知）。',
        '',
        '验证清单（完成编码任务后必须执行）：',
        '  □ 文件已创建/修改 → 用 Read 确认文件内容正确',
        '  □ 编译通过 → 用 Bash 跑 go build 或 bun build',
        '  □ 基本功能正确 → 用 Bash 跑 go test 或 go run',
        '  每项验证后在回复中说明结果。跳过的项说明原因。',
        '  注意：系统会自动检测你是否执行了验证，并在回复末尾追加标注。',
        '',
        '知识管理：',
        '  你有两种笔记：',
        '  1. notes/memory.md — 永久记忆（跨任务通用知识）',
        '     写入：项目事实、代码规范、协作偏好、永久性发现',
        '     维护：保持精简（< 1KB），过时条目主动删除，不要无限追加',
        '     注意：这个文件每次推理都注入你的上下文',
        '  2. notes/plans/{计划名}.md — 工作笔记（当前任务特定）',
        '     写入：当前任务的中间发现、临时决策、待确认事项',
        '     生命周期：计划完成后不再修改',
        '  写入规则：',
        '  - 发现永久有用的知识 → 写 memory.md（用 Edit 工具修改，保持精简）',
        '  - 当前任务的临时笔记 → 追加到 plans/{计划名}.md',
        '  - memory.md 超过 1KB 时 → 主动整理，删除过时条目',
        '',
        '语言要求：所有沟通使用中文。',
      ].join('\n')

      // 工作目录优先级：用户配置 > 项目 workspace/{agentID}（由 WORKSPACE_ROOT 环境变量或默认路径决定）
      const workspaceRoot = process.env.WORKSPACE_ROOT || join(dirname(dirname(this.opts.cursorPath)), 'workspace')
      const defaultWorkspace = join(workspaceRoot, this.opts.agentID)
      const agentCwd = this.workspacePath || defaultWorkspace
      turnCtx.cwd = agentCwd // 事后独立验证用
      const { mkdir: mkdirFs } = await import('node:fs/promises')
      await mkdirFs(agentCwd, { recursive: true })

      const sysPrompts: string[] = [meshSystemGuide]
      if (this.systemPrompt) sysPrompts.push(this.systemPrompt)
      // 队友摘要（启动时从好友/群组拉取的 headline）
      if (this.teammatesSummary) sysPrompts.push(this.teammatesSummary)
      // 个人笔记（启动时从 notes/ 读取的历史知识）
      if (this.notesSummary) sysPrompts.push(this.notesSummary)
      // 告诉 agent 它的工作目录
      sysPrompts.push(`你的工作目录是：${agentCwd}\n创建文件和项目时，默认放在此目录下，不要使用 /tmp 或其他随意路径。`)
      // 强制中文输出（放最后，优先级最高）
      sysPrompts.push('【强制规则】你的所有输出、思考、回复、与其他 agent 的对话必须使用中文。工具参数中的 text 字段也必须是中文。这是不可违反的硬性要求。')

      // 持久 session：每个 agent 用独立的 cwd + continue 模式。
      // SDK 会自动在该 cwd 下维护 session 文件，跨重启保持对话历史。
      // 比 sessionId + resume 更可靠（SDK 内部管理文件生命周期）。

      const result = query({
        prompt: userMsg,
        options: {
          model: this.opts.model,
          continue: this.sessionStarted, // 第一次 false（新对话），之后 true（继续）
          cwd: agentCwd,
          systemPrompt: sysPrompts,
          mcpServers: { 'agent-mesh': meshServer },
          pathToClaudeCodeExecutable: process.env.CLAUDE_CODE_EXECUTABLE || undefined,

          // ── 推理深度：根据任务复杂度调整 ──
          effort: eventComplexity === 'high' ? 'high' : eventComplexity === 'medium' ? 'medium' : 'low',
          thinking: eventComplexity === 'high'
            ? { type: 'adaptive' as const }
            : eventComplexity === 'medium'
              ? { type: 'adaptive' as const }
              : { type: 'disabled' as const },

          // ── 硬性防护：防止无限循环和成本失控 ──
          maxTurns: 30,
          maxBudgetUsd: 5.0,

          // ── Hooks：PostToolUse 拦截工具结果用于精确行为标注 ──
          hooks: {
            PostToolUse: [{
              hooks: [async (event: any) => {
                const toolName = event?.tool_name || ''
                const exitCode = event?.tool_result?.exit_code
                const cmd = event?.tool_input?.command || ''

                // ── 行为标注 ──
                if (toolName === 'Bash' && exitCode === 0) {
                  if (cmd.includes('go build')) turnCtx.verifications.push('编译通过(exit 0)')
                  else if (cmd.includes('go test')) turnCtx.verifications.push('测试通过(exit 0)')
                  else if (cmd.includes('go run')) turnCtx.verifications.push('运行成功(exit 0)')
                  else if (cmd.includes('bun build') || cmd.includes('tsc')) turnCtx.verifications.push('TS编译通过(exit 0)')
                } else if (toolName === 'Bash' && exitCode !== undefined && exitCode !== 0) {
                  turnCtx.verifications.push(`命令失败(exit ${exitCode}): ${cmd.slice(0, 50)}`)
                }

                // ── Circuit Breaker：工具调用计数 ──
                turnCtx.toolCallCount++
                if (toolName === turnCtx.lastToolName) {
                  turnCtx.consecutiveSameCount++
                } else {
                  turnCtx.consecutiveSameCount = 1
                  turnCtx.lastToolName = toolName
                }

                // 连续相同工具 ≥ 10 次 → 警告
                if (turnCtx.consecutiveSameCount === 10) {
                  return {
                    systemMessage: '⚠️ 你连续调用了同一个工具 10 次，可能陷入了循环。请停下来重新思考方法。',
                    continue: true,
                  }
                }
                // 总调用 ≥ 200 次 → 硬中断
                if (turnCtx.toolCallCount >= 200) {
                  return { continue: false, stopReason: 'circuit_breaker: 工具调用超过 200 次上限' }
                }

                // ── Preemptive Compaction：渐进式警告 ──
                if (turnCtx.toolCallCount === 80) {
                  return { systemMessage: '提示：上下文使用过半，注意效率，优先完成核心任务。', continue: true }
                }
                if (turnCtx.toolCallCount === 130) {
                  return { systemMessage: '⚠️ 上下文即将耗尽，请尽快完成当前任务并回复。如有重要发现请先写入 notes/。', continue: true }
                }

                return { continue: true }
              }],
            }],
            Stop: [{
              hooks: [async (event: any) => {
                const lastMsg = event?.last_assistant_message || ''
                // 任务完成时提醒 agent 记录知识
                const completionSignals = ['完成', '已创建', '已修改', '修复', '实现了', '写好了']
                if (completionSignals.some(s => lastMsg.includes(s))) {
                  return {
                    systemMessage: '任务已完成。如果发现了永久有用的知识，写入 notes/memory.md（保持精简）。如果是当前任务的临时笔记，写入 notes/plans/ 对应文件。',
                    continue: true,
                  }
                }
                return { continue: false }
              }],
            }],
            PreCompact: [{
              hooks: [async () => {
                return {
                  systemMessage: '上下文即将压缩。如果有重要发现尚未记录：永久知识写 notes/memory.md，任务笔记写 notes/plans/ 对应文件。压缩后你将无法回忆当前对话细节。',
                  continue: true,
                }
              }],
            }],
            PostCompact: [{
              hooks: [async () => {
                return {
                  systemMessage: '上下文已压缩。你的永久记忆在 notes/memory.md（已注入上下文），工作笔记在 notes/plans/。如需恢复任务细节，请 Read 对应的 plan 笔记。',
                  continue: true,
                }
              }],
            }],
          },

          // debug 收集 SDK 内部请求/错误细节
          stderr: (data: string) => {
            const trimmed = data.trim()
            if (trimmed) {
              this.opts.log.debug('SDK stderr', { line: trimmed.slice(0, 500) })
            }
          },
          // Agent 拥有完整的 Claude Code 能力（Bash/Read/Write/WebSearch）
          // + mesh MCP 工具（通信协作）。不限制 allowedTools，让 agent 自主决定用什么工具。
          permissionMode: 'bypassPermissions',
        },
      })

      // 消费 stream 直到结束。
      // Debug：打印每条消息的简要类型，便于排查"为什么模型不回复"。
      for await (const msg of result) {
        const m = msg as any
        if (m.type === 'assistant') {
          // assistant 消息含模型当前轮的完整输出（text + tool_use 块）
          const blocks: string[] = []
          for (const c of m.message?.content ?? []) {
            if (c.type === 'text') {
              const t = String(c.text ?? '')
              blocks.push(`text:${t.length > 80 ? t.slice(0, 80) + '...' : t}`)
            } else if (c.type === 'tool_use') {
              blocks.push(`tool_use:${c.name}`)
              // 行为标注：记录验证性工具调用
              const name = c.name as string
              if (name === 'Bash') {
                const cmd = c.input?.command || ''
                if (cmd.includes('go build')) turnCtx.verifications.push('编译验证')
                else if (cmd.includes('go test')) turnCtx.verifications.push('测试验证')
                else if (cmd.includes('go run')) turnCtx.verifications.push('运行验证')
                else if (cmd.includes('bun build') || cmd.includes('tsc')) turnCtx.verifications.push('TS 编译验证')
                else if (cmd.includes('curl') || cmd.includes('wget')) turnCtx.verifications.push('接口验证')
                else turnCtx.verifications.push('命令执行')
              } else if (name === 'Read') {
                turnCtx.verifications.push('文件读取确认')
              }
            } else {
              blocks.push(c.type)
            }
          }
          this.opts.log.info('SDK assistant', { event_id: ev.id, blocks })
        } else if (m.type === 'result') {
          this.opts.log.info('SDK turn done', {
            event_id: ev.id,
            stop_reason: m.stop_reason,
            num_turns: m.num_turns,
            is_error: m.is_error,
          })
          this.sessionStarted = true
        }
      }
    } catch (e) {
      const errStr = String(e)
      // continue 模式下 session 找不到 → 重置为新对话重试
      if (errStr.includes('No conversation found') && this.sessionStarted) {
        this.opts.log.warn('SDK session not found, starting fresh', {})
        this.sessionStarted = false
        try {
          await this.feedPromptToSDK(userMsg)
        } catch (retryErr) {
          this.opts.log.error('SDK query failed on retry', { event_id: ev.id, err: String(retryErr) })
        }
        return
      }
      this.opts.log.error('SDK query failed', { event_id: ev.id, err: errStr })
    }
  }

  /**
   * 喂一条 prompt 给 SDK（用于 fan-out 汇总投递）。
   * 逻辑跟 handleEvent 里的 SDK 调用一样，但不依赖 ev 对象。
   */
  private async feedPromptToSDK(prompt: string): Promise<void> {
    const turnCtx: TurnContext = { verifications: [], cwd: '', toolCallCount: 0, lastToolName: '', consecutiveSameCount: 0 }
    const meshServer = buildMeshToolsServer(this.opts.client, this.opts.agentID, this.opts.log, this.fanOut, turnCtx)
    try {
      const meshSystemGuide = [
        '你是 Agent Mesh 协作网络中的一个 agent。',
        '- 回复 task → mesh_reply',
        '- 问其他 agent 并等回复 → mesh_broadcast（推荐）',
        '- 通知其他 agent 不等回复 → mesh_send_message',
        '',
        '这是一条广播汇总回复。分析所有回复后，用 mesh_reply 向原始请求者汇报总结。',
        '',
        '在 mesh_reply 之前问自己：我的回复有新信息吗？如果只是客套，用 mesh_set_task_status(status="completed") 关闭。',
        '',
        '语言要求：所有沟通使用中文。',
      ].join('\n')

      // 工作目录优先级：用户配置 > 项目 workspace/{agentID}
      const workspaceRoot = process.env.WORKSPACE_ROOT || join(dirname(dirname(this.opts.cursorPath)), 'workspace')
      const defaultWorkspace = join(workspaceRoot, this.opts.agentID)
      const agentCwd = this.workspacePath || defaultWorkspace
      turnCtx.cwd = agentCwd // 事后独立验证用
      const { mkdir: mkdirFs } = await import('node:fs/promises')
      await mkdirFs(agentCwd, { recursive: true })

      const sysPrompts: string[] = [meshSystemGuide]
      if (this.systemPrompt) sysPrompts.push(this.systemPrompt)
      // 队友摘要（启动时从好友/群组拉取的 headline）
      if (this.teammatesSummary) sysPrompts.push(this.teammatesSummary)
      // 个人笔记（启动时从 notes/ 读取的历史知识）
      if (this.notesSummary) sysPrompts.push(this.notesSummary)
      sysPrompts.push(`你的工作目录是：${agentCwd}\n创建文件和项目时，默认放在此目录下，不要使用 /tmp 或其他随意路径。`)
      sysPrompts.push('【强制规则】你的所有输出、思考、回复、与其他 agent 的对话必须使用中文。工具参数中的 text 字段也必须是中文。这是不可违反的硬性要求。')

      const result = query({
        prompt,
        options: {
          model: this.opts.model,
          continue: this.sessionStarted,
          cwd: agentCwd,
          systemPrompt: sysPrompts,
          mcpServers: { 'agent-mesh': meshServer },
          pathToClaudeCodeExecutable: process.env.CLAUDE_CODE_EXECUTABLE || undefined,
          // fan-out 汇总通常是中等复杂度
          effort: 'medium',
          thinking: { type: 'adaptive' as const },
          maxTurns: 20,
          maxBudgetUsd: 3.0,
          hooks: {
            PostToolUse: [{
              hooks: [async (event: any) => {
                const toolName = event?.tool_name || ''
                const exitCode = event?.tool_result?.exit_code
                const cmd = event?.tool_input?.command || ''
                if (toolName === 'Bash' && exitCode === 0) {
                  if (cmd.includes('go build')) turnCtx.verifications.push('编译通过(exit 0)')
                  else if (cmd.includes('go test')) turnCtx.verifications.push('测试通过(exit 0)')
                } else if (toolName === 'Bash' && exitCode !== undefined && exitCode !== 0) {
                  turnCtx.verifications.push(`命令失败(exit ${exitCode})`)
                }
                return { continue: true }
              }],
            }],
          },
          permissionMode: 'bypassPermissions',
        },
      })
      for await (const msg of result) {
        const m = msg as any
        if (m.type === 'result') {
          this.opts.log.info('SDK turn done (fan-out)', { stop_reason: m.stop_reason })
          this.sessionStarted = true
        }
      }
    } catch (e) {
      this.opts.log.error('SDK query failed (fan-out)', { err: String(e) })
    }
  }

  /**
   * 初始化 workspace：确保 notes/ 目录存在，创建 CLAUDE.md（如果不存在），读取已有笔记。
   */
  private async initWorkspace(): Promise<void> {
    const { mkdir, readFile, writeFile, readdir } = await import('node:fs/promises')
    const workspaceRoot = process.env.WORKSPACE_ROOT || join(dirname(dirname(this.opts.cursorPath)), 'workspace')
    const agentCwd = this.workspacePath || join(workspaceRoot, this.opts.agentID)

    // 确保 notes/ 目录存在
    const notesDir = join(agentCwd, 'notes')
    await mkdir(notesDir, { recursive: true })
    await mkdir(join(notesDir, 'plans'), { recursive: true })

    // 创建 CLAUDE.md（如果不存在）
    const claudeMdPath = join(agentCwd, 'CLAUDE.md')
    try {
      await readFile(claudeMdPath, 'utf-8')
    } catch {
      // 文件不存在，创建默认模板
      const template = [
        `# ${this.agentName} 工作区`,
        '',
        '## 知识管理',
        '',
        '你有两种笔记：',
        '',
        '### notes/memory.md — 永久记忆',
        '跨任务通用的知识（项目事实、代码规范、协作偏好、永久性发现）。',
        '这个文件的内容每次推理都会注入你的上下文，请保持精简（< 1KB）。',
        '过时的条目主动删除，不要无限追加。',
        '',
        '### notes/plans/{计划名}.md — 工作笔记',
        '当前任务的中间发现、临时决策、待确认事项。',
        '计划完成后不再修改。不会自动注入上下文。',
        '',
      ].join('\n')
      await writeFile(claudeMdPath, template, 'utf-8')
    }

    // 创建 memory.md（如果不存在）
    const memoryPath = join(notesDir, 'memory.md')
    try {
      await readFile(memoryPath, 'utf-8')
    } catch {
      await writeFile(memoryPath, '# 永久记忆\n\n', 'utf-8')
    }

    // 读取 memory.md 全文注入 system prompt（不截断——agent 自己维护精简）
    try {
      const content = await readFile(memoryPath, 'utf-8')
      const trimmed = content.trim()
      if (trimmed && trimmed !== '# 永久记忆') {
        this.notesSummary = `你的永久记忆（notes/memory.md）：\n\n${trimmed}`
        this.opts.log.info('memory loaded', { chars: trimmed.length })
      }
    } catch { /* ignore */ }

    // 扫描未完成的计划文件（*.plan.md 中含有 "- [ ]"）
    try {
      const { readdir, readFile } = await import('node:fs/promises')
      const allFiles = await readdir(agentCwd)
      for (const f of allFiles) {
        if (!f.endsWith('.plan.md')) continue
        try {
          const content = await readFile(join(agentCwd, f), 'utf-8')
          if (content.includes('- [ ]')) {
            this.pendingPlans.push(f)
          }
        } catch { /* ignore */ }
      }
      if (this.pendingPlans.length > 0) {
        this.opts.log.info('pending plans found', { plans: this.pendingPlans })
      }
    } catch { /* ignore */ }
  }

  /**
   * 拉取好友和同群成员的 headline，构建队友摘要文本。
   * 启动时调用一次，结果注入 system prompt。
   */
  private async buildTeammatesSummary(): Promise<string> {
    const seen = new Set<string>()
    const lines: string[] = []

    try {
      // 拉好友
      const friends = await this.opts.client.listMyFriends('accepted')
      for (const f of friends.friends || []) {
        if (f.friend_agent_id === this.opts.agentID) continue
        seen.add(f.friend_agent_id)
        try {
          const profile = await this.opts.client.getAgentProfile(f.friend_agent_id)
          lines.push(`- ${profile.agent_id}：${profile.headline || profile.description || profile.name}`)
        } catch {
          lines.push(`- ${f.friend_agent_id}`)
        }
      }

      // 拉群组成员
      const groups = await this.opts.client.listMyGroups()
      for (const gid of groups.group_ids || []) {
        try {
          const roster = await this.opts.client.getRoster(gid)
          for (const m of roster.roster || []) {
            if (m.agent_id === this.opts.agentID || seen.has(m.agent_id)) continue
            seen.add(m.agent_id)
            lines.push(`- ${m.agent_id}：${m.description || m.name || ''}（群组 ${gid}）`)
          }
        } catch { /* ignore */ }
      }
    } catch (e) {
      this.opts.log.warn('buildTeammatesSummary failed', { err: String(e) })
    }

    if (lines.length === 0) return ''
    return [
      '你的队友：',
      ...lines,
      '需要了解队友的详细能力时，调用 mesh_get_agent_card(agent_id) 获取完整档案。',
    ].join('\n')
  }
}

/**
 * 把 inbox 事件格式化成 LLM 能理解的 user message。
 *
 * 不同 kind 用不同模板：
 *   - message：把 sender + 消息内容呈现给 LLM
 *   - artifact：通知有新 artifact 产出
 *   - transition：通知 task 状态变更
 */
function formatEventAsPrompt(ev: InboxEvent, _agentName: string): string {
  const p = ev.payload || {}
  switch (ev.kind) {
    case 'message': {
      const text = extractText(p.parts) || '(empty)'
      const sender = p.from_agent_id || p.role || 'someone'
      const taskID = ev.task_id
      // 检测 complexity 元数据
      const complexityPart = Array.isArray(p.parts) && p.parts.find((pt: any) => pt?.kind === 'metadata' && pt?.key === 'complexity')
      const complexity: string = complexityPart?.value || 'low'

      const lines = [
        `你在 task ${taskID} 中收到一条新消息。`,
        `发送者：${sender}`,
        `内容：`,
        text,
        ``,
      ]

      if (complexity === 'high' || complexity === 'medium') {
        // 语义握手：复杂任务先确认理解再执行
        lines.push(`⚠️ 这是一个${complexity === 'high' ? '高' : '中等'}复杂度任务，请先确认你的理解再执行：`)
        lines.push(`1. 用 mesh_reply(task_id="${taskID}") 回复你理解的范围、方法和假设`)
        lines.push(`2. 等对方确认后再动手执行`)
        lines.push(`3. 如果有疑问，在回复中列出`)
        lines.push(``)
        lines.push(`回复格式参考：`)
        lines.push(`  范围：...`)
        lines.push(`  方法：...`)
        lines.push(`  假设：...`)
        lines.push(`  疑问：...`)
      } else {
        lines.push(`请用 mesh_reply(task_id="${taskID}") 回复。`)
        lines.push(`如果你需要其他 agent 的帮助才能完成，可以用 mesh_send_message 或 mesh_broadcast 联系他们，获得信息后再回复。`)
      }

      return lines.join('\n')
    }
    case 'artifact': {
      const taskID = ev.task_id
      const name = p.name || ev.ref_id || 'unnamed'
      return [
        `队友在 task ${taskID} 中产出了一个 artifact。`,
        `名称：${name}`,
        `描述：${p.description || '(无)'}`,
        ``,
        `请用 mesh_reply(task_id="${taskID}") 回复确认或反馈。`,
      ].join('\n')
    }
    case 'transition': {
      const taskID = ev.task_id
      return [
        `Task ${taskID} 状态变更：${p.from || '?'} → ${p.to || '?'}`,
        `备注：${p.status_message || '(无)'}`,
        ``,
        `判断是否需要后续操作。`,
      ].join('\n')
    }
    case 'notification': {
      const from = p.from_agent_id || 'someone'
      const groupID = p.group_id || ''
      const text = p.text || '(empty)'
      return [
        `[群组通知] 来自 ${from}（群组 ${groupID}）：`,
        text,
        ``,
        `这是一条通知，不需要回复。如果内容与你当前工作相关，可以参考。`,
      ].join('\n')
    }
    default:
      return ''
  }
}

function extractText(parts: unknown): string {
  if (!Array.isArray(parts)) return ''
  return parts
    .filter((p: any) => p?.kind === 'text' && typeof p.text === 'string')
    .map((p: any) => p.text)
    .join('\n')
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}
