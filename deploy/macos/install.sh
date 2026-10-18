#!/usr/bin/env bash
# ============================================================================
#  Strix 监控端（server）一键部署 / macOS
# ----------------------------------------------------------------------------
#  自动识别芯片并选用对应二进制：
#    Intel 芯片    -> strix-server-darwin-amd64
#    Apple 芯片(M系列) -> strix-server-darwin-arm64
#
#  用 launchd 注册开机自启（macOS 原生守护方式，无需第三方工具）。
#
#  用法：
#    sudo ./install.sh
#    sudo ./install.sh -f ./strix-server-darwin-arm64
#    sudo ./install.sh --token '你的密钥' --admin-pass '你的密码'
#    sudo ./install.sh --addr :8000
#    sudo ./install.sh --uninstall
# ============================================================================
set -euo pipefail

VERSION="${STRIX_VERSION:-1.2.2}"
APP="strix-server"
PREFIX="${STRIX_PREFIX:-/usr/local/strix}"
BIN="$PREFIX/$APP"
CONF_DIR="/usr/local/etc/strix"
CONF_FILE="$CONF_DIR/strix.env"
RUNNER="$PREFIX/start-server.sh"
PLIST="/Library/LaunchDaemons/com.strix.server.plist"
LABEL="com.strix.server"

ADDR=":9999"
TOKEN=""
ADMIN_USER="admin"
ADMIN_PASS=""
PUBLIC_URL=""
RETENTION="30"
SRC_FILE=""
DO_UNINSTALL=0

log()  { printf '\033[32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
usage(){ sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [ $# -gt 0 ]; do
  case "$1" in
    -f|--file)     SRC_FILE="${2:?}"; shift 2 ;;
    --addr)        ADDR="${2:?}"; shift 2 ;;
    --token)       TOKEN="${2:?}"; shift 2 ;;
    --admin-user)  ADMIN_USER="${2:?}"; shift 2 ;;
    --admin-pass)  ADMIN_PASS="${2:?}"; shift 2 ;;
    --public-url)  PUBLIC_URL="${2:?}"; shift 2 ;;
    --retention)   RETENTION="${2:?}"; shift 2 ;;
    --prefix)      PREFIX="${2:?}"; BIN="$PREFIX/$APP"; RUNNER="$PREFIX/start-server.sh"; shift 2 ;;
    --uninstall)   DO_UNINSTALL=1; shift ;;
    -h|--help)     usage ;;
    *) die "未知参数：$1（用 -h 查看用法）" ;;
  esac
done

[ "$(id -u)" = "0" ] || die "需要 root 权限，请用 sudo 运行。"
[ "$(uname -s)" = "Darwin" ] || die "本脚本用于 macOS。Linux 请用 deploy/linux/install.sh，Windows 请用 deploy/windows/install.ps1。"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------------------------------------------------------------- 卸载
if [ "$DO_UNINSTALL" = "1" ]; then
  log "停止并移除 launchd 服务"
  launchctl bootout "system/$LABEL" 2>/dev/null || launchctl unload -w "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"
  pgrep -f "$BIN" >/dev/null 2>&1 && pkill -f "$BIN" || true
  warn "数据目录 $PREFIX/data 与配置 $CONF_FILE 已保留（含数据库）。确认不再需要时手工删除即可。"
  log "卸载完成"
  exit 0
fi

# ---------------------------------------------------------------- 环境探测
log "探测系统环境"
MACOS_VER="$(sw_vers -productVersion 2>/dev/null || echo '未知')"
case "$(uname -m)" in
  x86_64)  GOARCH=amd64; CHIP="Intel 芯片" ;;
  arm64)   GOARCH=arm64; CHIP="Apple 芯片（M 系列）" ;;
  *) die "不支持的 CPU 架构：$(uname -m)" ;;
esac
log "系统：macOS $MACOS_VER"
log "芯片：$CHIP  ->  $GOARCH"

