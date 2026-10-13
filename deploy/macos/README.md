# 监控端（server）· macOS 部署

支持 macOS 11 (Big Sur) 及以上，Intel 与 Apple 芯片（M 系列）都支持。

## 快速开始

```bash
# 1) 把二进制和本目录一起拷到 Mac（二进制会自动按芯片挑）
cd deploy/macos
chmod +x install.sh
sudo ./install.sh
```

脚本会自动：

1. `uname -m` 判断芯片 → Intel 用 `opsmonitor-server-darwin-amd64`，M 系列用 `-arm64`
2. 装到 `/usr/local/opsmonitor/opsmonitor-server`
3. 生成配置 `/usr/local/etc/opsmonitor/opsmonitor.env`（权限 600）
4. 注册 launchd 守护 `com.opsmonitor.server`（开机自启 + 崩溃自动拉起）
5. 若系统防火墙开着，自动为本程序放行入站
6. 打印**只显示一次**的控制台账号与通信密钥

> 用 launchd 而不是 `nohup`：macOS 上只有 launchd 托管的进程才能可靠地开机自启并自动重启。

## 常用参数

```bash
sudo ./install.sh -f ./opsmonitor-server-darwin-arm64   # 指定二进制
sudo ./install.sh --addr :8000                          # 换端口
sudo ./install.sh --token '你的密钥' --admin-pass '你的密码'
sudo ./install.sh --public-url http://10.0.0.5:9999
sudo ./install.sh --uninstall                           # 卸载（保留数据）
```

## 目录规划

| 路径 | 用途 |
|---|---|
| `/usr/local/opsmonitor/opsmonitor-server` | 主程序 |
| `/usr/local/opsmonitor/start-server.sh` | launchd 调用的包装脚本（加载配置后 exec 主程序） |
| `/usr/local/opsmonitor/data/opsmonitor.db` | SQLite 数据库 |
| `/usr/local/opsmonitor/logs/server.log` | 程序日志 |
| `/usr/local/etc/opsmonitor/opsmonitor.env` | 配置（权限 600） |
| `/Library/LaunchDaemons/com.opsmonitor.server.plist` | launchd 单元 |

## 日常运维

```bash
# 日志
tail -f /usr/local/opsmonitor/logs/server.log

# 状态
sudo launchctl print system/com.opsmonitor.server | head -20

# 重启 / 停止 / 启动
sudo launchctl kickstart -k system/com.opsmonitor.server
sudo launchctl bootout system/com.opsmonitor.server
sudo launchctl bootstrap system /Library/LaunchDaemons/com.opsmonitor.server.plist

# 临时禁用开机自启（不禁用当前运行）
sudo launchctl disable system/com.opsmonitor.server
```

## 手工部署（不用脚本）

```bash
sudo mkdir -p /usr/local/opsmonitor/{data,logs} /usr/local/etc/opsmonitor
sudo install -m 0755 opsmonitor-server-darwin-arm64 /usr/local/opsmonitor/opsmonitor-server
sudo cp ../linux/opsmonitor.env.example /usr/local/etc/opsmonitor/opsmonitor.env
sudo vi /usr/local/etc/opsmonitor/opsmonitor.env     # 改 OPSM_TOKEN / OPSM_ADMIN_PASS
sudo chmod 600 /usr/local/etc/opsmonitor/opsmonitor.env

# 包装脚本：launchd 不读环境文件，必须先 source 再 exec
cat > /usr/local/opsmonitor/start-server.sh <<'EOF'
#!/bin/sh
set -a; [ -f /usr/local/etc/opsmonitor/opsmonitor.env ] && . /usr/local/etc/opsmonitor/opsmonitor.env; set +a
exec /usr/local/opsmonitor/opsmonitor-server
EOF
sudo chmod 755 /usr/local/opsmonitor/start-server.sh

# launchd 单元
sudo tee /Library/LaunchDaemons/com.opsmonitor.server.plist >/dev/null <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.opsmonitor.server</string>
  <key>ProgramArguments</key><array><string>/usr/local/opsmonitor/start-server.sh</string></array>
  <key>WorkingDirectory</key><string>/usr/local/opsmonitor</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/usr/local/opsmonitor/logs/server.log</string>
  <key>StandardErrorPath</key><string>/usr/local/opsmonitor/logs/server.log</string>
</dict></plist>
EOF

sudo launchctl bootstrap system /Library/LaunchDaemons/com.opsmonitor.server.plist
```

## 升级

升级只需替换 `/usr/local/opsmonitor/opsmonitor-server` 这一个二进制（前端已编译在内）。launchd 已配置 `KeepAlive`，进程退出会自动拉起；替换后执行 `sudo launchctl kickstart -k system/com.opsmonitor.server` 即可。

## 排障

| 现象 | 处理 |
|---|---|
| 提示「无法打开，因为 Apple 无法检查其是否包含恶意软件」 | 二进制是自行编译未签名的。执行 `sudo xattr -dr com.apple.quarantine /usr/local/opsmonitor/opsmonitor-server` |
| `launchctl bootstrap` 报 `Bootstrap failed: 5: Input/output error` | 单元文件语法或权限问题：确认属主是 `root:wheel`、权限 644；先 `launchctl bootout` 再重试 |
| 服务反复重启 | 看 `logs/server.log`；常见是端口占用或数据目录权限 |
| 别的机器打不开页面 | macOS 系统设置 → 网络 → 防火墙，确认本程序已允许传入连接 |
| Apple 芯片上误用了 Intel 版 | 报 `Bad CPU type in executable`。换 `opsmonitor-server-darwin-arm64` 重新执行 |
| 启动日志指纹不是 `278D-4852-7191-E7CE` | 二进制不是本版本产物 |
