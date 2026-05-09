#!/bin/bash

# agent-gateway 自动化测试脚本
# 用法: ./test.sh [BASE_URL]
# 依赖: curl, jq

BASE_URL="${1:-http://localhost:8080}"
PASS=0
FAIL=0

# capability_id 三段式，末段带随机数保证唯一
R=$((RANDOM % 90000 + 10000))
CAP_HTTP="test.http.t${R}"
CAP_HTTP_SCHEMA="test.schema.t${R}"
CAP_GRPC="test.grpc.t${R}"
CAP_NOSCHEMA="test.noschema.t${R}"
CAP_UNREACH="test.unreach.t${R}"
CAP_ERROR="test.error.t${R}"
CAP_DELETE="test.delete.t${R}"
CAP_BAD_SCHEMA="test.badschema.t${R}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

header() { echo -e "\n${YELLOW}=== $1 ===${NC}"; }

# run_test NAME EXPECTED_CODE ACTUAL_CODE [DETAIL]
run_test() {
    local name="$1" expected="$2" actual="$3" detail="${4:-}"
    if [ "$expected" = "$actual" ]; then
        echo -e "  ✅ ${GREEN}PASS${NC} $name"
        PASS=$((PASS + 1))
    else
        echo -e "  ❌ ${RED}FAIL${NC} $name  (expected=$expected actual=$actual)${detail:+  [$detail]}"
        FAIL=$((FAIL + 1))
    fi
}

# assert_field RESPONSE JQPATH EXPECTED NAME
assert_field() {
    local resp="$1" path="$2" expected="$3" name="$4"
    local actual
    actual=$(echo "$resp" | jq -r "$path // empty" 2>/dev/null)
    run_test "$name" "$expected" "$actual"
}

cleanup() {
    for id in "$CAP_HTTP" "$CAP_HTTP_SCHEMA" "$CAP_GRPC" "$CAP_NOSCHEMA" \
               "$CAP_UNREACH" "$CAP_ERROR" "$CAP_DELETE" "$CAP_BAD_SCHEMA"; do
        curl -s -X DELETE "$BASE_URL/capability/$id" > /dev/null 2>&1 || true
    done
}
trap cleanup EXIT

# ── 前置检查 ──────────────────────────────────────────────
header "前置检查"
if ! curl -s --max-time 3 "$BASE_URL/ping" > /dev/null 2>&1; then
    echo -e "${RED}服务未运行: $BASE_URL${NC}"
    exit 1
fi
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/ping")
run_test "GET /ping → 200" "200" "$CODE"

# ── 1. 能力注册 ───────────────────────────────────────────
header "1. 能力注册"

# 1.1 注册 HTTP 能力（无 schema）
R1=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_HTTP\",\"name\":\"HTTP测试\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1}")
run_test "注册 HTTP 能力" "0" "$(echo "$R1" | jq -r '.code')"
assert_field "$R1" ".data.capability_id" "$CAP_HTTP" "  返回 capability_id 正确"

# 1.2 注册 HTTP 能力（带 input_schema JSON 对象）
R2=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_HTTP_SCHEMA\",\"name\":\"HTTP-Schema测试\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1,\"input_schema\":{\"type\":\"object\",\"required\":[\"message\"],\"properties\":{\"message\":{\"type\":\"string\"}}}}")
run_test "注册 HTTP 能力（带 schema）" "0" "$(echo "$R2" | jq -r '.code')"

# 1.3 注册 gRPC 能力
R3=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_GRPC\",\"name\":\"gRPC测试\",\"endpoint\":\"localhost:9090\",\"protocol\":1,\"grpc_method\":\"/test.Echo/Hello\",\"call_type\":1}")
run_test "注册 gRPC 能力" "0" "$(echo "$R3" | jq -r '.code')"

