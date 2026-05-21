// mesh-tools.ts：暴露给 LLM 的 mesh 通信工具。
//
// 通过 Claude Agent SDK 的 createSdkMcpServer + tool() 注入。
//
// 工具描述写得直白且强势——agent 收到任何"mesh 业务"问题（好友、群、消息、任务、
// 自身身份）必须用这里的工具，不要去跑 Bash 或调外部 skill 自查。每个 tool 都
// 在 description 里明确"用于 X 的唯一正确方式"，避免 LLM 看到熟悉的 Bash 就上。

import { tool, createSdkMcpServer } from '@anthropic-ai/claude-agent-sdk'
import { z } from 'zod'
import type { GatewayClient } from '../gateway/client.ts'
import type { Logger } from '../log.ts'
import type { FanOutCollector, FanOutGroup, FanOutPolicy } from '../agent/fan-out.ts'

/** 启发式推断任务复杂度 */
function inferComplexity(text: string): 'low' | 'medium' | 'high' {
  const highSignals = ['重构', '架构', '设计', '迁移', '从零', '全面', '系统性', '多模块']
  if (highSignals.some(s => text.includes(s))) return 'high'
  if (text.length > 200 || text.includes('步骤') || text.includes('然后') || text.includes('并且')) return 'medium'
  return 'low'
}

/**
 * 事后独立验证：扫描回复文本中的可验证声明，runtime 自己确认。
 * 只验证低成本、高确定性的声明（文件存在、编译通过）。
 */
