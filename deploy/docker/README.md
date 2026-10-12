# 监控端（server）· Docker 本地部署

## 快速开始

```bash
cd deploy/docker
cp .env.example .env
vi .env                     # 改 OPSM_ADMIN_PASS、OPSM_TOKEN、OPSM_PUBLIC_URL
docker compose up -d
docker compose logs -f
```

打开 `http://宿主机IP:9999`，用 `.env` 里的账号登录。

两种构建方式（`docker-compose.yml` 里默认第一种）：

| 方式 | Dockerfile | 特点 |
|---|---|---|
| 用现成二进制 | `Dockerfile.binary` | 不用 Go 环境、不用联网拉模块，几十秒出镜像。依赖仓库里已有 `release/1.1.0/opsmonitor-server-linux-amd64` |
| 从源码构建 | `Dockerfile` | 适合 GitHub 用户 / CI，需要能拉取 Go 模块 |

> 构建上下文必须是**仓库根目录**（compose 里已配好 `context: ../..`），因为 `Dockerfile.binary`
> 需要读取 `release/<版本>/` 下的二进制。

## ⚠️ 最容易踩的坑：机器码会变，授权会失效

监控端的授权是**绑定机器码**的，而容器重建后主机名、网卡信息都会变，机器码跟着变 → 授权判定失效。

**正确做法**：

1. 先起一次容器；
2. 打开控制台「系统 → 授权购买」，复制页面显示的**本机机器码**（形如 `C317-6E8B-BAE5-A8A6`）；
3. 把它填到 `.env` 的 `OPSM_LICENSE_MACHINE=`；
4. `docker compose up -d` 重建。

之后无论容器怎么重建，机器码都是固定的，授权不会掉。

## 目录与数据

| 位置 | 用途 |
|---|---|
| `./data/` | SQLite 数据库（挂到容器 `/data`） |
| `./releases/` | 从授权系统代拉的采集端发布物（挂到 `/releases`） |
| 容器 `/opt/opsmonitor` | 工作目录 |

数据都在宿主机 `./data/`，`docker compose down` 不会丢。备份直接打包这个目录即可。

## 采集端如何接入

采集端要能访问到**宿主机的 9999**（不是容器 IP）。两种方式：

```bash
# 方式一：采集端装在宿主机 / 其它机器上，直接指宿主机地址
opsmonitor-agent -server http://10.0.0.5:9999 -token 与OPSM_TOKEN一致 -name web-01

# 方式二：控制台「监控 → 添加主机」复制一键安装命令，命令里自动带上平台地址
```

想用容器采集宿主机指标，看 `DEPLOY.md §5.3`（需要 `pid: host`、`network_mode: host`、只读挂载 `/`）。

## 反向代理（可选，推荐生产用）

```nginx
server {
    listen 80;
    server_name monitor.example.com;

    location / {
        proxy_pass http://127.0.0.1:9999;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;   # 探测任务与升级下载可能较慢
        client_max_body_size 200m; # 上传采集端发布物
    }
}
```

挂了域名 https 之后，记得把 `.env` 的 `OPSM_PUBLIC_URL` 改成对外地址，
否则「一键安装采集端」生成的命令会指到内网 IP。

## 常用命令

```bash
docker compose ps                      # 状态
docker compose logs -f --tail=100      # 日志
docker compose restart                 # 重启
docker compose down                    # 停止（保留数据）
docker compose up -d --build           # 改完 .env 后重建
docker compose exec opsmonitor wget -qO- http://127.0.0.1:9999/api/health   # 容器内健康检查
```

## 升级

```bash
# 1) 把新版本二进制放到 release/<新版本>/ 下
# 2) 改 docker-compose.yml 与 .env.example 里的 VERSION 与 image tag
# 3) 重建
docker compose up -d --build
```

## 排障

| 现象 | 处理 |
|---|---|
| `docker compose up` 报找不到 `release/1.1.0/...` | 构建上下文不对。必须在仓库根目录执行，或 `docker compose -f deploy/docker/docker-compose.yml --project-directory . build` |
| 容器起来就退出 | `docker compose logs` 看日志；常见是 `/data` 挂载点权限或端口占用 |
| 页面能开、别的机器打不开 | 宿主机防火墙 / 云安全组没放行 9999 |
| 授权显示社区版且「等待签发」 | 先确认 `.env` 里 `OPSM_LICENSE_MACHINE` 与授权中心签发时绑定的机器码一致；再点「立即从授权系统拉取授权」 |
| 容器内访问授权中心超时 | 容器需要出网访问 `https://1999091.xyz`；内网离线环境请用启动参数 `-hub=` 完全离线并手工导入授权码 |
| 时间不对（日志/告警时间差 8 小时） | 确认 `TZ=Asia/Shanghai` 已传入，且宿主机时间正确 |
