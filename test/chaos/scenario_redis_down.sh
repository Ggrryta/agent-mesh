#!/usr/bin/env bash
# Chaos 演练 2：停 Redis，观察降级
# 预期：FeedHub 降级为 local-only 模式，跨 Pod 广播失效但单 Pod 内仍正常推

set -euo pipefail
ADMIN_URL=${ADMIN_URL:-http://localhost:9090}

log() { echo "[$(date +%H:%M:%S)] $*"; }

log "=== Stopping Redis ==="
docker compose -f docker-compose.dev.yml stop redis >/dev/null
sleep 3

log "=== Health checks ==="
echo "/healthz: $(curl -s -o /dev/null -w "%{http_code}" $ADMIN_URL/healthz)"
echo "/readyz: $(curl -s -o /dev/null -w "%{http_code}" $ADMIN_URL/readyz)"

log "=== Restarting Redis ==="
docker compose -f docker-compose.dev.yml start redis >/dev/null
sleep 5

echo "/readyz post-recovery: $(curl -s -o /dev/null -w "%{http_code}" $ADMIN_URL/readyz)"
log "Done"
