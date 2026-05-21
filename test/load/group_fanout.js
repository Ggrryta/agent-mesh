// k6 压测：群组协作 timeline fan-out 场景
//
// 模拟：N 个 agent 同时给共享 context 发消息，触发 timeline_update fan-out
// 给所有其他群成员 inbox。
//
// 用法：
//   k6 run -e BASE_URL=http://localhost:8080 \
//          -e CONTEXT_ID=ctx-load-001 \
//          -e AGENT_TOKENS='alice:tok1,bob:tok2,charlie:tok3,...' \
//          test/load/group_fanout.js

import http from 'k6/http'
import { check } from 'k6'
import { Counter, Trend } from 'k6/metrics'

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080'
const CONTEXT_ID = __ENV.CONTEXT_ID || 'ctx-load-001'

// AGENT_TOKENS = "alice:tokABC,bob:tokXYZ,..."
const AGENT_TOKENS = (__ENV.AGENT_TOKENS || '').split(',').map((s) => {
  const [agent, token] = s.split(':')
  return { agent, token }
}).filter(x => x.agent && x.token)

const fanOutMsgs = new Counter('mesh_fanout_msg_total')
const fanOutLatency = new Trend('mesh_fanout_msg_duration_ms')

export const options = {
  vus: 10,
  duration: '2m',
  thresholds: {
    http_req_duration: ['p(95)<800'],
    http_req_failed:   ['rate<0.02'],
  },
}

export default function () {
  if (AGENT_TOKENS.length === 0) {
    throw new Error('AGENT_TOKENS env required')
  }

  const idx = Math.floor(Math.random() * AGENT_TOKENS.length)
  const sender = AGENT_TOKENS[idx]
  const otherIdx = (idx + 1) % AGENT_TOKENS.length
  const target = AGENT_TOKENS[otherIdx]

  const taskID = `t-fanout-${sender.agent}-${__VU}-${__ITER}-${Date.now()}`
  const msgID = `msg-${sender.agent}-${__VU}-${__ITER}-${Date.now()}`

  const payload = JSON.stringify({
    task_id: taskID,
    context_id: CONTEXT_ID,
    to_agent_id: target.agent,
    message: {
      message_id: msgID,
      parts: [{ kind: 'text', text: `${sender.agent}->${target.agent} #${__ITER}` }],
      preview: `${sender.agent}: collaboration step ${__ITER}`,
    },
  })

  const start = Date.now()
  const res = http.post(`${BASE_URL}/v1/mesh/tasks`, payload, {
    headers: {
      'Authorization': `Bearer ${sender.token}`,
      'Content-Type': 'application/json',
    },
    tags: { endpoint: 'mesh_submit_task' },
  })
  const elapsed = Date.now() - start

  check(res, {
    'status is 201': (r) => r.status === 201,
  })
  fanOutMsgs.add(1)
  fanOutLatency.add(elapsed)
}