# ---------------------------------------------------------------- 取二进制
TMP_BIN="$(mktemp /tmp/strix.XXXXXX)"
trap 'rm -f "$TMP_BIN"' EXIT

# 候选路径。产物（bin/、dist/、release/）都在 .gitignore 里，仓库不放二进制，
# 所以从 GitHub 克隆下来的目录天然没有这个文件——见下方报错文案。
# release/ 下按「版本号倒序」扫，不能写死 VERSION：脚本自带的默认版本号只是占位符。
REPO_ROOT="$SCRIPT_DIR/../.."
candidates=(
  "$SRC_FILE"
  "$SCRIPT_DIR/strix-server-darwin-$GOARCH"
  "$SCRIPT_DIR/../strix-server-darwin-$GOARCH"
  "$REPO_ROOT/strix-server-darwin-$GOARCH"
  "$REPO_ROOT/dist/strix-server-darwin-$GOARCH"
)
if [ -d "$REPO_ROOT/release" ]; then
  for d in $(ls -1 "$REPO_ROOT/release" 2>/dev/null | sort -Vr); do
    candidates+=("$REPO_ROOT/release/$d/strix-server-darwin-$GOARCH")
  done
fi
found=""
if [ -n "$SRC_FILE" ]; then
  [ -f "$SRC_FILE" ] || die "找不到指定的文件：$SRC_FILE"
  found="$SRC_FILE"
else
  for c in "${candidates[@]}"; do
    [ -n "$c" ] && [ -f "$c" ] && { found="$c"; break; }
  done
fi
[ -n "$found" ] || die "没找到 strix-server-darwin-$GOARCH。

  原因：本仓库**不存放二进制**（bin/、dist/、release/ 均在 .gitignore 里），
        所以从 GitHub 克隆下来的目录里本来就没有这个文件 —— 不是下载失败。

  取法：从打包机/发布包把 strix-server-darwin-$GOARCH 拷到本脚本同一目录，
        或用 -f 指定路径：  sudo ./install.sh -f /tmp/strix-server-darwin-$GOARCH"
log "使用二进制：$found"
cp -f "$found" "$TMP_BIN"

# 校验是不是 macOS Mach-O（防止误用 Linux/Windows 产物）
MAGIC="$(head -c 4 "$TMP_BIN" | od -An -tx1 | tr -d ' \n')"
case "$MAGIC" in
  cffaedfe|cefaedfe|cafebabe) : ;;
  *) die "这个文件不是 macOS Mach-O 可执行文件，请确认用的是 strix-server-darwin-$GOARCH" ;;
esac

# 版本号以二进制自报的为准（老版本没有 -version 时会直接报错退出，不会把服务跑起来）
chmod +x "$TMP_BIN"
if _out="$(timeout 5 "$TMP_BIN" -version 2>/dev/null | head -n1)"; then
  _v="$(printf '%s' "$_out" | awk '{print $2}')"
  case "$_v" in [0-9]*.[0-9]*) VERSION="$_v" ;; esac
fi

# ---------------------------------------------------------------- 安装
log "创建目录"
mkdir -p "$PREFIX/data" "$PREFIX/releases" "$PREFIX/logs" "$CONF_DIR"
install -m 0755 "$TMP_BIN" "$BIN"

if [ ! -f "$CONF_FILE" ]; then
  log "生成配置文件 $CONF_FILE"
  [ -n "$TOKEN" ] || TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  [ -n "$ADMIN_PASS" ] || ADMIN_PASS="$(head -c 9 /dev/urandom | base64 | tr -d '/+=' | cut -c1-12)"
  [ -n "$PUBLIC_URL" ] || PUBLIC_URL="http://$(ipconfig getifaddr en0 2>/dev/null || echo 127.0.0.1)${ADDR}"
  cat > "$CONF_FILE" <<EOF