async function runIndependentVerification(replyText: string, cwd: string, log: Logger): Promise<string[]> {
  const results: string[] = []
  if (!cwd) return results

  const { exec } = await import('node:child_process')
  const { stat } = await import('node:fs/promises')
  const { promisify } = await import('node:util')
  const execAsync = promisify(exec)

  // 1. 检测文件路径声明 → stat 确认存在
  const filePathPattern = /(?:创建|写入|生成|保存).*?[`"']?([/\w.\-@]+\.\w{1,5})[`"']?/g
  let match: RegExpExecArray | null
  const checkedFiles = new Set<string>()
  while ((match = filePathPattern.exec(replyText)) !== null) {
    const filePath = match[1]
    if (checkedFiles.has(filePath)) continue
    checkedFiles.add(filePath)
    try {
      const fullPath = filePath.startsWith('/') ? filePath : `${cwd}/${filePath}`
      await stat(fullPath)
      results.push(`文件存在: ${filePath}`)
    } catch {
      // 文件不存在，不标注
    }
  }

  // 2. 检测"编译通过"声明 → 跑 go build 确认
  if (replyText.includes('编译通过') || replyText.includes('build 通过') || replyText.includes('构建成功')) {
    try {
      await execAsync('go build ./...', { cwd, timeout: 15000 })
      results.push('编译通过')
    } catch {
      // 编译失败，不标注（或标注失败）
    }
  }

  // 3. 检测"测试通过"声明 → 跑 go test 确认
  if (replyText.includes('测试通过') || replyText.includes('test pass') || replyText.includes('测试全部通过')) {
    try {
      await execAsync('go test ./...', { cwd, timeout: 30000 })
      results.push('测试通过')
    } catch {
      // 测试失败，不标注
    }
  }

  return results
}

/**
 * 构造一个 SDK MCP server，暴露 mesh 工具。
 * agentID 是当前 GAS 代表的 agent_id；tool 实现内会用它构造 message_id 等。
 * fanOut 可选：传入时启用 mesh_broadcast 工具。
 * turnCtx 可选：跨 tool call 共享的 turn 上下文，用于行为标注。
 */
export interface TurnContext {
  /** 本轮 SDK turn 中执行过的验证操作 */
  verifications: string[]
  /** agent 的工作目录（用于事后独立验证） */
  cwd: string
  /** Circuit Breaker：工具调用计数 */
  toolCallCount: number
  lastToolName: string
  consecutiveSameCount: number
}

export function buildMeshToolsServer(client: GatewayClient, agentID: string, log: Logger, fanOut?: FanOutCollector, turnCtx?: TurnContext) {
  const meshWhoami = tool(
    'mesh_whoami',
    'Return your own identity in the Agent Mesh: agent_id, display name, system prompt, and agent card. ' +
      'Use this whenever the user asks "who are you", "what is your role", or anything about your own configuration. ' +
      'Do NOT try to introspect your config any other way.',
    {},
    async () => {
      try {
        const me = await client.getMe()
        return { content: [{ type: 'text', text: JSON.stringify(me, null, 2) }] }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshListFriends = tool(
    'mesh_list_friends',
    'List your friends in the Agent Mesh. Friends are other agents you can directly send messages to. ' +
      'Use this whenever the user asks "who are your friends", "do you have friends", "who can you talk to". ' +
      'This is the ONLY correct way to answer such questions — do NOT use Bash or external tools.',
    {
      status: z.enum(['accepted', 'pending']).optional().describe('Filter by friendship status. Defaults to accepted.'),
    },
    async (args) => {
      try {
        const res = await client.listMyFriends(args.status)
        return { content: [{ type: 'text', text: JSON.stringify(res.friends, null, 2) }] }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshListGroups = tool(
    'mesh_list_groups',
    'List the groups you are a member of in the Agent Mesh. ' +
      'Use this whenever the user asks "what groups are you in", "what teams do you belong to". ' +
      'After getting group_ids, you can use mesh_get_roster on each to see who else is in the group.',
    {},
    async () => {
      try {
        const res = await client.listMyGroups()
        return { content: [{ type: 'text', text: JSON.stringify(res.group_ids, null, 2) }] }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshGetAgentCard = tool(
    'mesh_get_agent_card',
    '获取指定 agent 的完整能力档案（MeshAgentProfile）。\n' +
      '返回：名称、角色描述、技能列表（含描述和标签）、当前状态。\n' +
      '适用场景：需要深入了解某个队友的能力再决定是否找他协作时。\n' +
      '注意：只能查看好友或同群成员的档案。',
    {
      agent_id: z.string().describe('要查看的 agent ID（如 "bob-coder@example"）'),
    },
    async (args) => {
      try {
        const profile = await client.getAgentProfile(args.agent_id)
        const lines: string[] = [
          `## ${profile.name}（${profile.agent_id}）`,
          profile.headline || profile.description || '',
          `状态：${profile.status}`,
        ]
        if (profile.skills && profile.skills.length > 0) {
          lines.push('', '技能：')
          for (const s of profile.skills) {
            lines.push(`  - ${s.name}：${s.description}${s.tags?.length ? ` [${s.tags.join(', ')}]` : ''}`)
          }
        }
        return { content: [{ type: 'text', text: lines.join('\n') }] }
      } catch (e) {
        return { content: [{ type: 'text', text: `获取失败：${String(e)}` }], isError: true }
      }
    },
  )

  const meshSendMessage = tool(
    'mesh_send_message',
    '给其他 agent 发消息。两种模式：\n' +
      '- 不带 task_id：创建新 task（首次联系或新话题）\n' +
      '- 带 task_id：在已有 task 中继续对话（多轮）\n\n' +
      '目标必须是你的好友或同群成员。',
    {
      to_agent_id: z.string().describe('目标 agent ID（如 "bob-coder@example"）'),
      text: z.string().describe('消息内容（必须使用中文）'),
      task_id: z.string().optional().describe('如果提供，追加消息到已有 task（多轮对话）。不提供则创建新 task。'),
      complexity: z.enum(['low', 'medium', 'high']).optional().describe('任务复杂度。medium/high 会触发语义握手（接收方先确认理解再执行）。不指定则自动推断。'),
      preview: z.string().optional().describe('可选的简短摘要，群组其他成员可见'),
      context_id: z.string().optional().describe('已有的协作 context，不提供则自动生成'),
    },
    async (args) => {
      try {
        // 复杂度：显式指定或启发式推断
        const complexity = args.complexity || inferComplexity(args.text)

        if (args.task_id) {
          // 多轮模式：追加消息到已有 task
          const messageID = `m-${agentID}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
          await client.appendMessage(args.task_id, {
            message_id: messageID,
            parts: [{ kind: 'text', text: args.text }],
            preview: args.preview,
          })
          log.info('mesh_send_message (continue)', { to: args.to_agent_id, task: args.task_id })
          return {
            content: [{ type: 'text', text: `消息已追加到 task ${args.task_id}，对方会收到。` }],
          }
        }

        // 新建模式：创建新 task
        const taskID = `t-${agentID}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
        const messageID = `m-${taskID}`
        // 把 complexity 嵌入 parts 作为元数据，接收方 runtime 可以读取
        const parts: any[] = [{ kind: 'text', text: args.text }]
        if (complexity !== 'low') {
          parts.push({ kind: 'metadata', key: 'complexity', value: complexity })
        }
        const res = await client.submitTask({
          task_id: taskID,
          context_id: args.context_id,
          to_agent_id: args.to_agent_id,
          message: {
            message_id: messageID,
            parts,
            preview: args.preview,
          },
        })
        log.info('mesh_send_message (new)', { to: args.to_agent_id, task: res.task_id, complexity })
        return {
          content: [
            { type: 'text', text: `新 task 已创建：${res.task_id}（context=${res.context_id}）。对方会收到你的消息。` },
          ],
        }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshReply = tool(
    'mesh_reply',
    '回复当前 task 中的消息。这是回复 task 的唯一正确方式——必须通过此工具回复，不能只输出纯文本。',
    {
      task_id: z.string().describe('要回复的 task ID'),
      text: z.string().describe('回复内容（必须使用中文）'),
      preview: z.string().optional().describe('可选摘要'),
    },
    async (args) => {
      log.info('mesh_reply called', { task_id: args.task_id, text_len: args.text?.length ?? 0 })
      const messageID = `m-${agentID}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`

      // ── 三级行为标注 ──
      let annotatedText = args.text
      if (turnCtx) {
        const independentResults = await runIndependentVerification(args.text, turnCtx.cwd, log)
        const agentVerified = turnCtx.verifications.length > 0

        const annotations: string[] = []
        if (independentResults.length > 0) {
          annotations.push(`[独立验证 ✓] ${independentResults.join('、')}`)
        }
        if (agentVerified) {
          // 去掉已被独立验证覆盖的项
          const remaining = turnCtx.verifications.filter(v =>
            !independentResults.some(r => r.includes(v.replace('验证', '')))
          )
          if (remaining.length > 0) {
            annotations.push(`[agent 已验证] ${remaining.join('、')}`)
          }
        }
        if (annotations.length === 0) {
          annotations.push('[未验证] 以上结论基于推理，未执行验证命令')
        }
        annotatedText += '\n\n' + annotations.join('\n')
      }

      try {
        await client.appendMessage(args.task_id, {
          message_id: messageID,
          parts: [{ kind: 'text', text: annotatedText }],
          preview: args.preview,
        })
        log.info('mesh_reply ok', { task: args.task_id })
        return { content: [{ type: 'text', text: '回复已发送。' }] }
      } catch (e) {
        log.warn('mesh_reply failed', { task: args.task_id, err: String(e) })
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshSetTaskStatus = tool(
    'mesh_set_task_status',
    'Update a task you are working on. Use "working" when you start, "completed" when done, "failed" if you cannot complete.',
    {
      task_id: z.string(),
      status: z.enum(['working', 'completed', 'failed', 'input-required']),
      message: z.string().optional().describe('Optional human-readable status note.'),
    },
    async (args) => {
      try {
        await client.transition(args.task_id, args.status, args.message)
        return { content: [{ type: 'text', text: `Task ${args.task_id} → ${args.status}` }] }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshGetRoster = tool(
    'mesh_get_roster',
    'Get the list of teammates in a specific group, including their skills and capabilities. ' +
      'Use this AFTER mesh_list_groups when the user asks who is in a particular group.',
    {
      group_id: z.string(),
    },
    async (args) => {
      try {
        const res = await client.getRoster(args.group_id)
        return {
          content: [{ type: 'text', text: JSON.stringify(res.roster, null, 2) }],
        }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  const meshGetTimeline = tool(
    'mesh_get_timeline',
    'Get the metadata timeline of a collaboration context. Shows messages between teammates with previews. Useful to catch up on what others have been doing.',
    {
      context_id: z.string(),
      since: z.number().optional(),
      limit: z.number().optional(),
    },
    async (args) => {
      try {
        const res = await client.getTimeline(args.context_id, args.since ?? 0, args.limit ?? 50)
        return {
          content: [{ type: 'text', text: JSON.stringify(res.entries, null, 2) }],
        }
      } catch (e) {
        return { content: [{ type: 'text', text: `Failed: ${String(e)}` }], isError: true }
      }
    },
  )

  // ── mesh_notify_group：群组通知（单向广播，不期望回复）──

  const meshNotifyGroup = tool(
    'mesh_notify_group',
    '向群组所有成员发送通知（单向推送，不需要回复）。\n' +
      '适用场景：状态更新、进度通报、结果公告、重要信息同步。\n' +
      '注意：这不会创建 task，接收方只会看到一条通知消息，不需要回复。',
    {
      group_id: z.string().describe('目标群组 ID'),
      text: z.string().describe('通知内容（必须使用中文）'),
    },
    async (args) => {
      try {
        const res = await client.notifyGroup(args.group_id, args.text)
        log.info('mesh_notify_group', { group: args.group_id, notified: res.notified })
        return {
          content: [{ type: 'text', text: `通知已发送给群组 ${args.group_id} 的 ${res.notified} 位成员。` }],
        }
      } catch (e) {
        return { content: [{ type: 'text', text: `通知失败：${String(e)}` }], isError: true }
      }
    },
  )

  // ── mesh_broadcast：1:N 广播 + 回复汇总 ──

  const meshBroadcast = tool(
    'mesh_broadcast',
    '向多个 agent 同时发送消息并等待汇总回复。\n' +
      '适用场景：需要多人意见时。\n' +
      '策略：wait_all（默认，等所有人或超时）、wait_any（第一个回复即触发）、wait_n:K（等 K 个回复）。\n' +
      '调用后不需要做任何事——回复会自动收集并作为一条汇总消息返回给你。',
    {
      to_agent_ids: z.array(z.string()).min(1).describe('目标 agent ID 列表'),
      text: z.string().describe('发送给所有目标的消息内容（必须使用中文）'),
      complexity: z.enum(['low', 'medium', 'high']).optional().describe('任务复杂度。medium/high 触发语义握手。'),
      policy: z.string().optional().default('wait_all').describe('回复策略：wait_all | wait_any | wait_n:K'),
      timeout_sec: z.number().int().min(10).max(600).optional().default(120).describe('等待超时秒数'),
      context_id: z.string().optional().describe('共享 context（不提供则自动生成）'),
    },
    async (args) => {
      if (!fanOut) {
        return { content: [{ type: 'text', text: 'Fan-out not available (collector not initialized).' }], isError: true }
      }

      const fanOutID = `fo-${agentID}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
      const contextID = args.context_id || `ctx-${fanOutID}`
      const childTasks = new Map<string, string>()

      for (const targetID of args.to_agent_ids) {
        const taskID = `t-${agentID}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
        const messageID = `m-${taskID}`
        try {
          const res = await client.submitTask({
            task_id: taskID,
            context_id: contextID,
            to_agent_id: targetID,
            message: {
              message_id: messageID,
              parts: [{ kind: 'text', text: args.text }],
              preview: `[broadcast] ${args.text.slice(0, 50)}`,
            },
          })
          childTasks.set(res.task_id, targetID)
          log.info('broadcast: child task created', { fan_out_id: fanOutID, to: targetID, task: res.task_id })
        } catch (e) {
          log.warn('broadcast: child task failed', { fan_out_id: fanOutID, to: targetID, err: String(e) })
        }
      }

      if (childTasks.size === 0) {
        return { content: [{ type: 'text', text: 'All child task submissions failed. Check that targets are your friends or in your group.' }], isError: true }
      }

      const group: FanOutGroup = {
        fanOutID,
        originalText: args.text,
        policy: (args.policy || 'wait_all') as FanOutPolicy,
        deadlineMs: Date.now() + (args.timeout_sec ?? 120) * 1000,
        childTasks,
        replies: new Map(),
        resolved: false,
      }
      await fanOut.register(group)

      return {
        content: [{
          type: 'text',
          text: `Broadcast sent (id=${fanOutID}). ${childTasks.size} child task(s) created. ` +
            `Policy: ${args.policy}. Timeout: ${args.timeout_sec}s. ` +
            `Replies will be aggregated and delivered to you automatically. No action needed now.`,
        }],
      }
    },
  )

  return createSdkMcpServer({
    name: 'agent-mesh',
    version: '0.1.0',
    tools: [
      meshWhoami,
      meshListFriends,
      meshListGroups,
      meshGetAgentCard,
      meshSendMessage,
      meshReply,
      meshSetTaskStatus,
      meshGetRoster,
      meshGetTimeline,
      meshNotifyGroup,
      meshBroadcast,
    ],
  })
}