# 1.4 重复注册同一 capability_id → 409
R4=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_HTTP\",\"name\":\"重复\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1}")
run_test "重复注册同一 ID → 409" "409" "$(echo "$R4" | jq -r '.code')"

# 1.5 capability_id 格式错误（大写）
R5=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"INVALID_ID","name":"x","endpoint":"http://x.com","protocol":2,"call_type":1}')
run_test "capability_id 格式错误 → 400" "400" "$(echo "$R5" | jq -r '.code')"

# 1.6 capability_id 四段式（不合法）
R6=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"a.b.c.d","name":"x","endpoint":"http://x.com","protocol":2,"call_type":1}')
run_test "capability_id 四段式 → 400" "400" "$(echo "$R6" | jq -r '.code')"

# 1.7 缺少 name
R7=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.miss.name","endpoint":"http://x.com","protocol":2,"call_type":1}')
run_test "缺少 name → 400" "400" "$(echo "$R7" | jq -r '.code')"

# 1.8 缺少 endpoint
R8=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.miss.endpoint","name":"x","protocol":2,"call_type":1}')
run_test "缺少 endpoint → 400" "400" "$(echo "$R8" | jq -r '.code')"

# 1.9 protocol 非法值
R9=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.bad.protocol","name":"x","endpoint":"http://x.com","protocol":99,"call_type":1}')
run_test "protocol 非法 → 400" "400" "$(echo "$R9" | jq -r '.code')"

# 1.10 call_type 非法值
R10=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.bad.calltype","name":"x","endpoint":"http://x.com","protocol":2,"call_type":99}')
run_test "call_type 非法 → 400" "400" "$(echo "$R10" | jq -r '.code')"

# 1.11 gRPC 缺少 grpc_method
R11=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.grpc.nomethod","name":"x","endpoint":"localhost:9090","protocol":1,"call_type":1}')
run_test "gRPC 缺少 grpc_method → 400" "400" "$(echo "$R11" | jq -r '.code')"

# 1.12 capability_id 两段式（不合法）
R12=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.only","name":"x","endpoint":"http://x.com","protocol":2,"call_type":1}')
run_test "capability_id 两段式 → 400" "400" "$(echo "$R12" | jq -r '.code')"

# 1.13 capability_id 含特殊字符（不合法）
R13=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d '{"capability_id":"test.bad@char.action","name":"x","endpoint":"http://x.com","protocol":2,"call_type":1}')
run_test "capability_id 含特殊字符 → 400" "400" "$(echo "$R13" | jq -r '.code')"

# 1.14 input_schema 传非法 JSON（字符串而非对象）
R14=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_BAD_SCHEMA\",\"name\":\"x\",\"endpoint\":\"http://x.com\",\"protocol\":2,\"call_type\":1,\"input_schema\":\"not-a-json-object\"}")
run_test "input_schema 非 JSON 对象 → 400" "400" "$(echo "$R14" | jq -r '.code')"

# ── 2. 能力发现 ───────────────────────────────────────────
header "2. 能力发现"

# 2.1 查询所有
R=$(curl -s "$BASE_URL/capability/list")
run_test "GET /capability/list → 0" "0" "$(echo "$R" | jq -r '.code')"
run_test "  返回 total 字段" "1" "$(echo "$R" | jq 'has("data") and (.data | has("total"))' | grep -c true || echo 0)"

# 2.2 按 domain 过滤，验证结果中所有 capability_id 都以 test. 开头
R=$(curl -s "$BASE_URL/capability/list?domain=test")
run_test "按 domain=test 过滤" "0" "$(echo "$R" | jq -r '.code')"
MISMATCH=$(echo "$R" | jq '[.data.items[]?.capability_id | select(startswith("test.") | not)] | length')
run_test "  过滤结果均属于 test domain" "0" "${MISMATCH:-0}"

# 2.3 按 call_type 过滤
R=$(curl -s "$BASE_URL/capability/list?call_type=1")
run_test "按 call_type=1 过滤" "0" "$(echo "$R" | jq -r '.code')"

