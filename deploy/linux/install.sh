#!/usr/bin/env bash
# ============================================================================
#  Strix 监控端（server）一键部署 / Linux
# ----------------------------------------------------------------------------
#  自适应主流发行版，无需为不同发行版改脚本：
#    Debian / Ubuntu / 深度 Deepin / 麒麟 Kylin / 统信 UOS   -> apt + systemd
#    CentOS / RHEL / Rocky / AlmaLinux / Fedora / openEuler  -> yum|dnf + systemd
#    Alpine                                                  -> OpenRC
#    其它（无 systemd 也无 OpenRC）                            -> nohup 常驻
#
#  为什么能跨发行版通用：本程序是 **纯静态链接** 的 Go 二进制
#  （无 PT_INTERP、不依赖 glibc / libc.musl），所以不看发行版、不看 glibc 版本。
#
#  用法：
#    sudo ./install.sh                          # 用同目录/上级目录里的二进制
#    sudo ./install.sh -f /tmp/strix-server-linux-amd64
#    sudo ./install.sh -u https://example.com/strix-server-linux-amd64
#    sudo ./install.sh --token '你的密钥' --public-url http://10.0.0.5:9999
#    sudo ./install.sh --addr :8000 --no-firewall
#    sudo ./install.sh --uninstall
# ============================================================================
set -euo pipefail

VERSION="${STRIX_VERSION:-1.2.0}"
APP="strix-server"
# 前缀沿用顺序：显式 --prefix > STRIX_PREFIX > 更名前的 OPSM_PREFIX > 默认路径
PREFIX="${STRIX_PREFIX:-${OPSM_PREFIX:-/opt/strix}}"
BIN="$PREFIX/$APP"
CONF_DIR="/etc/strix"
CONF_FILE="$CONF_DIR/strix.env"
SERVICE="/etc/systemd/system/strix.service"
OPENRC_SERVICE="/etc/init.d/strix"
SVC_USER="strix"

# 更名前（OpsMonitor）留下的安装位置，用于检测并存冲突
LEGACY_SERVICE="/etc/systemd/system/opsmonitor.service"
LEGACY_PREFIX="/opt/opsmonitor"

ADDR=":9999"
TOKEN=""
ADMIN_USER="admin"
ADMIN_PASS=""
PUBLIC_URL=""
RETENTION="30"
SRC_FILE=""
SRC_URL=""
OPEN_FIREWALL=1
DO_UNINSTALL=0
FORCE=0

log()  { printf '\033[32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---------------------------------------------------------------- 参数解析
while [ $# -gt 0 ]; do
  case "$1" in
    -f|--file)       SRC_FILE="${2:?}"; shift 2 ;;
    -u|--url)        SRC_URL="${2:?}"; shift 2 ;;
    --addr)          ADDR="${2:?}"; shift 2 ;;
    --token)         TOKEN="${2:?}"; shift 2 ;;
    --admin-user)    ADMIN_USER="${2:?}"; shift 2 ;;
    --admin-pass)    ADMIN_PASS="${2:?}"; shift 2 ;;
    --public-url)    PUBLIC_URL="${2:?}"; shift 2 ;;
    --retention)     RETENTION="${2:?}"; shift 2 ;;
    --prefix)        PREFIX="${2:?}"; BIN="$PREFIX/$APP"; shift 2 ;;
    --no-firewall)   OPEN_FIREWALL=0; shift ;;
    --uninstall)     DO_UNINSTALL=1; shift ;;
    --force)         FORCE=1; shift ;;
    -h|--help)       usage ;;
    *) die "未知参数：$1（用 -h 查看用法）" ;;
  esac
done

[ "$(id -u)" = "0" ] || die "需要 root 权限，请用 sudo 运行。"

# 更名前的旧版本（OpsMonitor）如果还装着，新旧两套都监听同一个端口，会互相抢占。
if [ "$DO_UNINSTALL" = "0" ] && [ "$FORCE" = "0" ]; then
  if [ -f "$LEGACY_SERVICE" ] || [ -d "$LEGACY_PREFIX" ]; then
    warn "检测到更名前的旧版本：$LEGACY_PREFIX 或 $LEGACY_SERVICE"
    warn "新旧两套都监听同一端口，并存会互相抢占，只能留一个。"
    warn "  · 卸载旧版（保留数据目录）：进入旧程序目录执行 sudo ./install.sh --uninstall"
    warn "  · 迁移数据：停掉旧服务后，把 $LEGACY_PREFIX/data 整个拷到 $PREFIX/data"
    warn "确认不会冲突时，加 --force 跳过本检查。"
    die "已中止安装"
  fi
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------------------------------------------------------------- 卸载
if [ "$DO_UNINSTALL" = "1" ]; then
  log "停止并移除服务"
  if [ -f "$SERVICE" ]; then
    systemctl disable --now strix 2>/dev/null || true
    rm -f "$SERVICE"; systemctl daemon-reload 2>/dev/null || true
  fi
  [ -f "$OPENRC_SERVICE" ] && { rc-service strix stop 2>/dev/null || true
    rc-update del strix default 2>/dev/null || true; rm -f "$OPENRC_SERVICE"; }
  if [ -f /var/run/strix.pid ]; then
    kill "$(cat /var/run/strix.pid)" 2>/dev/null || true; rm -f /var/run/strix.pid
  fi
  warn "数据目录 $PREFIX/data 与配置 $CONF_FILE 已保留（含数据库）。确认不再需要时手工删除即可。"
  log "卸载完成"
  exit 0
