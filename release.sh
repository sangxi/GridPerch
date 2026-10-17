#!/usr/bin/env bash
# 一键构建并向「授权系统」（strix-hub）上传采集端发布物。
#
# 版本发布与授权都集中在授权系统上：授权系统按「类型 + 系统 + CPU 架构」为每个组合登记一个
# 发布物，平台（strix-server）配置 -hub 后会定期把最新版**代拉**回本地缓存，再分发给
# 内网采集端。手工交叉构建 5 个平台再逐个上传很容易漏掉某一个，结果就是目标主机上跑
# 安装命令时才发现「服务端尚未发布该平台的采集端版本」。本脚本把这几步合成一条命令。
#
# 用法：
#   ./release.sh                                  # 构建全部平台并上传到授权系统 http://127.0.0.1:8090
#   ./release.sh -s https://你的授权中心地址         # 指定授权系统地址（默认已内置，不对外展示）
#   ./release.sh -u admin -p 你的后台密码          # 指定授权系统后台账号（默认 admin/strix）
#   ./release.sh -v 1.0.1 -n "修复磁盘统计偏差"    # 指定版本号与更新说明
#   ./release.sh --only linux/amd64,windows/amd64 # 只处理部分平台
#   ./release.sh --skip-build                     # 复用 dist/ 里已构建好的产物
#   ./release.sh --with-server                   # 同时把服务端（strix-server）也发到授权系统
#   ./release.sh --min-version 1.0.0              # 低于该版本的采集端强制升级
#   ./release.sh -t hub-secret                    # 指定授权系统拉取令牌，用于上传后的自检
#
# 全部参数也支持环境变量：STRIX_HUB / STRIX_USER / STRIX_PASSWORD / STRIX_HUB_TOKEN /
# STRIX_NOTES / STRIX_MIN_VERSION。
set -euo pipefail

HUB=${STRIX_HUB:-http://127.0.0.1:8090}
USER_NAME=${STRIX_USER:-admin}
PASSWORD=${STRIX_PASSWORD:-strix}
HUB_TOKEN=${STRIX_HUB_TOKEN:-strix-hub}
NOTES=${STRIX_NOTES:-}
MIN_VERSION=${STRIX_MIN_VERSION:-}
ONLY=${STRIX_ONLY:-}
SKIP_BUILD=0
WITH_SERVER=0
VERSION=""

usage() {
  cat <<'EOF'
一键构建并向授权系统（strix-hub）上传发布物

  ./release.sh [选项]

  -s, --hub URL         授权系统地址（默认 http://127.0.0.1:8090）
  -u, --user NAME       授权系统后台账号（默认 admin）
  -p, --password PASS   授权系统后台密码（默认 strix）
  -t, --hub-token TOKEN 授权系统拉取令牌，仅用于上传后的自检（默认 strix-hub）
  -v, --version VER     版本号，默认取 internal/version/version.go
  -n, --notes TEXT      更新说明，会显示在授权系统版本列表与平台升级页
      --min-version VER 强升门槛，低于它的采集端会被强制升级
      --only LIST       只处理指定平台，逗号分隔，如 linux/amd64,darwin/arm64
      --with-server     同时把服务端 strix-server 也发布到授权系统（用于平台在线自升级）
      --skip-build      不重新构建，直接复用 dist/ 下的产物
  -h, --help            显示本帮助
EOF
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    -s|--hub)        HUB=${2:?}; shift 2 ;;
    -u|--user)       USER_NAME=${2:?}; shift 2 ;;
    -p|--password)   PASSWORD=${2:?}; shift 2 ;;
    -t|--hub-token)  HUB_TOKEN=${2:?}; shift 2 ;;
    -v|--version)    VERSION=${2:?}; shift 2 ;;
    -n|--notes)      NOTES=${2:?}; shift 2 ;;
    --min-version)   MIN_VERSION=${2:?}; shift 2 ;;
    --only)          ONLY=${2:?}; shift 2 ;;
    --with-server)   WITH_SERVER=1; shift ;;
    --skip-build)    SKIP_BUILD=1; shift ;;
    -h|--help)       usage ;;
    *) echo "未知参数：$1（用 -h 查看用法）" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")"
HUB=${HUB%/}

if [ -z "$VERSION" ]; then
  VERSION=$(grep -m1 'var Version = ' internal/version/version.go | sed -E 's/.*"([^"]+)".*/\1/')
fi
if [ -z "$VERSION" ]; then
  echo "无法从 internal/version/version.go 读取版本号，请用 -v 指定" >&2
  exit 1
fi

# 防呆：dist/ 里的产物是按哪个版本构建的，必须与本次要登记的版本号一致。
# 事故原型：改了 -v 却没重新构建（--skip-build 复用上一版 dist）——
# 授权端登记成 1.1.7、二进制却自报 1.1.6，平台「在线升级」会每轮同步都
# 下载→替换→重启，版本号永远不变（表现为「平台无法更新自己」）。
# dist/VERSION 由 build.sh all 写入，没有该文件（旧产物）时跳过检查。
if [ -f dist/VERSION ]; then
  BUILT=$(tr -d ' \t\r\n' < dist/VERSION)
  if [ -n "$BUILT" ] && [ "$BUILT" != "$VERSION" ]; then
    echo "错误：dist/ 里的产物是 v$BUILT 构建的，与本次要登记的 v$VERSION 不一致，已中止。" >&2
    echo "      去掉 --skip-build 重新构建，或把 -v 改成 $BUILT（并确保 version.go 也是 $BUILT）。" >&2
    exit 1
  fi
fi

PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"
if [ -n "$ONLY" ]; then
  PLATFORMS=$(printf '%s' "$ONLY" | tr ',' ' ')
fi

echo "授权系统 $HUB"
echo "版本号   $VERSION"

