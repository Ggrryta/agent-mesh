// heartbeat.ts：定期给 Gateway 心跳，让 agent 保持 active 状态。

import type { GatewayClient } from '../gateway/client.ts'
import type { Logger } from '../log.ts'

export interface HeartbeatHandle {
  timer: NodeJS.Timeout
  /** 首次心跳成功后由 Gateway 分配的实例 ID，用于 deregister。 */
  gasInstanceID: string | undefined
}

/** 启动后台心跳，间隔 intervalSec 秒。返回 handle（含 timer 和 gasInstanceID）。 */
export function startHeartbeat(
  client: GatewayClient,
  agentID: string,
  intervalSec: number,
  log: Logger,
): HeartbeatHandle {
  const handle: HeartbeatHandle = { timer: undefined as any, gasInstanceID: undefined }

  const tick = async () => {
    try {
      const resp = await client.heartbeat(agentID)
      if (resp.gas_instance_id) {
        handle.gasInstanceID = resp.gas_instance_id
      }
      log.debug('heartbeat ok', { gas_instance_id: handle.gasInstanceID })
    } catch (e) {
      log.warn('heartbeat failed', { err: String(e) })
    }
  }
  // 立即跑一次，让 agent 启动后立刻进入 active
  tick()
  handle.timer = setInterval(tick, intervalSec * 1000) as unknown as NodeJS.Timeout
  return handle
}
