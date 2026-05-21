// k6 压测：WebSocket FeedHub 实时推送
// 验证 N 个并发 WebSocket 连接同时订阅时的 Gateway 行为。

import ws from 'k6/ws'
import { check } from 'k6'
import { Counter, Trend } from 'k6/metrics'

const BASE_URL = __ENV.BASE_URL || 'ws://localhost:8080'
const USER_TOKEN = __ENV.USER_TOKEN || ''

const wsConnects = new Counter('ws_connect_total')
const wsMessages = new Counter('ws_message_total')
const wsHandshakeLatency = new Trend('ws_handshake_ms')

export const options = {
  scenarios: {
    long_lived: {
      executor: 'ramping-vus',
      startVUs: 10,
      stages: [
        { duration: '30s', target: 50 },
        { duration: '1m', target: 200 },   // 200 并发 WebSocket
        { duration: '30s', target: 0 },
      ],
    },
  },
  thresholds: {
    ws_connecting: ['p(95)<1000'],
    checks: ['rate>0.95'],
  },
}

export default function () {
  const url = `${BASE_URL}/v1/admin/ws/feed`
  const params = {
    headers: {
      'Authorization': `Bearer ${USER_TOKEN}`,
    },
  }

  const start = Date.now()
  const res = ws.connect(url, params, (socket) => {
    wsConnects.add(1)
    wsHandshakeLatency.add(Date.now() - start)

    socket.on('open', () => {
      check(null, { 'ws connected': () => true })
    })

    socket.on('message', (data) => {
      wsMessages.add(1)
    })

    socket.setTimeout(() => {
      socket.close()
    }, 30000) // 保持 30s 后断开
  })

  check(res, {
    'handshake succeeded': (r) => r && r.status === 101,
  })
}
