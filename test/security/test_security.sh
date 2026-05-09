#!/bin/bash
# 安全测试

BASE_URL="http://localhost:8080"
PASS=0
FAIL=0
TOTAL=0

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
echo "  Agent Gateway 安全测试"
echo "========================================="
echo ""

# 1. 无 Token 调用
echo "🔒 1. 无 Token 调用"
NO_TOKEN=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/test.service" \
  -H "Content-Type: application/json" \
  -d '{"input": {}}')
run_test "无 Token 返回 401" "401" "$NO_TOKEN"
echo ""

# 2. 错误 Token
echo "🔒 2. 错误 Token"
BAD_TOKEN=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/test.service" \
  -H "Authorization: Bearer invalid.token.here" \
  -H "Content-Type: application/json" \
  -d '{"input": {}}')
run_test "错误 Token 返回 401" "401" "$BAD_TOKEN"
echo ""

# 3. Token 格式错误
echo "🔒 3. Token 格式错误"
BAD_FORMAT=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/test.service" \
  -H "Authorization: InvalidFormat" \
  -H "Content-Type: application/json" \
  -d '{"input": {}}')
run_test "Token 格式错误返回 401" "401" "$BAD_FORMAT"
echo ""

# 4. 无权限调用
echo "🔒 4. 无权限调用"
APP_ID="test.security.consumer"
SECRET=$(curl -s -X POST "$BASE_URL/consumers" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\"}" | jq -r '.data.secret')

TOKEN=$(curl -s -X POST "$BASE_URL/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"app_id\": \"$APP_ID\", \"secret\": \"$SECRET\"}" | jq -r '.data.token')

NO_PERM=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/unauthorized.service" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input": {}}')
run_test "无权限返回 403" "403" "$NO_PERM"
echo ""

# 5. 过期 Token
echo "🔒 5. 过期 Token（模拟）"
EXPIRED_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJhcHBfaWQiOiJ0ZXN0IiwiZXhwIjoxNjAwMDAwMDAwfQ.expired"
EXPIRED=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/gateway/invoke/test.service" \
  -H "Authorization: Bearer $EXPIRED_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input": {}}')
run_test "过期 Token 返回 401" "401" "$EXPIRED"
echo ""

# 6. SQL 注入测试
echo "🔒 6. SQL 注入测试"
SQL_INJECT=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id": "test\"; DROP TABLE capabilities; --", "name": "SQL注入", "endpoint": "http://test.com", "protocol": 2, "call_type": 1}')
run_test "SQL 注入被拦截（400）" "400" "$SQL_INJECT"
echo ""

# 7. XSS 测试
echo "🔒 7. XSS 测试"
XSS_CODE=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id": "test.xss.service", "name": "<script>alert(1)</script>", "endpoint": "http://test.com", "protocol": 2, "call_type": 1}' | jq -r '.code')
run_test "XSS 存储（应正常返回 0）" "0" "$XSS_CODE"

# 清理
curl -s -X DELETE "$BASE_URL/capability/test.xss.service" > /dev/null 2>&1
echo ""

# 8. 重复注册测试
echo "🔒 8. 重复注册测试"
curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id": "test.duplicate.service", "name": "测试", "endpoint": "http://test.com", "protocol": 2, "call_type": 1}' > /dev/null

DUP_CODE=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id": "test.duplicate.service", "name": "重复", "endpoint": "http://test.com", "protocol": 2, "call_type": 1}' | jq -r '.code')
run_test "重复注册返回 409" "409" "$DUP_CODE"

# 清理
curl -s -X DELETE "$BASE_URL/capability/test.duplicate.service" > /dev/null 2>&1
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
  echo -e "🎉 ${GREEN}所有安全测试通过！${NC}"
  exit 0
else
  echo -e "❌ ${RED}有测试失败${NC}"
  exit 1
fi
