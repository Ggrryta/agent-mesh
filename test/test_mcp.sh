#!/bin/bash
# MCP 协议测试 - 验证 tools/list 和 tools/call

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
echo "  🤖 MCP 协议测试"
echo "  Model Context Protocol"
echo "========================================="
echo ""

# 前置检查
echo "🔍 前置检查..."
if ! curl -s "$BASE_URL/ping" > /dev/null 2>&1; then
    echo "❌ agent-gateway 未启动，请先运行: go run cmd/main.go"
    exit 1
fi
echo -e "✅ \033[32magent-gateway 运行中\033[0m"

# 准备测试数据：注册 Skill 和获取 Token
echo ""
echo "📦 准备测试数据..."

# 注册测试 Skill
curl -s -X POST "$BASE_URL/internal/skills/register-batch" \
  -H "Content-Type: application/json" \
  -d '[
    {
      "skill_id": "test.mcp.tool",
      "name": "MCP 测试工具",
      "description": "用于测试 MCP 协议的 Skill",
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
      "output_schema": {"type": "object"},
      "execution_policy": {"timeout_ms": 30000},
      "owner_app_id": "test.mcp"
    }
  ]' > /dev/null

# 创建 Consumer
CONSUMER=$(curl -s -X POST "$BASE_URL/consumers" \
  -H "Content-Type: application/json" \
  -d '{"name": "MCP 测试", "capability_ids": ["test.mcp.tool"]}')

APP_ID=$(echo "$CONSUMER" | jq -r '.data.app_id')
SECRET=$(echo "$CONSUMER" | jq -r '.data.secret')

# 获取 Token
TOKEN=$(curl -s -X POST "$BASE_URL/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\", \"secret\": \"$SECRET\"}" | jq -r '.data.token')

if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
    echo "❌ Token 获取失败，无法继续测试"
    exit 1
fi

echo -e "✅ \033[32m测试数据准备完成\033[0m"
echo ""

# 1. 测试 SSE 连接
echo "📡 1. 测试 SSE 连接..."
SSE_RESPONSE=$(curl -s --max-time 3 -N "$BASE_URL/mcp/sse" \
  -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)

if echo "$SSE_RESPONSE" | grep -q "event: endpoint"; then
    ENDPOINT=$(echo "$SSE_RESPONSE" | grep "data: /mcp/message" | head -1)
    run_test "SSE 连接" "success" "success"
    echo -e "   ℹ️  $ENDPOINT"
else
    run_test "SSE 连接" "success" "failed"
fi
echo ""

# 2. 测试 initialize
echo "🔧 2. 测试 initialize..."
INIT_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "protocolVersion": "2024-11-05",
      "capabilities": {},
      "clientInfo": {
        "name": "test-client",
        "version": "1.0.0"
      }
    }
  }')

INIT_CODE=$(echo "$INIT_RESULT" | jq -r '.error.code // 0')
if [ "$INIT_CODE" = "0" ] || [ "$INIT_CODE" = "null" ]; then
    SERVER_INFO=$(echo "$INIT_RESULT" | jq -r '.result.serverInfo.name // "unknown"')
    run_test "initialize" "success" "success"
    echo -e "   ℹ️  Server: $SERVER_INFO"
else
    INIT_ERROR=$(echo "$INIT_RESULT" | jq -r '.error.message')
    run_test "initialize" "success" "$INIT_ERROR"
fi
echo ""

# 3. 测试 tools/list
echo "📋 3. 测试 tools/list..."
TOOLS_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/list",
    "params": {}
  }')

TOOLS_ERROR=$(echo "$TOOLS_RESULT" | jq -r '.error.code // 0')
if [ "$TOOLS_ERROR" = "0" ] || [ "$TOOLS_ERROR" = "null" ]; then
    TOOL_COUNT=$(echo "$TOOLS_RESULT" | jq '.result.tools | length')
    run_test "tools/list" "success" "success"
    echo -e "   ℹ️  发现 $TOOL_COUNT 个工具"
    
    # 列出工具名称
    echo "$TOOLS_RESULT" | jq -r '.result.tools[] | "   - \(.name): \(.description)"' 2>/dev/null
