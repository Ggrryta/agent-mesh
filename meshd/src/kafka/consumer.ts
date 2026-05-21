// kafka/consumer.ts — Kafka consumer for inbox events.
//
// Phase 2：替代 HTTP 长轮询，延迟从 ~500ms 降到 ~10ms。
// 每个 agent 用独立的 consumer group（groupId = meshd-{agentID}），
// 只消费 key 匹配自己 agentID 的消息。
//
// Kafka topic "inbox.events" 的消息格式（Gateway 双写的 payload）：
//   key = agent_id（接收方）
//   value = JSON（完整的 inbox event payload）

import { Kafka, Consumer, EachMessagePayload, logLevel } from 'kafkajs'
import type { Logger } from '../log.ts'

export interface KafkaConsumerOpts {
  brokers: string // 逗号分隔
  agentID: string
  topic: string // 默认 "inbox.events"
  log: Logger
}

export interface InboxKafkaEvent {
  kind: string
  task_id: string
  agent_id?: string
  payload: any
  // 从 Kafka message 解析出的字段
  [key: string]: any
}

export class InboxKafkaConsumer {
  private consumer: Consumer | null = null
  private kafka: Kafka
  private running = false
  private opts: KafkaConsumerOpts

  constructor(opts: KafkaConsumerOpts) {
    this.opts = opts
    const brokers = opts.brokers.split(',').map((b) => b.trim())
    this.kafka = new Kafka({
      clientId: `meshd-${opts.agentID}`,
      brokers,
      logLevel: logLevel.WARN,
      retry: { retries: 5 },
    })
  }

  /**
   * 启动消费。onEvent 回调在收到属于本 agent 的消息时触发。
   * 阻塞直到 stop() 被调用。
   */
  async start(onEvent: (ev: InboxKafkaEvent) => Promise<void>): Promise<void> {
    const groupId = `meshd-${this.opts.agentID}`
    this.consumer = this.kafka.consumer({
      groupId,
      // 从最新开始消费（历史消息已经通过 HTTP poll 处理过了）
      // 新 agent 首次启动时不需要回放 Kafka 历史
    })

    await this.consumer.connect()
    await this.consumer.subscribe({ topic: this.opts.topic, fromBeginning: false })

    this.running = true
    this.opts.log.info('kafka consumer started', { group: groupId, topic: this.opts.topic })

    await this.consumer.run({
      eachMessage: async (payload: EachMessagePayload) => {
        if (!this.running) return

        const { message } = payload
        const key = message.key?.toString() || ''

        // 只处理属于本 agent 的消息
        if (key !== this.opts.agentID) return

        try {
          const value = message.value?.toString()
          if (!value) return

          const parsed = JSON.parse(value)
          // 推断 event 结构：Gateway 双写的是 inbox event 的 payload（即 message/artifact/transition 的 JSON）
          const event: InboxKafkaEvent = {
            kind: parsed.role ? 'message' : parsed.from ? 'timeline_update' : 'message',
            task_id: parsed.task_id || '',
            payload: parsed,
          }

          // 如果有 message_id 说明是 message 类型
          if (parsed.message_id) {
            event.kind = 'message'
          }

          await onEvent(event)
        } catch (e) {
          this.opts.log.warn('kafka: failed to process message', { err: String(e), offset: message.offset })
        }
      },
    })
  }

  async stop(): Promise<void> {
    this.running = false
    if (this.consumer) {
      await this.consumer.disconnect()
      this.consumer = null
      this.opts.log.info('kafka consumer stopped')
    }
  }
}
