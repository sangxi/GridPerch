# deploy/ · 监控端部署文件

> 📖 **图文部署教程**：独立维护的静态站点（选型 / 四平台步骤 / 兼容矩阵 / 排障手册），
> 可离线打开或托管为在线文档；本站点不在本仓库内，单独存放。

本目录只放**监控端（server）**的部署文件，按平台分开，互不影响。
采集端不在这里——采集端由监控端控制台的「添加主机」一键下发安装（见 `DEPLOY.md §5`）。

## 四套部署方式

| 目录 | 平台 | 注册方式 | 适用 |
|---|---|---|---|
| [`linux/`](linux/) | Linux 全发行版（Debian / Ubuntu / CentOS / RHEL / openEuler / Alpine / 麒麟 / 统信…） | systemd / OpenRC / nohup | 服务器主流选择 |
| [`windows/`](windows/) | Windows 10 / 11 / Server 2012 R2+ | 计划任务（SYSTEM 权限） | 内网 Windows 服务器 |
| [`macos/`](macos/) | macOS 11+（Intel 与 Apple 芯片） | launchd | 开发机 / 办公 Mac |
| [`docker/`](docker/) | 任何装了 Docker 的机器 | Docker Compose | 最快跑起来、1Panel/宝塔面板环境 |

每个目录下都有独立的 `README.md`，包含快速开始、手工部署、目录规划与排障。

## 选哪个？

- **只是想尽快看到界面** → `docker/`，两条命令起来。
- **正式部署到 Linux 服务器** → `linux/`，脚本会自适应发行版。
- **Windows / macOS 单机** → 对应目录。

## 关于「Linux 发行版兼容性」

**不用担心 glibc 版本，也不用为不同发行版准备不同文件。**
所有 Linux 产物都是 `CGO_ENABLED=0` 编译的**纯静态链接**二进制：

```
program headers: LOAD / NOTE / PHDR / GNU_STACK     <- 没有 PT_INTERP、没有 DYNAMIC
引用 ld-linux? 0   引用 libc.so? 0   引用 ld-musl? 0
```

所以 Debian 10/11/12、Ubuntu 18.04–24.04、CentOS 7/8、RHEL 7/8/9、Rocky、AlmaLinux、
Fedora、openEuler、麒麟、统信、Alpine 都能直接跑，唯一的差异是「服务怎么注册、端口怎么放行」，
`linux/install.sh` 已自动适配这几种情况。

CPU 架构只需要选对文件：

| 机器 | 文件 |
|---|---|
| x86_64 / amd64（绝大多数服务器） | `strix-server-linux-amd64` |
| aarch64 / arm64（鲲鹏、飞腾、树莓派、Apple 芯片上的 Linux 虚拟机） | `strix-server-linux-arm64` |

## 部署前先确认二进制来源

二进制在仓库的 `release/<版本>/` 下（例如 `release/1.2.2/`），文件名形如
`strix-server-{linux,windows,darwin}-{amd64,arm64}[.exe]`。

各平台的 `install.sh` / `install.ps1` 会自己往上找这些目录，通常不用手动指定路径；
找不到时用 `-f <路径>`（Windows 用 `-File <路径>`）明确指定即可。

部署完成后，**启动日志里应出现**：

```
授权验签公钥指纹：278D-4852-7191-E7CE
```

不是这个值说明二进制不是当前版本（旧版本曾内置错误的验签公钥，会导致授权码全部校验失败）。

## 需要更完整的方案

`DEPLOY.md` 里有容量规划、高可用说明、Nginx/1Panel/宝塔接入、备份恢复、升级回滚、
安全加固与排障手册；本目录是它的**可执行版本**。