else
    TOOLS_ERROR_MSG=$(echo "$TOOLS_RESULT" | jq -r '.error.message')
    run_test "tools/list" "success" "$TOOLS_ERROR_MSG"
fi
echo ""

# 4. 测试 tools/call（同步）
echo "🚀 4. 测试 tools/call（同步）..."
CALL_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "test.mcp.tool",
      "arguments": {
        "message": "MCP sync test"
      }
    }
  }')

CALL_ERROR=$(echo "$CALL_RESULT" | jq -r '.error.code // 0')
if [ "$CALL_ERROR" = "0" ] || [ "$CALL_ERROR" = "null" ]; then
    run_test "tools/call (同步)" "success" "success"
    echo -e "   ℹ️  调用成功"
else
    CALL_ERROR_MSG=$(echo "$CALL_RESULT" | jq -r '.error.message')
    run_test "tools/call (同步)" "success" "$CALL_ERROR_MSG"
fi
echo ""

# 5. 测试 tools/call（不存在的工具）
echo "❌ 5. 测试 tools/call（不存在的工具）..."
NOT_FOUND_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 4,
    "method": "tools/call",
    "params": {
      "name": "nonexistent.tool",
      "arguments": {}
    }
  }')

NOT_FOUND_CODE=$(echo "$NOT_FOUND_RESULT" | jq -r '.error.code // 0')
if [ "$NOT_FOUND_CODE" != "0" ] && [ "$NOT_FOUND_CODE" != "null" ]; then
    run_test "tools/call (不存在)" "error" "error"
    echo -e "   ℹ️  正确返回错误"
else
    run_test "tools/call (不存在)" "error" "success"
fi
echo ""

# 6. 测试 invalid method
echo "🚫 6. 测试无效方法..."
INVALID_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 5,
    "method": "invalid/method",
    "params": {}
  }')

INVALID_CODE=$(echo "$INVALID_RESULT" | jq -r '.error.code // 0')
if [ "$INVALID_CODE" = "-32601" ]; then
    run_test "无效方法" "-32601" "$INVALID_CODE"
else
    run_test "无效方法" "-32601" "$INVALID_CODE"
fi
echo ""

# 7. 测试无效 JSON
echo "🔤 7. 测试无效 JSON..."
INVALID_JSON_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: text/plain" \
  -d 'invalid json')

INVALID_JSON_CODE=$(echo "$INVALID_JSON_RESULT" | jq -r '.code // 0')
if [ "$INVALID_JSON_CODE" != "0" ]; then
    run_test "无效 JSON" "error" "error"
else
    run_test "无效 JSON" "error" "success"
fi
echo ""

# 8. 测试无 Token 访问
echo "🔒 8. 测试无 Token 访问..."
NO_TOKEN_RESULT=$(curl -s -X POST "$BASE_URL/mcp/message" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 6, "method": "tools/list"}')

NO_TOKEN_CODE=$(echo "$NO_TOKEN_RESULT" | jq -r '.code // 0')
if [ "$NO_TOKEN_CODE" != "0" ]; then
    run_test "无 Token 访问" "rejected" "rejected"
else
    run_test "无 Token 访问" "rejected" "allowed"
fi
echo ""

# 清理
echo "🧹 清理测试数据..."
curl -s -X DELETE "$BASE_URL/internal/skills/unregister" \
  -H "Content-Type: application/json" \
  -d '{"skill_ids": ["test.mcp.tool"]}' > /dev/null 2>&1
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
    echo -e "\033[32m🎉 所有 MCP 测试通过！\033[0m"
    exit 0
else
    echo -e "\033[31m❌ 有 $FAIL 个测试失败，请检查上方日志\033[0m"
    exit 1
fi
