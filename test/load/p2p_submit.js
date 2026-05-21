// k6 压测脚本：点对点 task 提交 QPS + 延迟基线
//
// 用法（前置：Gateway 已起，admin user 已注册并有 agent friend 对）：
//   k6 run -e BASE_URL=http://localhost:8080 \
//          -e USER_TOKEN=<jwt> \
//          -e FROM_AGENT=alice \
//          -e TO_AGENT=bob \
//          test/load/p2p_submit.js
//
// 输出关键指标：
//   - http_req_duration p50/p95/p99
//   - mesh_task_submit_total rate
//   - 失败率
import http from 'k6/http'
import { check, sleep } from 'k6'
import { Counter, Trend } from 'k6/metrics'

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080'
const USER_TOKEN = __ENV.USER_TOKEN || ''
const FROM_AGENT = __ENV.FROM_AGENT || 'alice'
const TO_AGENT = __ENV.TO_AGENT || 'bob'

const taskSubmits = new Counter('mesh_task_submit_total')
const taskLatency = new Trend('mesh_task_submit_duration_ms')

export const options = {
  scenarios: {
    submit_burst: {
      executor: 'ramping-arrival-rate',
      startRate: 10,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 200,
      stages: [
        { duration: '30s', target: 50 },   // 预热
        { duration: '1m', target: 200 },   // 主压测
        { duration: '30s', target: 500 },  // 冲击
        { duration: '30s', target: 0 },    // 降压
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<2000'], // P95<500ms, P99<2s
    http_req_failed:   ['rate<0.01'],                // 错误率 <1%
    mesh_task_submit_total: ['count>1000'],          // 总数 >1000
  },
}

export default function () {
  const taskID = `t-load-${__VU}-${__ITER}-${Date.now()}`
  const msgID = `msg-${__VU}-${__ITER}-${Date.now()}`

  const payload = JSON.stringify({
    to_agent_id: TO_AGENT,
    message: {
      message_id: msgID,
      parts: [{ kind: 'text', text: `load test message ${__VU}/${__ITER}` }],
    },
  })

  const start = Date.now()
  const res = http.post(`${BASE_URL}/v1/admin/tasks`, payload, {
    headers: {
      'Authorization': `Bearer ${USER_TOKEN}`,
      'Content-Type': 'application/json',
    },
    tags: { endpoint: 'admin_submit_task' },
  })
  const elapsed = Date.now() - start

  check(res, {
    'status is 201': (r) => r.status === 201,
    'has task_id': (r) => r.json('task_id') !== undefined,
  })
  taskSubmits.add(1)
  taskLatency.add(elapsed)
}