fi

# ---------------------------------------------------------------- 环境探测
log "探测系统环境"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
fi
OS_PRETTY="${PRETTY_NAME:-$(uname -s)}"
[ "$(uname -s)" = "Linux" ] || die "本脚本用于 Linux。Windows 请用 deploy\\windows\\install.ps1，macOS 请用 deploy/macos/install.sh。"

case "$(uname -m)" in
  x86_64|amd64)   GOARCH=amd64 ;;
  aarch64|arm64)  GOARCH=arm64 ;;
  *) die "不支持的 CPU 架构：$(uname -m)（仅提供 amd64 与 arm64 产物）" ;;
esac

INIT="none"
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  INIT="systemd"
elif command -v rc-update >/dev/null 2>&1; then
  INIT="openrc"
fi

log "系统：$OS_PRETTY"
log "架构：$(uname -m)  ->  $GOARCH"
log "初始化：$INIT"

# ---------------------------------------------------------------- 取二进制
TMP_BIN="$(mktemp)"
cleanup() { rm -f "$TMP_BIN"; }
trap cleanup EXIT

# 候选路径。注意：产物（bin/、dist/、release/）都在 .gitignore 里，仓库不放二进制，
# 所以从 GitHub 克隆下来的目录天然没有这个文件——这不是下载失败，见下方报错文案。
# release/ 下按「版本号倒序」扫，不能写死 VERSION：脚本自带的默认版本号是占位符，
# 一旦落后于实际产物（例如脚本写 1.1.0、产物是 1.1.5）就会白找一圈。
REPO_ROOT="$SCRIPT_DIR/../.."
candidates=(
  "$SRC_FILE"
  "$SCRIPT_DIR/strix-server-linux-$GOARCH"
  "$SCRIPT_DIR/../strix-server-linux-$GOARCH"
  "$REPO_ROOT/strix-server-linux-$GOARCH"
  "$REPO_ROOT/dist/strix-server-linux-$GOARCH"
)
if [ -d "$REPO_ROOT/release" ]; then
  for d in $(ls -1 "$REPO_ROOT/release" 2>/dev/null | sort -Vr); do
    candidates+=("$REPO_ROOT/release/$d/strix-server-linux-$GOARCH")
  done
fi

if [ -n "$SRC_FILE" ]; then
  [ -f "$SRC_FILE" ] || die "找不到指定的文件：$SRC_FILE"
  cp -f "$SRC_FILE" "$TMP_BIN"
elif [ -n "$SRC_URL" ]; then
  log "下载二进制：$SRC_URL"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --connect-timeout 15 -o "$TMP_BIN" "$SRC_URL" || die "下载失败"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$TMP_BIN" "$SRC_URL" || die "下载失败"
  else
    die "系统里没有 curl 也没有 wget，请用 -f 指定本地文件"
  fi
else
  found=""
  for c in "${candidates[@]}"; do
    [ -n "$c" ] && [ -f "$c" ] && { found="$c"; break; }
  done
  [ -n "$found" ] || die "没找到 strix-server-linux-$GOARCH。

  原因：本仓库**不存放二进制**（bin/、dist/、release/ 均在 .gitignore 里），
        所以从 GitHub 克隆下来的目录里本来就没有这个文件 —— 不是下载失败。

  三种取法，任选其一：
    1) 从打包机/发布包把 strix-server-linux-$GOARCH 拷到本脚本同一目录，重新执行；
    2) 指定本地路径：  sudo ./install.sh -f /tmp/strix-server-linux-$GOARCH
    3) 指定下载地址：  sudo ./install.sh -u http://<文件服务器>/strix-server-linux-$GOARCH"
  log "使用本地二进制：$found"
  cp -f "$found" "$TMP_BIN"
fi

# 校验是不是真正的 Linux 二进制（防止误拷了 Windows/macOS 的产物）
if [ "$(head -c 4 "$TMP_BIN" | od -An -tx1 | tr -d ' \n')" != "7f454c46" ]; then
  die "这个文件不是 Linux ELF 可执行文件，请确认下载的是 strix-server-linux-$GOARCH"
fi

# 版本号以二进制自报的为准，别信脚本里的默认值（那个只是占位符）。
# 老版本二进制没有 -version 参数时由 flag 包直接报错退出，不会把服务跑起来。
chmod +x "$TMP_BIN"
if _out="$(timeout 5 "$TMP_BIN" -version 2>/dev/null | head -n1)"; then
  _v="$(printf '%s' "$_out" | awk '{print $2}')"
  case "$_v" in [0-9]*.[0-9]*) VERSION="$_v" ;; esac
fi

