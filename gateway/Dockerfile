# agent-mesh gateway 多阶段 Dockerfile。
#
# 支持构建所有服务（通过 SERVICE build arg 选择）：
#   docker build --build-arg SERVICE=server -t agent-mesh-gateway .
#   docker build --build-arg SERVICE=identity-svc -t agent-mesh-identity .
#   docker build --build-arg SERVICE=messaging-svc -t agent-mesh-messaging .
#   docker build --build-arg SERVICE=push-gateway -t agent-mesh-push .
#   docker build --build-arg SERVICE=gateway -t agent-mesh-api-gateway .

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /src

ARG SERVICE=server

RUN set -eux; \
    sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories; \
    apk add --no-cache git ca-certificates

ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/app ./cmd/${SERVICE}

FROM alpine:3.20
LABEL org.opencontainers.image.title="agent-mesh" \
      org.opencontainers.image.description="Agent-Mesh Service"

RUN set -eux; \
    sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories; \
    apk add --no-cache ca-certificates tzdata; \
    adduser -D -u 65532 nonroot

WORKDIR /app
COPY --from=builder /out/app /app/app
RUN chown -R nonroot:nonroot /app

USER nonroot:nonroot

EXPOSE 8080 9090

ENTRYPOINT ["/app/app"]