# Strix 监控端配置（由 install.sh 生成，$VERSION）
STRIX_ADDR=$ADDR
STRIX_DB=$PREFIX/data/strix.db
STRIX_TOKEN=$TOKEN
STRIX_ADMIN_USER=$ADMIN_USER
STRIX_ADMIN_PASS=$ADMIN_PASS
STRIX_PUBLIC_URL=$PUBLIC_URL
STRIX_RETENTION=$RETENTION
EOF
  chmod 600 "$CONF_FILE"
  NEW_CRED=1
else
  log "配置文件已存在，保留原内容：$CONF_FILE"
  NEW_CRED=0
fi

# launchd 不读取环境文件，用一个 wrapper 加载配置后再 exec 主程序
log "生成启动包装脚本 $RUNNER"
cat > "$RUNNER" <<EOF
#!/bin/sh
# 由 Strix install.sh 生成：加载配置后启动监控端
set -a
[ -f "$CONF_FILE" ] && . "$CONF_FILE"
set +a
exec "$BIN"
EOF
chmod 755 "$RUNNER"

# ---------------------------------------------------------------- launchd
log "注册 launchd 守护（开机自启 + 崩溃自动拉起）"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>            <string>$LABEL</string>
    <key>ProgramArguments</key>
    <array><string>$RUNNER</string></array>
    <key>WorkingDirectory</key> <string>$PREFIX</string>
    <key>RunAtLoad</key>        <true/>
    <key>KeepAlive</key>        <true/>
    <key>ThrottleInterval</key> <integer>5</integer>
    <key>StandardOutPath</key>  <string>$PREFIX/logs/server.log</string>
    <key>StandardErrorPath</key><string>$PREFIX/logs/server.log</string>
</dict>
</plist>
EOF
chown root:wheel "$PLIST"
chmod 644 "$PLIST"

# 新老 macOS 的加载方式不同，两种都试
launchctl bootout "system/$LABEL" 2>/dev/null || true
if ! launchctl bootstrap system "$PLIST" 2>/dev/null; then
  launchctl unload -w "$PLIST" 2>/dev/null || true
  launchctl load -w "$PLIST"
fi
launchctl enable "system/$LABEL" 2>/dev/null || true

# ---------------------------------------------------------------- 应用防火墙
if /usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate 2>/dev/null | grep -q "enabled"; then
  log "系统防火墙已开启，为程序放行入站"
  /usr/libexec/ApplicationFirewall/socketfilterfw --add "$BIN" >/dev/null 2>&1 || true
  /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp "$BIN" >/dev/null 2>&1 || true
else
  log "系统防火墙未开启，跳过放行"
fi

# ---------------------------------------------------------------- 结果
sleep 2
echo
log "部署完成"
echo "  程序      : $BIN（版本 $VERSION）"
echo "  配置      : $CONF_FILE"
echo "  数据      : $PREFIX/data/strix.db"
echo "  监听      : $ADDR"
echo "  控制台    : $PUBLIC_URL"
if [ "$NEW_CRED" = "1" ]; then
  echo
  echo "  ── 控制台账号（只在这里显示一次，请立刻保存）──"
  echo "     用户名  : $ADMIN_USER"
  echo "     密码    : $ADMIN_PASS"
  echo "     通信密钥: $TOKEN   （采集端要用同一值）"
fi
echo
echo "  查看日志  : tail -f $PREFIX/logs/server.log"
echo "  服务状态  : sudo launchctl print system/$LABEL | head -20"
echo "  重启      : sudo launchctl kickstart -k system/$LABEL"
echo "  停止      : sudo launchctl bootout system/$LABEL"
echo "  启动      : sudo launchctl bootstrap system $PLIST"
echo "  卸载      : sudo $0 --uninstall"
echo
echo "  自检      : 启动日志里应出现「授权验签公钥指纹：278D-4852-7191-E7CE」"
echo "              不是这个值说明二进制不是本版本，请重新获取产物。"
