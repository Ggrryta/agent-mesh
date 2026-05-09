#!/bin/bash
# E2E 完整工作流测试

BASE_URL="http://localhost:8080"
PASS=0
FAIL=0
TOTAL=0

# 颜色输出
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

run_test() {
    local name="$1"
    local expected="$2"
    local actual="$3"
    TOTAL=$((TOTAL + 1))
    
    if [ "$expected" == "$actual" ]; then
        echo -e "✅ ${GREEN}PASS${NC}: $name"
        PASS=$((PASS + 1))
    else
        echo -e "❌ ${RED}FAIL${NC}: $name (期望: $expected, 实际: $actual)"
        FAIL=$((FAIL + 1))
    fi
}

echo "========================================="
echo "  Agent Gateway E2E 完整工作流测试"
echo "========================================="
echo ""

# 1. 健康检查
echo "📦 1. 健康检查"
HEALTH=$(curl -s http://localhost:8080/ping | jq -r '.message')
run_test "Ping" "pong" "$HEALTH"
echo ""

# 2. Provider 注册能力
echo "🔧 2. Provider 注册能力"
CAP_ID="test.e2e.$(date +%s)"
CREATE_RESP=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{
    \"capability_id\": \"$CAP_ID\",
    \"name\": \"E2E 测试能力\",
    \"endpoint\": \"https://httpbin.org/post\",
    \"protocol\": 2,
    \"call_type\": 1,
    \"input_schema\": {
      \"type\": \"object\",
      \"required\": [\"message\"],
      \"properties\": {
        \"message\": {\"type\": \"string\"}
      }
    }
  }")
CREATE_CODE=$(echo $CREATE_RESP | jq -r '.code')
run_test "注册能力" "0" "$CREATE_CODE"
echo ""

# 3. 创建 Consumer
echo "👤 3. 创建 Consumer"
APP_ID="test.e2e.consumer"
CONSUMER_RESP=$(curl -s -X POST "$BASE_URL/consumers" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\", \"description\": \"E2E 测试\"}")
CONSUMER_CODE=$(echo $CONSUMER_RESP | jq -r '.code')
SECRET=$(echo $CONSUMER_RESP | jq -r '.data.secret')
run_test "创建 Consumer" "0" "$CONSUMER_CODE"
echo ""

# 4. 授权能力
echo "🔐 4. 授权能力"
AUTH_RESP=$(curl -s -X PUT "$BASE_URL/consumers/$APP_ID/capabilities" \
  -H "Content-Type: application/json" \
  -d "{\"capability_ids\": [\"$CAP_ID\"]}")
AUTH_CODE=$(echo $AUTH_RESP | jq -r '.code')
run_test "授权能力" "0" "$AUTH_CODE"
echo ""

# 5. 获取 Token
echo "🎫 5. 获取 Token"
TOKEN_RESP=$(curl -s -X POST "$BASE_URL/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\", \"secret\": \"$SECRET\"}")
TOKEN_CODE=$(echo $TOKEN_RESP | jq -r '.code')
TOKEN=$(echo $TOKEN_RESP | jq -r '.data.token')
run_test "获取 Token" "0" "$TOKEN_CODE"
echo ""

# 6. 同步调用
echo "🔄 6. 同步调用"
SYNC_RESP=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"input\": {\"message\": \"hello\"}}")
SYNC_CODE=$(echo $SYNC_RESP | jq -r '.code')
TRACE_ID=$(echo $SYNC_RESP | jq -r '.data.trace_id')
run_test "同步调用" "0" "$SYNC_CODE"
run_test "返回 trace_id" "true" "$([ -n "$TRACE_ID" ] && echo true || echo false)"
echo ""

# 7. 异步调用
echo "⚡ 7. 异步调用"
ASYNC_RESP=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_ID?async=true" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"input\": {\"message\": \"async test\"}}")
ASYNC_CODE=$(echo $ASYNC_RESP | jq -r '.code')
TASK_ID=$(echo $ASYNC_RESP | jq -r '.data.task_id')
run_test "异步提交" "0" "$ASYNC_CODE"

sleep 2
TASK_RESP=$(curl -s "$BASE_URL/gateway/task/$TASK_ID" \
  -H "Authorization: Bearer $TOKEN")
TASK_STATUS=$(echo $TASK_RESP | jq -r '.data.status')
run_test "任务状态" "completed" "$TASK_STATUS"
echo ""

# 8. 流式调用
echo "🌊 8. 流式调用"
STREAM_RESP=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_ID?stream=true" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"input\": {\"message\": \"stream test\"}}")
HAS_DATA=$(echo "$STREAM_RESP" | grep -c "data:" || true)
run_test "流式响应" "true" "$([ $HAS_DATA -gt 0 ] && echo true || echo false)"
echo ""

# 9. 无 Token 调用
echo "🚫 9. 安全测试 - 无 Token"
NO_TOKEN_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/$CAP_ID" \
  -H "Content-Type: application/json" \
  -d "{\"input\": {}}")
run_test "无 Token 返回 401" "401" "$NO_TOKEN_CODE"
echo ""

# 10. 限流测试
echo "🚦 10. 限流测试（150 并发，QPS=100）"
TMPDIR_RL=$(mktemp -d)
for i in $(seq 1 150); do
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "$BASE_URL/gateway/invoke/$CAP_ID" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"input\": {\"message\": \"ratelimit\"}}" > "$TMPDIR_RL/$i" &
done
wait
LIMITED=$(grep -l "429" "$TMPDIR_RL"/* 2>/dev/null | wc -l | tr -d ' ')
rm -rf "$TMPDIR_RL"
run_test "限流生效" "true" "$([ "$LIMITED" -gt 0 ] && echo true || echo false)"
echo ""

# 11. 软删除 + 重新注册
echo "🔄 11. 软删除 + 重新注册"
DEL_RESP=$(curl -s -X DELETE "$BASE_URL/capability/$CAP_ID")
DEL_CODE=$(echo $DEL_RESP | jq -r '.code')
run_test "软删除" "0" "$DEL_CODE"

RE_REG_RESP=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{
    \"capability_id\": \"$CAP_ID\",
    \"name\": \"重新注册\",
    \"endpoint\": \"https://httpbin.org/post\",
    \"protocol\": 2,
    \"call_type\": 1
  }")
RE_REG_CODE=$(echo $RE_REG_RESP | jq -r '.code')
run_test "重新注册" "0" "$RE_REG_CODE"
echo ""

# 总结
echo "========================================="
echo "  测试总结"
echo "========================================="
echo -e "总计: $TOTAL"
echo -e "通过: ${GREEN}$PASS${NC}"
echo -e "失败: ${RED}$FAIL${NC}"
echo ""

if [ $FAIL -eq 0 ]; then
  echo -e "🎉 ${GREEN}所有测试通过！${NC}"
  exit 0
else
  echo -e "❌ ${RED}有测试失败${NC}"
  exit 1
fi
