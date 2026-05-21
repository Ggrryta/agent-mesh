// loadtest/register_login.js
//
// k6 脚本：Week 2 压测基线。覆盖 Week 1 里程碑里高频的两条路径：
//   - POST /v1/admin/auth/register  （含 bcrypt + 事务内插 agent，重路径）
//   - POST /v1/admin/auth/login     （bcrypt compare）
//
// 目的：**建基线**而不是冲指标。Week 5 治理层上了限流 / 熔断 / 并发控制
// 之后，用这个脚本回归对比。
//
// 用法：
//   BASE_URL=http://localhost:38080 k6 run loadtest/register_login.js
//
// 参数（env）：
//   BASE_URL      默认 http://localhost:38080（配合 `make smoke-forward`）
//   VUS           并发虚拟用户数（默认 20）
//   DURATION      持续时间（默认 30s）
//
// 结果字段：
//   http_req_duration (p95/p99)
//   iterations / checks 通过率
//
// 不做：
//   - 登录后操作链路（Week 3 加 Task 端点后再加脚本）
//   - SSE / WebSocket（同上）

import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  vus: parseInt(__ENV.VUS || '20'),
  duration: __ENV.DURATION || '30s',
  thresholds: {
    // 基线阶段阈值偏宽；Week 7 收敛。
    http_req_failed: ['rate<0.05'],
    http_req_duration: ['p(95)<500', 'p(99)<1500'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:38080';

// 每个 VU 独立生成一个用户名，避免冲突；iteration 里重复登录刷 RPS。
export function setup() {
  return { runID: Date.now().toString(36) };
}

export default function (data) {
  const username = `k6_${data.runID}_${__VU}_${__ITER}`;
  const password = 'loadtest_pw_12345';

  // register
  const reg = http.post(`${BASE}/v1/admin/auth/register`,
    JSON.stringify({ username, password }),
    { headers: { 'Content-Type': 'application/json' } });
  check(reg, {
    'register 201': (r) => r.status === 201,
    'register returns token': (r) => r.json('token') !== '',
  });

  // login（立即再登）
  const login = http.post(`${BASE}/v1/admin/auth/login`,
    JSON.stringify({ username, password }),
    { headers: { 'Content-Type': 'application/json' } });
  check(login, {
    'login 200': (r) => r.status === 200,
    'login returns token': (r) => r.json('token') !== '',
  });

  // 短 think time 模拟真实用户节奏（非严格 RPS 测试）
  sleep(0.3);
}
