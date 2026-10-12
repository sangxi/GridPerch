#!/usr/bin/env bash
# 跨平台构建脚本：一次产出监控服务系统的服务端与采集端静态二进制。
#
# 注意：版本发布 / 授权签发属于另一套独立系统（授权系统，工程目录 ../opsmonitor-hub），
# 本脚本不再构建它。
#
# 用法：
#   ./build.sh              # 只构建当前平台，输出到 bin/
#   ./build.sh all          # 交叉构建常见平台，输出到 dist/
set -euo pipefail

OUT=${OUT:-bin}
# 版本号取自 internal/version/version.go，构建时间在此注入，
# 控制台「在线升级」页与 /agent/version 接口都会展示。
VERSION=$(grep -m1 'var Version = ' internal/version/version.go | sed -E 's/.*"([^"]+)".*/\1/')
# 用不含空格的 ISO 时间，避免 -ldflags 被空格拆开
BUILD_TIME=$(date '+%Y-%m-%dT%H:%M:%S')
LDFLAGS="-s -w -X github.com/sangxi/OpsMonitor/internal/version.BuildTime=${BUILD_TIME}"
echo "版本 ${VERSION}，构建时间 ${BUILD_TIME}"

build_one() {
  local goos=$1 goarch=$2 ext=$3 out=$4
  echo "构建 ${goos}/${goarch} -> ${out}/"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "${out}/opsmonitor-server-${goos}-${goarch}${ext}" ./cmd/opsmonitor-server
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "${out}/opsmonitor-agent-${goos}-${goarch}${ext}" ./cmd/opsmonitor-agent
}

if [[ "${1:-}" == "all" ]]; then
  OUT=dist
  mkdir -p "$OUT"
  build_one linux amd64 "" "$OUT"
  build_one linux arm64 "" "$OUT"
  build_one darwin amd64 "" "$OUT"
  build_one darwin arm64 "" "$OUT"
  build_one windows amd64 ".exe" "$OUT"
  echo "全部完成，产物在 dist/ 目录"
  exit 0
fi

mkdir -p "$OUT"
# Windows 上必须带 .exe 后缀，否则控制台/计划任务按 opsmonitor-server.exe 调用会命中旧二进制
HOST_OS=$(go env GOOS)
HOST_EXT=""
if [[ "$HOST_OS" == "windows" ]]; then HOST_EXT=".exe"; fi
GOOS="$HOST_OS" GOARCH=$(go env GOARCH) CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/opsmonitor-server${HOST_EXT}" ./cmd/opsmonitor-server
GOOS="$HOST_OS" GOARCH=$(go env GOARCH) CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/opsmonitor-agent${HOST_EXT}" ./cmd/opsmonitor-agent
echo "构建完成：${OUT}/opsmonitor-server${HOST_EXT} ${OUT}/opsmonitor-agent${HOST_EXT}"
echo "版本 ${VERSION}（${BUILD_TIME}）"