# 2.4 关键词搜索
R=$(curl -s "$BASE_URL/capability/list?keyword=HTTP测试")
run_test "关键词搜索" "0" "$(echo "$R" | jq -r '.code')"
COUNT=$(echo "$R" | jq -r '.data.total')
run_test "  关键词搜索结果 ≥1" "1" "$([ "${COUNT:-0}" -ge 1 ] && echo 1 || echo 0)"

# 2.5 查询单个（存在）
R=$(curl -s "$BASE_URL/capability/$CAP_HTTP")
run_test "GET /capability/:id（存在）→ 0" "0" "$(echo "$R" | jq -r '.code')"
assert_field "$R" ".data.capability_id" "$CAP_HTTP" "  返回 capability_id 正确"

# 2.6 查询单个（不存在）→ 404
R=$(curl -s "$BASE_URL/capability/nonexistent.service.action")
run_test "GET /capability/:id（不存在）→ 404" "404" "$(echo "$R" | jq -r '.code')"

# ── 3. 软删除 & 重新注册 ──────────────────────────────────
header "3. 软删除 & 重新注册"

# 3.1 注册待删除能力
curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_DELETE\",\"name\":\"待删除\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1}" > /dev/null

# 3.2 删除
R=$(curl -s -X DELETE "$BASE_URL/capability/$CAP_DELETE")
run_test "DELETE /capability/:id → 0" "0" "$(echo "$R" | jq -r '.code')"

# 3.3 删除后查询 → 404
R=$(curl -s "$BASE_URL/capability/$CAP_DELETE")
run_test "删除后查询 → 404" "404" "$(echo "$R" | jq -r '.code')"

# 3.4 删除后重新注册同一 ID → 成功（软删除不阻塞）
R=$(curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_DELETE\",\"name\":\"重新注册\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1}")
run_test "软删除后重新注册同一 ID → 0" "0" "$(echo "$R" | jq -r '.code')"

# 3.5 删除不存在的能力 → 404
R=$(curl -s -X DELETE "$BASE_URL/capability/nonexistent.del.action")
run_test "删除不存在的能力 → 404" "404" "$(echo "$R" | jq -r '.code')"

# ── 4. 同步调用 ───────────────────────────────────────────
header "4. 同步调用"

# 4.1 HTTP 调用成功，验证 trace_id 存在且为 UUID 格式，output 字段存在
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP" \
  -H "Content-Type: application/json" \
  -d '{"input":{"key":"value"}}')
run_test "HTTP 调用成功 → 0" "0" "$(echo "$R" | jq -r '.code')"
TRACE=$(echo "$R" | jq -r '.data.trace_id // empty')
run_test "  响应包含 trace_id" "1" "$([ -n "$TRACE" ] && echo 1 || echo 0)"
UUID_OK=$(echo "$TRACE" | grep -cE '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' || echo 0)
run_test "  trace_id 为 UUID 格式" "1" "$UUID_OK"
run_test "  响应包含 output 字段" "1" "$(echo "$R" | jq 'has("data") and (.data | has("output"))' | grep -c true || echo 0)"

# 4.2 调用不存在的 capability → 404
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/nonexistent.service.action" \
  -H "Content-Type: application/json" \
  -d '{"input":{}}')
run_test "调用不存在 capability → 404" "404" "$(echo "$R" | jq -r '.code')"

# 4.3 请求体缺少 input 字段（input 为 null）→ 正常处理（无 schema 时通过）
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP" \
  -H "Content-Type: application/json" \
  -d '{}')
run_test "input 为空（无 schema）→ 0" "0" "$(echo "$R" | jq -r '.code')"

# 4.4 请求体非 JSON → 400
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP" \
  -H "Content-Type: application/json" \
  -d 'not-json')
run_test "请求体非 JSON → 400" "400" "$(echo "$R" | jq -r '.code')"

