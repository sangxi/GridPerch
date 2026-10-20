#!/usr/bin/env bash
# 额外构建：Windows 7 SP1 / 8 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 也能跑的采集端。
#
# ── 为什么需要单独一条构建链 ──────────────────────────────────────────────
# 官方口径：Go 1.21 起，编译出来的程序**最低要求 Windows 10 / Server 2016**，
# 之前把 Windows 7 / 8 / 8.1 与 Server 2008 R2 / 2012 / 2012 R2 全部列为不再支持。
# 官方最后一个支持这些系统、并实际测过的工具链是 Go 1.20。
#
# 具体崩法（实测）：
#   ① Go 1.21.5 起 runtime 的随机数源从 RtlGenRandom（advapi32!SystemFunction036）
#      换成 ProcessPrng（bcryptprimitives.dll）。这台机器上加载失败，函数指针是
#      NULL，初始化阶段一调用就 0xc0000005 —— 现象是「双击后黑窗口一闪就没了」，
#      既没报错框也没日志（崩在 runtime 初始化，早于任何一行业务日志）。
#   ② 即使随机数这一关过了（Server 2012 R2 上 bcryptprimitives 存在），
#      Go 1.21+ 还砍掉了 WSASocket 的旧回退、LoadLibraryEx 的回退与旧版控制台
#      句柄处理，这些在上面这些系统上同样会失败。
#
# 所以旧系统不是「打个补丁就好」，只能换工具链 —— 一条独立构建链最省事。
#
# ── 本脚本做什么 ────────────────────────────────────────────────────────
# 用 Go 1.20.14 + 一份裁剪过的 modfile（go120.mod：去掉声明了 go 1.21 的
# sqlite/mysql，采集端本来也不依赖它们）单独编译 Windows 采集端。
#
# 产物名带 -win7 后缀（沿用下来的叫法，实际覆盖 2008 R2 到 2012 R2 全部旧系统），
# **不覆盖** dist/ 里的正式产物，也不参与 release.sh 上传。
# 平台要能把它发给旧系统，需在控制台「版本发布」用**同一版本号**再传一次，
# 并勾选「旧系统兼容版」；记录靠 variant 区分，两份产物并存互不影响。
#
# 产物里注入了 version.LegacyWindows=1，采集端自升级时会带 legacy=1 查版本，
# 平台只回兼容版 —— 否则自动升级会把它换成正式版，换完就起不来。
#
# ── 用法 ────────────────────────────────────────────────────────────────
#   ./build-win7.sh                     # 输出 dist/strix-agent-windows-amd64-win7.exe
#   OUT=/tmp/x ./build-win7.sh          # 指定输出目录
#   GO120=/path/to/go ./build-win7.sh   # 指定 Go 1.20 工具链
set -euo pipefail

GO120=${GO120:-D:/c/opsmonitor-buildtools/goroot120/go/bin/go.exe}
export GOPATH=${GOPATH:-D:/c/opsmonitor-buildtools/gopath}
export GOCACHE=${GOCACHE:-D:/c/opsmonitor-buildtools/gocache120}
export GOPROXY=${GOPROXY:-https://goproxy.cn,direct}
export GOSUMDB=off
export CGO_ENABLED=0

if [ ! -x "$GO120" ] && [ ! -f "$GO120" ]; then
  echo "找不到 Go 1.20 工具链：$GO120" >&2
  echo "下载：https://mirrors.aliyun.com/golang/go1.20.14.windows-amd64.zip" >&2
  echo "解压到 D:/c/opsmonitor-buildtools/goroot120/（解压后应是 goroot120/go/bin/go.exe）" >&2
  exit 1
fi

VER=$("$GO120" version)
case "$VER" in
  *go1.2[1-9]*) echo "错误：$GO120 是 $VER，不是 1.20.x —— 1.21+ 构建的产物在 2008 R2 / 2012 R2 上会闪退。" >&2; exit 1 ;;
esac

VERSION=$(grep -m1 'var Version = ' internal/version/version.go | sed -E 's/.*"([^"]+)".*/\1/')
BUILD_TIME=$(date '+%Y-%m-%dT%H:%M:%S')
# LegacyWindows=1 是这份产物的「身份」：采集端启动后会带上 legacy=1 查版本，
# 平台因此只回兼容版，不会被自动升级换成跑不起来的正式版。
LDFLAGS="-s -w -X github.com/sangxi/strix/internal/version.BuildTime=${BUILD_TIME} -X github.com/sangxi/strix/internal/version.LegacyWindows=1"
OUT=${OUT:-dist}
SUFFIX=${SUFFIX:-win7}

mkdir -p "$OUT"
echo "工具链  $VER"
echo "版本    ${VERSION}（构建时间 ${BUILD_TIME}）"
echo "modfile go120.mod（go 1.20）"
echo "产物    ${OUT}/strix-agent-windows-amd64-${SUFFIX}.exe"

GOOS=windows GOARCH=amd64 "$GO120" build -modfile=go120.mod -mod=mod -trimpath \
  -ldflags "$LDFLAGS" \
  -o "${OUT}/strix-agent-windows-amd64-${SUFFIX}.exe" ./cmd/strix-agent

# 重算校验和：这时 dist/ 里通常还有 build.sh all 的 10 个正式产物，
# 一并纳入，否则发出去的 SHA256SUMS.txt 会漏掉兼容版这一条。
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$OUT" && sha256sum strix-* > SHA256SUMS.txt )
  echo "校验和 ${OUT}/SHA256SUMS.txt（$(wc -l < "${OUT}/SHA256SUMS.txt" | tr -d ' ') 条）"
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$OUT" && shasum -a 256 strix-* > SHA256SUMS.txt )
  echo "校验和 ${OUT}/SHA256SUMS.txt（$(wc -l < "${OUT}/SHA256SUMS.txt" | tr -d ' ') 条）"
fi

echo
echo "完成。自检："
echo "  ./${OUT}/strix-agent-windows-amd64-${SUFFIX}.exe -version"
