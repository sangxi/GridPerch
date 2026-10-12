# ============================================================================
#  OpsMonitor 监控端（server）镜像 · 直接从源码构建
# ----------------------------------------------------------------------------
#  适合 GitHub 用户 / CI：不需要预先准备二进制。
#
#  docker build -f deploy/docker/Dockerfile \
#    --build-arg VERSION=1.1.0 \
#    -t opsmonitor/opsmonitor-server:1.1.0 .
#
#  运行时数据目录挂到 /data，采集端发布物挂到 /releases，不要用容器内匿名卷。
# ============================================================================

FROM golang:1.23-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

# 先只拷依赖清单，让 go mod download 这一层能吃缓存
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=1.1.0
ARG BUILD_TIME=unknown
ARG TARGETARCH=amd64
RUN go build -trimpath \
      -ldflags "-s -w -X github.com/sangxi/OpsMonitor/internal/version.BuildTime=${BUILD_TIME}" \
      -o /out/opsmonitor-server ./cmd/opsmonitor-server \
 && /out/opsmonitor-server -version

# ---------------------------------------------------------------- 运行层
FROM alpine:3.20

# ca-certificates：访问授权中心（HTTPS）；tzdata：日志与告警时间正确
RUN apk add --no-cache ca-certificates tzdata \
 && mkdir -p /data /releases

COPY --from=build /out/opsmonitor-server /usr/local/bin/opsmonitor-server

ENV TZ=Asia/Shanghai \
    OPSM_ADDR=:9999 \
    OPSM_DB=/data/opsmonitor.db \
    OPSM_RELEASE_DIR=/releases

VOLUME ["/data", "/releases"]
EXPOSE 9999

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1${OPSM_ADDR}/api/health" >/dev/null 2>&1 || exit 1

WORKDIR /opt/opsmonitor
ENTRYPOINT ["/usr/local/bin/opsmonitor-server"]
