#!/usr/bin/env bash
# 压测准备脚本：注册测试用户 + 创建 agent + 加好友 + 输出 JWT
#
# 用法：
#   bash test/load/setup.sh          # 创建测试数据，输出 USER_TOKEN
#   USER_TOKEN=$(bash test/load/setup.sh)
#   k6 run -e USER_TOKEN=$USER_TOKEN test/load/p2p_submit.js

set -euo pipefail

BASE_URL=${BASE_URL:-http://localhost:8080}
USERNAME=${USERNAME:-loadtest}
PASSWORD=${PASSWORD:-loadtest_pw_strong_enough}

curl_json() {
  curl -sf -H "Content-Type: application/json" "$@"
}

# 1. 注册（已存在则跳过）
register_resp=$(curl_json -X POST "$BASE_URL/v1/admin/auth/register" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" 2>/dev/null || true)

# 2. 登录拿 JWT
login_resp=$(curl_json -X POST "$BASE_URL/v1/admin/auth/login" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}")
TOKEN=$(echo "$login_resp" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
UID_VAL=$(echo "$login_resp" | sed -n 's/.*"uid":\([0-9]*\).*/\1/p')

if [ -z "$TOKEN" ]; then
  echo "login failed: $login_resp" >&2
  exit 1
fi

# 3. 创建两个 agent
for agent_id in alice bob; do
  curl_json -X POST "$BASE_URL/v1/admin/users/me/agents" \
    -H "Authorization: Bearer $TOKEN" \
    -d "{\"agent_id\":\"$agent_id\",\"name\":\"$agent_id\"}" >/dev/null 2>&1 || true
done

# 4. 加好友（alice → bob）
curl_json -X POST "$BASE_URL/v1/admin/friends" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"from_agent_id":"alice","to_agent_id":"bob","reason":"loadtest"}' >/dev/null 2>&1 || true

echo "$TOKEN"
