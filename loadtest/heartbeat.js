// loadtest/heartbeat.js
//
// k6 脚本：Heartbeat QPS 基线。
//
// 场景设计：1 个用户，1 把 API Key，10 个 agent，每个 agent 一把 JWT。
// 所有 VU 轮询往 /v1/mesh/agents/{id}/heartbeat 打请求；模拟大规模 agent
// 集群的心跳聚集到 Gateway 的情况。
//
// 这条路径是**纯 JWT 校验 + 一次 DB UPDATE**，是 Gateway 核心高频写路径。
//
// 用法：
//   BASE_URL=http://localhost:38080 k6 run loadtest/heartbeat.js
//   VUS=50 DURATION=1m k6 run loadtest/heartbeat.js
//
// setup 阶段做一次性数据准备；iteration 阶段只做 POST heartbeat。

import http from 'k6/http';
import { check, sleep, fail } from 'k6';

export const options = {
  vus: parseInt(__ENV.VUS || '10'),
  duration: __ENV.DURATION || '30s',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<100', 'p(99)<300'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:38080';
const AGENT_COUNT = 10;

export function setup() {
  const runID = Date.now().toString(36);
  const username = `k6_hb_${runID}`;
  const password = 'loadtest_pw_12345';

  // 1. 注册 + 拿 user JWT
  let resp = http.post(`${BASE}/v1/admin/auth/register`,
    JSON.stringify({ username, password }),
    { headers: { 'Content-Type': 'application/json' } });
  if (resp.status !== 201) fail(`register failed: ${resp.status} ${resp.body}`);
  const userJWT = resp.json('token');

  // 2. 签 API Key
  resp = http.post(`${BASE}/v1/admin/users/me/api-keys`,
    JSON.stringify({ label: 'k6' }),
    {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${userJWT}`,
      },
    });
  if (resp.status !== 201) fail(`api-key failed: ${resp.status} ${resp.body}`);
  const rawKey = resp.json('raw_key');

  // 3. 批量建 agent + 换 agent JWT
  const agents = [];
  for (let i = 0; i < AGENT_COUNT; i++) {
    const agentID = `${username}-agent-${i}`;
    resp = http.post(`${BASE}/v1/admin/users/me/agents`,
      JSON.stringify({ agent_id: agentID, name: agentID, url: `http://${agentID}:0` }),
      {
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${userJWT}`,
        },
      });
    if (resp.status !== 201) fail(`create agent ${agentID}: ${resp.status} ${resp.body}`);

    // 换 agent JWT
    resp = http.post(`${BASE}/v1/mesh/auth/token`,
      JSON.stringify({ agent_id: agentID }),
      { headers: { 'Content-Type': 'application/json', 'X-Api-Key': rawKey } });
    if (resp.status !== 200) fail(`token ${agentID}: ${resp.status} ${resp.body}`);
    agents.push({ id: agentID, jwt: resp.json('token') });
  }

  return { agents };
}

export default function (data) {
  // VU 轮流挑一个 agent 打心跳，模拟分布式 agent 集群
  const pick = data.agents[__ITER % data.agents.length];
  const resp = http.post(`${BASE}/v1/mesh/agents/${pick.id}/heartbeat`, null, {
    headers: { Authorization: `Bearer ${pick.jwt}` },
  });
  check(resp, {
    'heartbeat 200': (r) => r.status === 200,
    'status active': (r) => r.json('status') === 'active',
  });
  sleep(0.1);
}