if [ "$SKIP_BUILD" = "0" ]; then
  echo "==> 交叉构建全部平台（./build.sh all）"
  ./build.sh all
else
  echo "==> 跳过构建，直接复用 dist/ 下的产物"
fi

# ---------------- 登录授权系统后台 ----------------
# 临时文件放在脚本目录：Windows 的 Git Bash 里 mktemp 返回 /tmp/...，
# 某些安全策略会拦截删除这类映射路径，导致脚本在收尾时以非 0 退出。
COOKIE=".release-cookie.$$"
trap 'rm -f "$COOKIE" 2>/dev/null || true' EXIT

echo "==> 登录授权系统后台（$USER_NAME）"
LOGIN=$(curl -sS -c "$COOKIE" -X POST "$HUB/api/admin/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER_NAME\",\"password\":\"$PASSWORD\"}" 2>&1 || true)
case "$LOGIN" in
  *'"ok":true'*) echo "    登录成功" ;;
  *) echo "    登录失败：$LOGIN" >&2; exit 1 ;;
esac

# ---------------- 上传单个发布物 ----------------
UPLOADED=""

upload_one() {
  kind=$1; goos=$2; goarch=$3; file=$4
  if [ ! -f "$file" ]; then
    echo "==> ${kind} ${goos}/${goarch}  跳过：未找到 ${file}"
    return 1
  fi
  size=$(wc -c < "$file" | tr -d ' \t')
  echo "==> 上传 ${kind} ${goos}/${goarch}（$((size / 1024 / 1024)) MB）"
  resp=$(curl -sS -b "$COOKIE" -X POST "$HUB/api/admin/releases" \
    -F "kind=$kind" \
    -F "version=$VERSION" \
    -F "goos=$goos" \
    -F "goarch=$goarch" \
    -F "latest=true" \
    -F "notes=$NOTES" \
    -F "minVersion=$MIN_VERSION" \
    -F "file=@$file" 2>&1 || true)
  case "$resp" in
    *'"ok":true'*)
      m=$(printf '%s' "$resp" | sed -n 's/.*"msg":"\([^"]*\)".*/\1/p')
      echo "    ${m:-已发布 v$VERSION}"
      return 0 ;;
    *) echo "    上传失败：$resp" >&2; return 1 ;;
  esac
}

ok_count=0
fail_count=0

for pf in $PLATFORMS; do
  goos=${pf%%/*}
  goarch=${pf##*/}
  ext=""
  if [ "$goos" = "windows" ]; then ext=".exe"; fi
  if upload_one agent "$goos" "$goarch" "dist/strix-agent-${goos}-${goarch}${ext}"; then
    ok_count=$((ok_count + 1))
  else
    fail_count=$((fail_count + 1))
  fi
done

if [ "$WITH_SERVER" = "1" ]; then
  echo "==> 同时发布服务端（strix-server）"
  for pf in $PLATFORMS; do
    goos=${pf%%/*}
    goarch=${pf##*/}
    ext=""
    if [ "$goos" = "windows" ]; then ext=".exe"; fi
    if upload_one server "$goos" "$goarch" "dist/strix-server-${goos}-${goarch}${ext}"; then
      ok_count=$((ok_count + 1))
    else
      fail_count=$((fail_count + 1))
    fi
  done
fi

echo
echo "==> 结果：成功 ${ok_count} 个发布物，失败 ${fail_count} 个"

# ---------------- 自检 ----------------
# 模拟平台代拉与回执两步：
#   1) /api/v1/check  —— 平台代拉的第一步，通过就说明平台能拉到、采集端安装能继续
#   2) /api/v1/report —— 平台把升级结果回执给授权端，通过就说明双向通信是通的
# 第 2 步是授权端侧的接口，若返回 404 只说明该版本授权端还没提供，不影响部署。
if [ "$ok_count" -gt 0 ] && [ -n "$HUB_TOKEN" ]; then
  echo "==> 自检 1/2：模拟平台向授权系统查询各平台采集端版本"
  for pf in $PLATFORMS; do
    goos=${pf%%/*}
    goarch=${pf##*/}
    v=$(curl -sS "$HUB/api/v1/check?os=$goos&arch=$goarch&token=$HUB_TOKEN&version=$VERSION&agentVersion=0.0.0" \
      | sed -n 's/.*"agent":{[^}]*"version":"\([^"]*\)".*/\1/p')
    if [ -n "$v" ]; then
      echo "    agent ${goos}/${goarch} -> v${v}"
    else
      echo "    agent ${goos}/${goarch} -> 授权系统未返回该平台版本，请检查上面的上传报错"
      fail_count=$((fail_count + 1))
    fi
  done

  echo "==> 自检 2/2：验证平台 → 授权系统的回执通道"
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$HUB/api/v1/report" \
    -H 'Content-Type: application/json' \
    -H "X-Opsm-Hub-Token: $HUB_TOKEN" \
    -d "{\"action\":\"release-check\",\"version\":\"$VERSION\",\"result\":\"ok\"}" || echo 000)
  case "$code" in
    200) echo "    回执通道正常（授权端可接收平台升级与采集端升级统计）" ;;
    404) echo "    回执接口尚未在授权端提供（HTTP 404）：单向代拉不受影响，双向统计暂不可用" ;;
    403|401) echo "    回执被拒（HTTP $code）：-hub-token 与授权系统 -token 不一致" ;;
    *) echo "    回执通道返回 HTTP $code，请检查授权端可达性" ;;
  esac
fi

if [ "$fail_count" -gt 0 ]; then
  exit 1
fi
echo "全部完成。平台会在 -hub-interval 内自动代拉最新版本与授权；"
echo "检测到服务端新版本时按 -hub-auto-update 策略自动就位（重启后生效）。"
