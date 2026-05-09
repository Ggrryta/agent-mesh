#!/bin/bash
# 双项目集成测试 - 验证 action_builder 和 agent-gateway 的完整对接

BASE_URL="http://localhost:8080"
PASS=0
FAIL=0
TOTAL=0

run_test() {
    local name="$1"
    local expected="$2"
    local actual="$3"
    TOTAL=$((TOTAL + 1))
    
    if [ "$expected" == "$actual" ]; then
        echo -e "✅ \033[32mPASS\033[0m: $name"
        PASS=$((PASS + 1))
    else
        echo -e "❌ \033[31mFAIL\033[0m: $name (期望: $expected, 实际: $actual)"
        FAIL=$((FAIL + 1))
    fi
}

echo "========================================="
echo "  🧪 双项目集成测试"
echo "  action_builder ↔ agent-gateway"
echo "========================================="
echo ""

# 前置检查
echo "🔍 前置检查..."
if ! curl -s "$BASE_URL/ping" > /dev/null 2>&1; then
    echo "❌ agent-gateway 未启动，请先运行: go run cmd/main.go"
    exit 1
fi
echo -e "✅ \033[32magent-gateway 运行中\033[0m"
echo ""

# 1. 测试 Skill 注册（手动）
echo "📝 1. 测试 Skill 注册..."
REGISTER_RESULT=$(curl -s -X POST "$BASE_URL/skills" \
  -H "Content-Type: application/json" \
  -d '{
    "skill_id": "test.integration.manual",
    "name": "集成测试-手动注册",
    "description": "通过 HTTP API 手动注册",
    "execution_type": 1,
    "endpoint": "https://httpbin.org/post",
    "http_method": "POST",
    "call_type": 1,
    "input_schema": {
      "type": "object",
      "properties": {
        "message": {"type": "string", "description": "测试消息"}
      }
    },
    "execution_policy": {
      "timeout_ms": 10000,
      "max_concurrency": 10
    },
    "owner_app_id": "test.integration"
  }')

REGISTER_CODE=$(echo "$REGISTER_RESULT" | jq -r '.code')
run_test "Skill 注册" "0" "$REGISTER_CODE"
echo ""

# 2. 查询 Skill
echo "🔍 2. 测试 Skill 查询..."
SKILL_DETAIL=$(curl -s "$BASE_URL/skills/test.integration.manual")
SKILL_NAME=$(echo "$SKILL_DETAIL" | jq -r '.data.name')
run_test "Skill 查询" "集成测试-手动注册" "$SKILL_NAME"
echo ""

# 3. 批量注册（模拟 action_builder 推送）
echo "📤 3. 测试批量注册（模拟 action_builder）..."
BATCH_RESULT=$(curl -s -X POST "$BASE_URL/internal/skills/register-batch" \
  -H "Content-Type: application/json" \
  -d '[
    {
      "skill_id": "test.integration.batch1",
      "name": "批量测试 Action 1",
      "description": "通过 internal 接口批量注册",
      "execution_type": 1,
      "endpoint": "https://httpbin.org/post",
      "http_method": "POST",
      "call_type": 1,
      "input_schema": {"type": "object", "properties": {}},
      "output_schema": {"type": "object"},
      "execution_policy": {"timeout_ms": 30000},
      "owner_app_id": "test.integration"
    },
    {
      "skill_id": "test.integration.batch2",
      "name": "批量测试 Action 2",
      "description": "批量注册第二条",
      "execution_type": 1,
      "endpoint": "https://httpbin.org/post",
      "http_method": "POST",
      "call_type": 1,
      "input_schema": {"type": "object", "properties": {}},
      "output_schema": {"type": "object"},
      "execution_policy": {"timeout_ms": 30000},
      "owner_app_id": "test.integration"
    }
  ]')

BATCH_SUCCESS=$(echo "$BATCH_RESULT" | jq -r '.data.success')
run_test "批量注册" "2" "$BATCH_SUCCESS"
echo ""

# 4. 创建 Consumer 并授权
echo "🔑 4. 创建 Consumer 并授权..."
CONSUMER_RESULT=$(curl -s -X POST "$BASE_URL/consumers" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "集成测试 Consumer",
    "capability_ids": [
      "test.integration.manual",
      "test.integration.batch1",
      "test.integration.batch2"
    ]
  }')

APP_ID=$(echo "$CONSUMER_RESULT" | jq -r '.data.app_id')
SECRET=$(echo "$CONSUMER_RESULT" | jq -r '.data.secret')

if [ "$APP_ID" != "null" ] && [ -n "$APP_ID" ]; then
    run_test "Consumer 创建" "success" "success"
else
    run_test "Consumer 创建" "success" "failed"
    echo "❌ 无法继续测试，退出"
    exit 1
fi
echo ""

# 5. 获取 Token
echo "🎫 5. 获取 Token..."
TOKEN_RESULT=$(curl -s -X POST "$BASE_URL/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\", \"secret\": \"$SECRET\"}")

TOKEN=$(echo "$TOKEN_RESULT" | jq -r '.data.token')

if [ "$TOKEN" != "null" ] && [ -n "$TOKEN" ] && [ "$TOKEN" != "" ]; then
    run_test "Token 获取" "success" "success"
else
    run_test "Token 获取" "success" "failed"
    echo "❌ 无法继续测试，退出"
    exit 1
fi
echo ""

