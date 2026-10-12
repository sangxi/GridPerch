# 监控端（server）· Linux 部署

## 一句话结论：主流发行版**全都兼容**

本程序是 `CGO_ENABLED=0` 编译的**纯静态链接** Go 二进制。实测校验（`readelf` 等价检查）：

```
program headers: LOAD / NOTE / PHDR / GNU_STACK      <- 没有 PT_INTERP、没有 DYNAMIC
PT_INTERP 存在? False                                  <- 静态链接
引用 ld-linux? 0    引用 libc.so? 0    引用 ld-musl? 0
```

也就是说它**不依赖 glibc 版本、不依赖任何系统库**，所以：

| 发行版 | 版本 | 架构 | 支持 |
|---|---|---|---|
| Debian | 10 / 11 / 12 及以上 | amd64 / arm64 | ✅ |
| Ubuntu | 18.04 / 20.04 / 22.04 / 24.04 | amd64 / arm64 | ✅ |
| CentOS | 7 / 8 / Stream | amd64 / arm64 | ✅ （CentOS 7 自带 glibc 2.17 也没问题） |
| RHEL / Rocky / AlmaLinux | 7 / 8 / 9 | amd64 / arm64 | ✅ |
| Fedora | 最新 | amd64 / arm64 | ✅ |
| openEuler / 欧拉 | 20.03 / 22.03 | amd64 / aarch64 | ✅ |
| 麒麟 Kylin / 统信 UOS | V10 / 20 | amd64 / arm64 | ✅ |
| Alpine | 3.x | amd64 / arm64 | ✅ （musl 也不依赖） |
| SUSE / openSUSE | 15+ | amd64 / arm64 | ✅ |

**唯一会因发行版不同的，是「怎么把它注册成开机自启的服务」和「怎么放行端口」**，
这两件事 `install.sh` 已经自动适配，不需要你改脚本：

| 发行版 | 初始化系统 | 端口放行 | 脚本行为 |
|---|---|---|---|
| Debian / Ubuntu / UOS / Kylin | systemd | ufw | 装 systemd 单元 + `ufw allow` |
| CentOS / RHEL / Rocky / Alma / openEuler | systemd | firewalld | 装 systemd 单元 + `firewall-cmd`；检测到 SELinux Enforcing 会 `restorecon` |
| Alpine | OpenRC | 无 | 生成 `/etc/init.d/opsmonitor` + `rc-update` |
| 极小化容器镜像 | 都没有 | 无 | 回退 `nohup` 常驻（并提示重启不自启） |

## 快速开始

```bash
# 1) 把二进制和本目录一起传到服务器
scp -r deploy/linux opsmonitor-server-linux-amd64 root@10.0.0.5:/root/

# 2) 在服务器上执行
cd /root && chmod +x linux/install.sh
sudo ./linux/install.sh
```

脚本会自动：探测发行版/架构 → 取出对应二进制 → 建低权限用户 `opsmonitor` →
装到 `/opt/opsmonitor/opsmonitor-server` → 生成 `/etc/opsmonitor/opsmonitor.env` →
注册开机自启 → 放行端口 → 打印**只显示一次**的控制台账号与通信密钥。

## 常用参数

```bash
sudo ./install.sh --addr :8000                    # 换监听端口
sudo ./install.sh --token '你的通信密钥'            # 指定采集端要用的密钥
sudo ./install.sh --admin-pass '你的控制台密码'      # 指定控制台密码（否则随机生成）
sudo ./install.sh --public-url http://10.0.0.5:9999
sudo ./install.sh --no-firewall                   # 不动防火墙（自己管理）
sudo ./install.sh -f /tmp/opsmonitor-server-linux-arm64   # 指定二进制
sudo ./install.sh -u https://example.com/opsmonitor-server-linux-amd64  # 在线拉取
sudo ./install.sh --uninstall                     # 卸载（保留数据与配置）
```

参数也可以通过环境变量给：`OPSM_VERSION` / `OPSM_PREFIX`。

## 目录规划

| 路径 | 用途 |
|---|---|
| `/opt/opsmonitor/opsmonitor-server` | 主程序（自动更新会替换它，旧版存为 `.bak`） |
| `/opt/opsmonitor/data/opsmonitor.db` | SQLite 数据库（WAL 模式，备份拷这个目录即可） |
| `/opt/opsmonitor/releases/` | 采集端发布物缓存（内网主机从这里升级） |
| `/etc/opsmonitor/opsmonitor.env` | 配置（权限 600，密码/密钥都在这里） |
| `/etc/systemd/system/opsmonitor.service` | systemd 单元 |

## 手工部署（不想用脚本时）

```bash
sudo useradd -r -s /sbin/nologin opsmonitor
sudo mkdir -p /opt/opsmonitor/{data,releases} /etc/opsmonitor
sudo install -m0755 opsmonitor-server-linux-amd64 /opt/opsmonitor/opsmonitor-server

sudo cp opsmonitor.env.example /etc/opsmonitor/opsmonitor.env
sudo vi /etc/opsmonitor/opsmonitor.env      # 至少改 OPSM_TOKEN 与 OPSM_ADMIN_PASS
sudo chmod 600 /etc/opsmonitor/opsmonitor.env

sudo cp opsmonitor.service /etc/systemd/system/
sudo chown -R opsmonitor:opsmonitor /opt/opsmonitor
sudo systemctl daemon-reload && sudo systemctl enable --now opsmonitor
sudo systemctl status opsmonitor --no-pager
```

## 排障

| 现象 | 原因与处理 |
|---|---|
| `journalctl -u opsmonitor` 里 `avc: denied` | CentOS/RHEL 的 SELinux。执行 `sudo semanage fcontext -a -t bin_t '/opt/opsmonitor(/.*)?' && sudo restorecon -R /opt/opsmonitor`，再 `systemctl restart opsmonitor` |
| `permission denied` 启动失败 | 数据目录属主不对：`sudo chown -R opsmonitor:opsmonitor /opt/opsmonitor` |
| 本机能开页面、外面打不开 | firewalld/ufw 没放行，或云服务器**安全组**没放行 9999 |
| `address already in use` | `ss -tlnp \| grep 9999` 找占用进程；或换 `--addr :8000` |
| 服务起来但页面报错、日志有 `打开数据库失败` | `OPSM_DB` 指向的目录不存在或无写权限 |
| 启动日志指纹不是 `278D-4852-7191-E7CE` | 二进制不是本版本产物（旧版内置错误公钥，授权码会全部验不过） |
| CentOS 7 上 systemd 报 `Unknown lvalue LimitNOFILE` | 老 systemd 语法差异，删掉该行即可（非必需） |
| 想完全离线（不连授权中心） | 单元里 `ExecStart` 改成 `/opt/opsmonitor/opsmonitor-server -hub=`。注意**不能**用 `OPSM_HUB=` 环境变量——空串会回落到默认地址 |

## 采集端

监控端跑起来后，在控制台「监控 → 添加主机」复制一键安装命令到目标主机执行即可。
采集端同样支持上面这些发行版（同一套静态二进制）。
