#!/usr/bin/env bash
# ============================================================================
#  GridPerch 监控端备份 / Linux
# ----------------------------------------------------------------------------
#  备份内容（缺一不可）：
#    1. 数据库快照   —— 主机列表、历史指标、告警、设备账号（密码是密文）
#    2. gridperch.env —— 里面有 GRIDPERCH_TOKEN，设备密码的解密钥匙由它派生
#
#  ⚠️ 只备数据库不备 env：设备账号的密码将**永久无法解密**。
#     反过来只备 env 也没用。两个文件必须成对备份、成对恢复。
#
#  用法：
#    sudo ./backup.sh                  # 备份到 /home/gridperch/backups（无 /home 时退回 /var/backups/gridperch）
#    sudo ./backup.sh -d /mnt/nas/gp   # 指定目录（建议挂 NAS 或另一块盘）
#    sudo ./backup.sh -k 14            # 保留最近 14 份（默认 7）
#
#  定时任务（每天 2:30）：
#    echo '30 2 * * * root /opt/gridperch/deploy/backup.sh -k 14 >> /var/log/gridperch-backup.log 2>&1' \
#      > /etc/cron.d/gridperch-backup
#
#  恢复：
#    1) systemctl stop gridperch
#    2) 解包最新归档，把 gridperch.db 放回 /opt/gridperch/data/
#    3) 把 gridperch.env 放回 /etc/gridperch/ 并 chmod 600
#    4) systemctl start gridperch
# ============================================================================
set -euo pipefail

# 默认放到 /home 下：安装时 / 往往只切了几十 G，/home 才是大盘。
# 没有 /home 的机器（容器、单分区）自动退回 /var/backups。
if [ -d /home ] && [ -w /home ]; then
  DEST=/home/gridperch/backups
else
  DEST=/var/backups/gridperch
fi
KEEP=7
BIN=/opt/gridperch/gridperch-server
DB=/opt/gridperch/data/gridperch.db
ENVFILE=/etc/gridperch/gridperch.env
PREFIX=/opt/gridperch

usage() {
  sed -n '2,30p' "$0"
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    -d|--dest) DEST=${2:?}; shift 2 ;;
    -k|--keep) KEEP=${2:?}; shift 2 ;;
    -h|--help) usage ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "请用 root 执行（需要读 /etc/gridperch/gridperch.env）" >&2; exit 1; }
[ -x "$BIN" ] || { echo "找不到监控端程序：$BIN" >&2; exit 1; }
[ -f "$DB" ] || { echo "找不到数据库：$DB" >&2; exit 1; }
[ -f "$ENVFILE" ] || echo "警告：没有 $ENVFILE，设备密码将无法解密，请确认这是不是你要的" >&2

mkdir -p "$DEST"

# ---- 空间预检 ------------------------------------------------------------
# 快照（VACUUM INTO）只拷有效页，所以按「有效数据 + 20% 余量」判断，
# 而不是按数据库文件大小——库删过数据没 VACUUM 时文件会大得多。
# 空间不够就直接退出，绝不写到一半把磁盘塞满。
NEED=$("$BIN" -db "$DB" -dbsize 2>/dev/null || echo 0)
FREE=$(df -PB1 "$DEST" 2>/dev/null | awk 'NR==2{print $4}')
if [ "${NEED:-0}" -gt 0 ] && [ -n "${FREE:-}" ]; then
  NEED_MARGIN=$((NEED + NEED / 5))
  if [ "$FREE" -lt "$NEED_MARGIN" ]; then
    echo "磁盘空间不足，已跳过本次备份：" >&2
    echo "  预计需要 $((NEED_MARGIN / 1024 / 1024)) MB（有效数据 $((NEED / 1024 / 1024)) MB + 20% 余量）" >&2
    echo "  $DEST 当前可用 $((FREE / 1024 / 1024)) MB" >&2
    echo "  处理：等历史数据清理跑几轮后再试，或用 -d 指到有空间的磁盘 / NAS" >&2
    exit 1
  fi
  echo "==> 空间预检通过：需要约 $((NEED_MARGIN / 1024 / 1024)) MB，可用 $((FREE / 1024 / 1024)) MB"
fi

STAMP=$(date +%Y%m%d-%H%M%S)
# 中间目录必须放在目标分区上：快照本身有整个库那么大（现在 5.5G），
# 放 /tmp 会把根分区写满——而根分区通常是最小的那个。
WORK=$(mktemp -d "$DEST/.work.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

echo "==> 生成数据库快照（不停服）"
# VACUUM INTO：一致性快照 + 压实空闲页，出来的文件通常比原库小很多
"$BIN" -db "$DB" -backup "$WORK/gridperch.db"

echo "==> 收集配置与清单"
if [ -f "$ENVFILE" ]; then
  cp -p "$ENVFILE" "$WORK/gridperch.env"
  chmod 600 "$WORK/gridperch.env"
fi
{
  echo "备份时间: $(date '+%Y-%m-%d %H:%M:%S')"
  echo "版本: $("$BIN" -version 2>/dev/null || echo 未知)"
  echo "数据库大小: $(du -h "$WORK/gridperch.db" | cut -f1)"
  echo ""
  echo "⚠️ gridperch.env 里含 GRIDPERCH_TOKEN，是设备账号密码的解密钥匙。"
  echo "   本归档请按密码同等级别保管，不要放进公开网盘。"
} > "$WORK/README.txt"

# 打包时把 env 设成 600，解包后也不会被别人读到
ARCHIVE="$DEST/gridperch-$STAMP.tar.gz"
tar -czf "$ARCHIVE" -C "$WORK" .
chmod 600 "$ARCHIVE"

SIZE=$(du -h "$ARCHIVE" | cut -f1)
echo "==> 完成：$ARCHIVE（$SIZE）"

echo "==> 清理旧备份（保留最近 $KEEP 份）"
ls -1t "$DEST"/gridperch-*.tar.gz 2>/dev/null | tail -n +$((KEEP + 1)) | while read -r old; do
  echo "    删除 $(basename "$old")"
  rm -f "$old"
done

echo "==> 当前备份列表"
ls -lh "$DEST"/gridperch-*.tar.gz 2>/dev/null | awk '{print "    "$5"\t"$9}'
