#!/usr/bin/env bash
# Chaos 演练 1：停 MySQL，观察 Gateway 降级行为
#
# 预期行为（设计契约）：
#   - /healthz 仍然返回 200（liveness 不依赖 MySQL）
#   - /readyz 立即返回 503（依赖 MySQL）
#   - 业务端点（POST /v1/admin/auth/login 等）返回 5xx 而非 hang
#   - MySQL 恢复后 /readyz 自动恢复，无需重启 Gateway
#
# 用法：
#   bash test/chaos/scenario_mysql_down.sh

set -euo pipefail

GATEWAY_URL=${GATEWAY_URL:-http://localhost:8080}
ADMIN_URL=${ADMIN_URL:-http://localhost:9090}

log() { echo "[$(date +%H:%M:%S)] $*"; }

check_endpoint() {
  local name=$1
  local url=$2
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 5 "$url")
  echo "$name: HTTP $code"
}

log "=== Step 1: baseline check ==="
check_endpoint "/healthz (business)" "$GATEWAY_URL/healthz"
check_endpoint "/readyz (business)"  "$GATEWAY_URL/readyz"
check_endpoint "/healthz (admin)"    "$ADMIN_URL/healthz"

log ""
log "=== Step 2: stopping MySQL container ==="
docker compose -f docker-compose.dev.yml stop mysql >/dev/null 2>&1

# 给 Gateway 几秒检测到失败
sleep 3

log ""
log "=== Step 3: state during MySQL outage ==="
check_endpoint "/healthz (expect 200)" "$GATEWAY_URL/healthz"
check_endpoint "/readyz (expect 503)" "$GATEWAY_URL/readyz"
check_endpoint "/metrics (expect 200)" "$ADMIN_URL/metrics"

# 业务接口应快速失败而非 hang
log ""
log "Testing business endpoint timeout behavior..."
start=$(date +%s%N)
code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 \
  -X POST -H "Content-Type: application/json" \
  -d '{"username":"chaos_test","password":"wrong"}' \
  "$GATEWAY_URL/v1/admin/auth/login" || echo "timeout")
end=$(date +%s%N)
elapsed_ms=$(( (end - start) / 1000000 ))
log "  login -> $code in ${elapsed_ms}ms (expect quick failure, not hang)"

log ""
log "=== Step 4: restart MySQL ==="
docker compose -f docker-compose.dev.yml start mysql >/dev/null 2>&1

# 等 MySQL 起来 + Gateway 探测恢复
log "Waiting for MySQL recovery..."
for i in {1..30}; do
  ready=$(curl -s -o /dev/null -w "%{http_code}" "$GATEWAY_URL/readyz")
  if [ "$ready" = "200" ]; then
    log "  /readyz recovered after ${i}s"
    break
  fi
  sleep 1
done

log ""
log "=== Step 5: post-recovery check ==="
check_endpoint "/healthz" "$GATEWAY_URL/healthz"
check_endpoint "/readyz" "$GATEWAY_URL/readyz"

log ""
log "=== Chaos test complete ==="