# ---------------------------------------------------------------- 安装
log "创建用户与目录"
if ! getent group "$SVC_USER" >/dev/null 2>&1; then
  groupadd -r "$SVC_USER" 2>/dev/null || addgroup -S "$SVC_USER" 2>/dev/null || true
fi
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  useradd -r -g "$SVC_USER" -s /sbin/nologin -d "$PREFIX" "$SVC_USER" 2>/dev/null \
    || adduser -S -D -H -G "$SVC_USER" -s /sbin/nologin "$SVC_USER" 2>/dev/null || true
fi

mkdir -p "$PREFIX/data" "$PREFIX/releases" "$CONF_DIR"
install -m 0755 "$TMP_BIN" "$BIN"

if [ ! -f "$CONF_FILE" ]; then
  log "生成配置文件 $CONF_FILE"
  [ -n "$TOKEN" ] || TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  [ -n "$ADMIN_PASS" ] || ADMIN_PASS="$(head -c 9 /dev/urandom | base64 | tr -d '/+=' | cut -c1-12)"
  [ -n "$PUBLIC_URL" ] || PUBLIC_URL="http://$(hostname -I 2>/dev/null | awk '{print $1}'):${ADDR##*:}"
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

chown -R "$SVC_USER:$SVC_USER" "$PREFIX"

# ---------------------------------------------------------------- 注册服务
PORT="${ADDR##*:}"

if [ "$INIT" = "systemd" ]; then
  log "注册 systemd 服务"
  cat > "$SERVICE" <<EOF
[Unit]
Description=Strix 运维监控服务端
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
WorkingDirectory=$PREFIX
EnvironmentFile=$CONF_FILE
ExecStart=$BIN
Restart=always
RestartSec=5
LimitNOFILE=65535
# 加固：只允许写数据目录
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=$PREFIX/data $PREFIX/releases

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now strix >/dev/null 2>&1 || systemctl restart strix
  sleep 2

elif [ "$INIT" = "openrc" ]; then
  log "注册 OpenRC 服务（Alpine 等）"
  cat > "$OPENRC_SERVICE" <<EOF
#!/sbin/openrc-run
name="strix"
description="Strix 运维监控服务端"
command="$BIN"
command_user="$SVC_USER:$SVC_USER"
directory="$PREFIX"
pidfile="/run/strix.pid"
command_background=true
output_log="/var/log/strix.log"
error_log="/var/log/strix.log"

depend() { need net; after firewall; }
EOF
  chmod +x "$OPENRC_SERVICE"
  # OpenRC 不读 EnvironmentFile，把变量注入到脚本里
  sed -i "1a export \$(grep -v '^#' $CONF_FILE | xargs) 2>/dev/null || true" "$OPENRC_SERVICE"
  rc-update add strix default >/dev/null 2>&1 || true
  rc-service strix restart >/dev/null 2>&1 || rc-service strix start || true
  sleep 2

else
  warn "未检测到 systemd / OpenRC，改用 nohup 常驻（重启机器后不会自启）"
  if [ -f /var/run/strix.pid ]; then kill "$(cat /var/run/strix.pid)" 2>/dev/null || true; fi
  set -a; # shellcheck disable=SC1090
  . "$CONF_FILE"; set +a
  nohup "$BIN" > /var/log/strix.log 2>&1 &
  echo $! > /var/run/strix.pid
  sleep 2
fi

# ---------------------------------------------------------------- 防火墙
if [ "$OPEN_FIREWALL" = "1" ] && [ -n "$PORT" ]; then
  if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    log "firewalld 放行 $PORT/tcp"
    firewall-cmd --permanent --add-port="$PORT/tcp" >/dev/null && firewall-cmd --reload >/dev/null || warn "放行失败，请手工执行"
  elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    log "ufw 放行 $PORT/tcp"
    ufw allow "$PORT/tcp" >/dev/null || warn "放行失败，请手工执行"
  else
    log "未检测到启用中的 firewalld/ufw，跳过（云服务器记得放行安全组）"
  fi
fi

# ---------------------------------------------------------------- SELinux
if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
  log "SELinux 处于 Enforcing，重建文件安全上下文"
  command -v restorecon >/dev/null 2>&1 && restorecon -R "$PREFIX" "$CONF_DIR" >/dev/null 2>&1 || true
  warn "若服务起不来且 journalctl 里出现 avc: denied，执行：
    semanage fcontext -a -t bin_t '$PREFIX(/.*)?' && restorecon -R $PREFIX
  仍不行则临时验证：setenforce 0（确认是 SELinux 后再做永久策略）"
fi

# ---------------------------------------------------------------- 结果
sleep 1
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
echo "  查看日志  : journalctl -u strix -f          # systemd"
echo "              tail -f /var/log/strix.log      # OpenRC / nohup"
echo "  重启      : systemctl restart strix"
echo "  卸载      : sudo $0 --uninstall"
echo
echo "  自检      : 启动日志里应出现「授权验签公钥指纹：278D-4852-7191-E7CE」"
echo "              不是这个值说明二进制不是本版本，请重新获取产物。"