# 6. 同步调用 Skill
echo "🚀 6. 测试同步调用..."
INVOKE_RESULT=$(curl -s -X POST "$BASE_URL/gateway/invoke/skill/test.integration.manual" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message": "sync test"}')

INVOKE_CODE=$(echo "$INVOKE_RESULT" | jq -r '.code')
run_test "同步调用" "0" "$INVOKE_CODE"
echo ""

# 7. 异步调用
echo "⏳ 7. 测试异步调用..."
ASYNC_RESULT=$(curl -s -X POST "$BASE_URL/gateway/invoke/skill/test.integration.manual?async=true" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message": "async test"}')

TASK_ID=$(echo "$ASYNC_RESULT" | jq -r '.data.task_id')

if [ "$TASK_ID" != "null" ] && [ -n "$TASK_ID" ]; then
    run_test "异步提交" "success" "success"
    
    # 轮询结果
    sleep 2
    TASK_RESULT=$(curl -s "$BASE_URL/gateway/task/$TASK_ID" \
      -H "Authorization: Bearer $TOKEN")
    
    TASK_STATUS=$(echo "$TASK_RESULT" | jq -r '.data.status')
    run_test "异步结果" "success" "$TASK_STATUS"
else
    run_test "异步提交" "success" "failed"
fi
echo ""

# 8. 查询调用统计
echo "📊 8. 测试调用统计..."
STATS_RESULT=$(curl -s "$BASE_URL/skills/test.integration.manual/stats")
TOTAL_CALLS=$(echo "$STATS_RESULT" | jq -r '.data.total_calls')

if [ "$TOTAL_CALLS" -gt 0 ] 2>/dev/null; then
    run_test "调用统计" ">0" "$TOTAL_CALLS"
else
    run_test "调用统计" ">0" "0"
fi
echo ""

# 9. 查询熔断器状态
echo "🔌 9. 测试熔断器状态..."
CIRCUIT_RESULT=$(curl -s "$BASE_URL/skills/test.integration.manual/circuit")
CIRCUIT_STATE=$(echo "$CIRCUIT_RESULT" | jq -r '.data.state')

if [ "$CIRCUIT_STATE" = "closed" ] || [ "$CIRCUIT_STATE" = "no_breaker" ]; then
    run_test "熔断器状态" "valid" "$CIRCUIT_STATE"
else
    run_test "熔断器状态" "valid" "$CIRCUIT_STATE"
fi
echo ""

# 10. 限流测试
echo "🚦 10. 测试限流（50 并发）..."
TMPDIR_RL=$(mktemp -d)
for i in $(seq 1 50); do
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "$BASE_URL/gateway/invoke/skill/test.integration.manual" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"message\": \"ratelimit $i\"}" > "$TMPDIR_RL/$i" &
done
wait

LIMITED=$(grep -l "429" "$TMPDIR_RL"/* 2>/dev/null | wc -l | tr -d ' ')
rm -rf "$TMPDIR_RL"

# 限流可能触发也可能不触发（取决于 QPS 配置）
echo -e "ℹ️  \033[33mINFO\033[0m: 限流触发 $LIMITED 次（取决于 QPS 配置）"
echo ""

# 11. 批量注销
echo "🗑️  11. 测试批量注销..."
UNREGISTER_RESULT=$(curl -s -X DELETE "$BASE_URL/internal/skills/unregister" \
  -H "Content-Type: application/json" \
  -d '{
    "skill_ids": [
      "test.integration.manual",
      "test.integration.batch1",
      "test.integration.batch2"
    ]
  }')

UNREGISTER_CODE=$(echo "$UNREGISTER_RESULT" | jq -r '.code')
run_test "批量注销" "0" "$UNREGISTER_CODE"
echo ""

# 12. 验证注销
echo "🔍 12. 验证注销结果..."
DELETED_SKILL=$(curl -s "$BASE_URL/skills/test.integration.manual")
DELETED_STATUS=$(echo "$DELETED_SKILL" | jq -r '.data.status')

if [ "$DELETED_STATUS" = "disabled" ]; then
    run_test "注销验证" "disabled" "$DELETED_STATUS"
else
    # 可能是软删除
    run_test "注销验证" "disabled/removed" "$DELETED_STATUS"
fi
echo ""

# 清理
echo "🧹 清理测试数据..."
curl -s -X DELETE "$BASE_URL/internal/skills/unregister" \
  -H "Content-Type: application/json" \
  -d '{"skill_ids": ["test.integration.manual", "test.integration.batch1", "test.integration.batch2"]}' > /dev/null 2>&1
echo ""

# 输出结果
echo "========================================="
echo "  📊 测试结果"
echo "========================================="
echo "总计: $TOTAL"
echo -e "✅ 通过: \033[32m$PASS\033[0m"
echo -e "❌ 失败: \033[31m$FAIL\033[0m"

if [ $TOTAL -gt 0 ]; then
    PASS_RATE=$((PASS * 100 / TOTAL))
    echo "通过率: ${PASS_RATE}%"
else
    echo "通过率: 0%"
fi
echo ""

if [ $FAIL -eq 0 ]; then
    echo -e "\033[32m🎉 所有测试通过！双项目集成正常！\033[0m"
    exit 0
else
    echo -e "\033[31m❌ 有 $FAIL 个测试失败，请检查上方日志\033[0m"
    exit 1
fi
