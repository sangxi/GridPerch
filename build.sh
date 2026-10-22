#!/usr/bin/env bash
# 跨平台构建脚本：一次产出监控服务系统的服务端与采集端静态二进制。
#
# 注意：版本发布 / 授权签发属于另一套独立系统（授权端工程，不在本仓库内），
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
LDFLAGS="-s -w -X github.com/sangxi/gridperch/internal/version.BuildTime=${BUILD_TIME}"
echo "版本 ${VERSION}，构建时间 ${BUILD_TIME}"

build_one() {
  local goos=$1 goarch=$2 ext=$3 out=$4
  echo "构建 ${goos}/${goarch} -> ${out}/"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "${out}/gridperch-server-${goos}-${goarch}${ext}" ./cmd/gridperch-server
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "${out}/gridperch-agent-${goos}-${goarch}${ext}" ./cmd/gridperch-agent
}

# 按目录里现存的产物重算校验和。
# 必须由「最后跑的那个构建脚本」调用：build.sh all 出 10 个正式产物、
# build-win7.sh 追加兼容版，谁后跑谁重算，顺序换了也不会漏。
# 之前这一步是手工做的，结果 1.2.3 的 dist/ 里躺着 1.2.2 的 SHA256SUMS.txt，
# 用户照着校验会 11/11 FAILED —— 比没有校验和更糟。
write_sums() {
  local out=$1
  [[ -d "$out" ]] || return 0
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$out" && sha256sum gridperch-* > SHA256SUMS.txt )
  elif command -v shasum >/dev/null 2>&1; then
    ( cd "$out" && shasum -a 256 gridperch-* > SHA256SUMS.txt )
  else
    echo "警告：找不到 sha256sum / shasum，跳过校验和生成" >&2
    return 0
  fi
  echo "校验和 ${out}/SHA256SUMS.txt（$(wc -l < "${out}/SHA256SUMS.txt" | tr -d ' ') 条）"
}

if [[ "${1:-}" == "all" ]]; then
  OUT=dist
  mkdir -p "$OUT"
  build_one linux amd64 "" "$OUT"
  build_one linux arm64 "" "$OUT"
  build_one darwin amd64 "" "$OUT"
  build_one darwin arm64 "" "$OUT"
  build_one windows amd64 ".exe" "$OUT"
  # 记录本批产物对应的版本号：release.sh 上传前会比对，防止
  # 「复用旧 dist 却登记新版本号」——那会让平台自升级永远换不到新版本。
  printf '%s\n' "$VERSION" > "$OUT/VERSION"
  write_sums "$OUT"
  echo "全部完成，产物在 dist/ 目录"
  exit 0
fi

mkdir -p "$OUT"
# Windows 上必须带 .exe 后缀，否则控制台/计划任务按 gridperch-server.exe 调用会命中旧二进制
HOST_OS=$(go env GOOS)
HOST_EXT=""
if [[ "$HOST_OS" == "windows" ]]; then HOST_EXT=".exe"; fi
GOOS="$HOST_OS" GOARCH=$(go env GOARCH) CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/gridperch-server${HOST_EXT}" ./cmd/gridperch-server
GOOS="$HOST_OS" GOARCH=$(go env GOARCH) CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/gridperch-agent${HOST_EXT}" ./cmd/gridperch-agent
echo "构建完成：${OUT}/gridperch-server${HOST_EXT} ${OUT}/gridperch-agent${HOST_EXT}"
echo "版本 ${VERSION}（${BUILD_TIME}）"
