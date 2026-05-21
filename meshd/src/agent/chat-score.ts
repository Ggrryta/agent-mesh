// chat-score.ts：检测消息是否是"闲聊/套话"而非实质性工作内容。
//
// 设计原则：
//   - 纯本地计算，不调后端、不调 LLM
//   - 输出 0–1 的分数：0 = 纯工作内容，1 = 纯套话
//   - 四个信号加权：长度 / 套话 regex / 连续发送 / 间隔过短
//   - 单条不杀：只有连续多条高分才触发降级
//
// 使用方：
//   - meshd runtime.ts 在 handleEvent 时调用
//   - 高分时在 prompt 末尾附 hint 引导 LLM close task
//   - Gateway 侧有独立的 chat_streak 计数做硬兜底

/** 每个 task 的历史状态，用于计算"连续发送"和"间隔"信号。 */
export interface ChatScoreContext {
  /** 最近 N 条消息的 chat_score（用于判断连续高分） */
  recentScores: number[]
  /** 上一条消息的时间戳（ms） */
  lastMessageAt: number
  /** 上一条消息的发送方 */
  lastSender: string
  /** 同一发送方连续发送的次数 */
  consecutiveSameSender: number
}

export function newChatScoreContext(): ChatScoreContext {
  return {
    recentScores: [],
    lastMessageAt: 0,
    lastSender: '',
    consecutiveSameSender: 0,
  }
}

/** 计算单条消息的 chat_score (0–1)。同时更新 context 状态。 */
export function computeChatScore(
  text: string,
  sender: string,
  now: number,
  ctx: ChatScoreContext,
): number {
  let score = 0

  // ── Signal 1: 长度过短（< 50 字符 → 0.3 分）──
  // 实质性消息通常包含代码、路径、详细描述，很少低于 50 字符。
  // "Thanks! 👋" = 10 字符，"收到，我来处理" = 7 字符
  if (text.length < 50) {
    score += 0.3
  } else if (text.length < 100) {
    score += 0.1
  }

  // ── Signal 2: 套话 regex（匹配常见客套/告别用语 → 0.35 分）──
  if (isPleasantry(text)) {
    score += 0.35
  }

  // ── Signal 3: 同一发送方连续发送（≥ 2 次 → 0.15 分）──
  // 正常协作是来回交替；同一方连续发多条通常是"补充客气话"
  if (sender === ctx.lastSender) {
    ctx.consecutiveSameSender++
  } else {
    ctx.consecutiveSameSender = 1
  }
  if (ctx.consecutiveSameSender >= 2) {
    score += 0.15
  }

  // ── Signal 4: 间隔过短（< 15 秒 → 0.2 分）──
  // agent 认真思考 + 写代码通常 30s+；< 15s 的快速来回是机器人式客套
  if (ctx.lastMessageAt > 0) {
    const gap = now - ctx.lastMessageAt
    if (gap < 15_000) {
      score += 0.2
    } else if (gap < 30_000) {
      score += 0.05
    }
  }

  // 更新 context
  ctx.lastMessageAt = now
  ctx.lastSender = sender
  ctx.recentScores.push(Math.min(score, 1))
  if (ctx.recentScores.length > 10) {
    ctx.recentScores.shift()
  }

  return Math.min(score, 1)
}

/** 判断最近是否处于"闲聊连击"状态（连续 N 条高分）。 */
export function isChatterStreak(ctx: ChatScoreContext, threshold = 0.6, minStreak = 3): boolean {
  const scores = ctx.recentScores
  if (scores.length < minStreak) return false
  const tail = scores.slice(-minStreak)
  return tail.every((s) => s >= threshold)
}

// ── 套话检测 ──

const PLEASANTRY_PATTERNS = [
  // 英文
  /\b(thanks?|thank you|cheers|bye|goodbye|take care|see you|have a (good|great|nice)|happy coding|no worries|sounds good|got it|noted|will do|roger|acknowledged)\b/i,
  // emoji 为主（3 个以上 emoji 且总长 < 30）
  /^[\s\p{Emoji}\p{Emoji_Presentation}\p{Emoji_Modifier}\p{Emoji_Component}]{1,30}$/u,
  // 中文
  /^(收到|好的|了解|明白|没问题|辛苦了|感谢|谢谢|拜拜|再见|加油|保持联系|随时找我|有问题随时|互相学习|一起进步|祝顺利).{0,20}$/,
  // 纯确认（极短 + 无实质）
  /^(ok|okay|sure|yep|yeah|yes|no|nope|done|ack|👍|🤝|👋|✅|🙌|😄|lgtm|sgtm)\s*[!.]*$/i,
]

function isPleasantry(text: string): boolean {
  const trimmed = text.trim()
  // 长文本不可能是纯套话
  if (trimmed.length > 200) return false
  return PLEASANTRY_PATTERNS.some((re) => re.test(trimmed))
}