# ── 5. Schema 校验 ────────────────────────────────────────
header "5. Schema 校验"

# 5.1 满足 schema → 成功
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP_SCHEMA" \
  -H "Content-Type: application/json" \
  -d '{"input":{"message":"hello"}}')
run_test "满足 schema → 0" "0" "$(echo "$R" | jq -r '.code')"

# 5.2 缺少 required 字段 → 400
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP_SCHEMA" \
  -H "Content-Type: application/json" \
  -d '{"input":{"other":"value"}}')
run_test "缺少 required 字段 → 400" "400" "$(echo "$R" | jq -r '.code')"
MSG=$(echo "$R" | jq -r '.msg')
run_test "  错误信息含 validation" "1" "$(echo "$MSG" | grep -ci 'validation\|required\|校验' || echo 0 | head -1)"

# 5.3 字段类型错误（message 应为 string，传 int）→ 400
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_HTTP_SCHEMA" \
  -H "Content-Type: application/json" \
  -d '{"input":{"message":123}}')
run_test "字段类型错误 → 400" "400" "$(echo "$R" | jq -r '.code')"

# 5.4 无 schema 能力，任意 input 均通过
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_NOSCHEMA" \
  -H "Content-Type: application/json" \
  -d '{"input":{"anything":true,"nested":{"x":1}}}')
# CAP_NOSCHEMA 还未注册，先注册
curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_NOSCHEMA\",\"name\":\"无Schema\",\"endpoint\":\"https://httpbin.org/post\",\"protocol\":2,\"call_type\":1}" > /dev/null
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_NOSCHEMA" \
  -H "Content-Type: application/json" \
  -d '{"input":{"anything":true,"nested":{"x":1}}}')
run_test "无 schema 任意 input → 0" "0" "$(echo "$R" | jq -r '.code')"

# ── 6. 错误处理 ───────────────────────────────────────────
header "6. 错误处理"

# 6.1 Provider 不可达 → 502
curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_UNREACH\",\"name\":\"不可达\",\"endpoint\":\"http://127.0.0.1:19999\",\"protocol\":2,\"call_type\":1}" > /dev/null
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_UNREACH" \
  -H "Content-Type: application/json" \
  -d '{"input":{}}')
run_test "Provider 不可达 → 502" "502" "$(echo "$R" | jq -r '.code')"

# 6.2 Provider 返回 5xx → 502
curl -s -X POST "$BASE_URL/capability/create" \
  -H "Content-Type: application/json" \
  -d "{\"capability_id\":\"$CAP_ERROR\",\"name\":\"错误服务\",\"endpoint\":\"https://httpbin.org/status/500\",\"protocol\":2,\"call_type\":1}" > /dev/null
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_ERROR" \
  -H "Content-Type: application/json" \
  -d '{"input":{}}')
run_test "Provider 返回 500 → 502" "502" "$(echo "$R" | jq -r '.code')"

# 6.3 gRPC 能力调用（reflection 失败）→ 502
R=$(curl -s -X POST "$BASE_URL/gateway/invoke/$CAP_GRPC" \
  -H "Content-Type: application/json" \
  -d '{"input":{"msg":"test"}}')
run_test "gRPC 调用（无 server）→ 502" "502" "$(echo "$R" | jq -r '.code')"

# ── 总结 ──────────────────────────────────────────────────
TOTAL=$((PASS + FAIL))
echo ""
echo -e "${YELLOW}========================================${NC}"
printf "  总计 %d  通过 ${GREEN}%d${NC}  失败 ${RED}%d${NC}\n" "$TOTAL" "$PASS" "$FAIL"
echo -e "${YELLOW}========================================${NC}"

[ $FAIL -eq 0 ] && echo -e "${GREEN}所有测试通过${NC}" && exit 0
echo -e "${RED}有 $FAIL 个测试失败${NC}" && exit 1
