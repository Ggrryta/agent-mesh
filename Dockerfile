FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o agent-gateway ./cmd/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o migrate ./cmd/migrate
# 把 skill tarball 烘焙进镜像,供 /skill/download 接口分发
# sha256 同样打出来供 /skill/version 接口校验,防止中间人替换
RUN cd agent-gateway-skill && \
    tar --exclude='.venv' --exclude='__pycache__' --exclude='.pytest_cache' \
        --exclude='*.pyc' --exclude='.DS_Store' --exclude='gas/tests' \
        -czf /app/skill-dist.tar.gz . && \
    sha256sum /app/skill-dist.tar.gz | awk '{print $1}' > /app/skill-dist.sha256 && \
    cp VERSION /app/skill-dist.version

FROM alpine:latest
WORKDIR /app
RUN apk add --no-cache bash
COPY --from=builder /app/agent-gateway .
COPY --from=builder /app/migrate .
COPY --from=builder /app/skill-dist.tar.gz /app/skill-dist/skill-dist.tar.gz
COPY --from=builder /app/skill-dist.sha256 /app/skill-dist/skill-dist.sha256
COPY --from=builder /app/skill-dist.version /app/skill-dist/skill-dist.version
COPY config/config.docker.yaml.example ./config/config.yaml
COPY frontend/ ./frontend/
COPY docker-entrypoint.sh ./
RUN chmod +x ./docker-entrypoint.sh
EXPOSE 8080
CMD ["./docker-entrypoint.sh"]
