#!/bin/bash
set -e

echo "[entrypoint] running migrations..."
./migrate -config /app/config/config.yaml

echo "[entrypoint] starting agent-gateway..."
exec ./agent-gateway -config /app/config/config.yaml
