#!/usr/bin/env bash
# 额外构建：Windows 7 SP1 / Server 2008 R2 也能跑的采集端。
#
# ── 为什么需要单独一条构建链 ──────────────────────────────────────────────
# Go 1.21.5 起，runtime 的随机数源从 RtlGenRandom（advapi32.dll 的
# SystemFunction036，Windows XP 起就有）换成了 ProcessPrng
# （bcryptprimitives.dll）—— 而 bcryptprimitives.dll 只有 Windows 8 /
# Server 2012 及以上才存在。
#
# 在 Windows 7 / Server 2008 R2 上，runtime 用 LoadLibraryExW 加载它失败，
# 拿到的函数指针是 NULL，初始化阶段一调用就触发 0xc0000005 访问违规，进程
# 当场终止 —— 现象就是「双击后黑窗口一闪就没了」，既没有报错框也没有日志
# （崩在 runtime 初始化里，早于任何一行业务日志）。
#
# 官方口径：Go 1.21 起最低要求 Windows 10 / Windows Server 2016。官方最后
# 一个支持并在 Server 2008 R2 上实际测试过的版本是 Go 1.20。
#
# ── 本脚本做什么 ────────────────────────────────────────────────────────
# 用 Go 1.20.14 + 一份裁剪过的 modfile（go120.mod：去掉声明了 go 1.21 的
# sqlite/mysql，采集端本来也不依赖它们）单独编译 Windows 采集端。
#
# 产物名带 -win7 后缀，**不覆盖** dist/ 里的正式产物，也不参与 release.sh
# 上传 —— 平台下发的正式采集端仍是当前工具链构建的版本，两者互不影响。
#
# ── 用法 ────────────────────────────────────────────────────────────────
#   ./build-win7.sh                     # 输出 dist/opsmonitor-agent-windows-amd64-win7.exe
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
  *go1.2[1-9]*) echo "错误：$GO120 是 $VER，不是 1.20.x —— 1.21+ 构建的产物在 2008 R2 上会闪退。" >&2; exit 1 ;;
esac

VERSION=$(grep -m1 'var Version = ' internal/version/version.go | sed -E 's/.*"([^"]+)".*/\1/')
BUILD_TIME=$(date '+%Y-%m-%dT%H:%M:%S')
LDFLAGS="-s -w -X github.com/sangxi/OpsMonitor/internal/version.BuildTime=${BUILD_TIME}"
OUT=${OUT:-dist}
SUFFIX=${SUFFIX:-win7}

mkdir -p "$OUT"
echo "工具链  $VER"
echo "版本    ${VERSION}（构建时间 ${BUILD_TIME}）"
echo "modfile go120.mod（go 1.20）"
echo "产物    ${OUT}/opsmonitor-agent-windows-amd64-${SUFFIX}.exe"

GOOS=windows GOARCH=amd64 "$GO120" build -modfile=go120.mod -mod=mod -trimpath \
  -ldflags "$LDFLAGS" \
  -o "${OUT}/opsmonitor-agent-windows-amd64-${SUFFIX}.exe" ./cmd/opsmonitor-agent

echo
echo "完成。自检："
echo "  ./${OUT}/opsmonitor-agent-windows-amd64-${SUFFIX}.exe -version"
