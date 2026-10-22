// 供 Go 1.20 编译 Windows 旧系统（Windows 7 SP1 / Server 2008 R2）采集端用的替代 go.mod。
//
// ── 为什么需要它 ────────────────────────────────────────────────────────
// Go 1.21.5 起，runtime 的随机数源从 RtlGenRandom（advapi32!SystemFunction036，
// XP 起就有）换成了 ProcessPrng（bcryptprimitives.dll，只有 Windows 8 /
// Server 2012 及以上才存在）。在 Windows 7 / Server 2008 R2 上加载失败，
// 拿到的函数指针是 NULL，runtime 初始化时一调用就 0xc0000005 退出 ——
// 现象是双击闪退、既无报错框也无日志。官方最后一个支持并在 Server 2008 R2
// 上实测过的工具链是 Go 1.20。
//
// 而 go.mod 声明的是 go 1.23.9，Go 1.20 不接受更高的 go 指令，所以要单独给一份。
//
// ── 为什么删掉了 sqlite / mysql ─────────────────────────────────────────
// 采集端不依赖它们（internal/agent 的依赖图里没有 internal/store、
// internal/probe），而 modernc.org/sqlite v1.34.1 自己声明 go 1.21，
// 留着会让 Go 1.20 读 module graph 时直接报错。
//
// ── 为什么把间接依赖也逐条锁死版本 ──────────────────────────────────────
// 只写直接依赖时，Go 1.20 的最小版本选择会挑出更老的间接依赖（实测挑到了
// golang.org/x/sys v0.20.0、2021 年的 x/net 快照、uuid v1.2.0），与正式产物
// 用的版本不一致。这里全部对齐主 go.mod 的版本，让两个产物的差异只来自
// Go 工具链本身。所锁版本均声明 go <= 1.20，Go 1.20 才能加载。
//
// ── 用法 ────────────────────────────────────────────────────────────────
// go build -modfile=go120.mod -mod=mod ./cmd/gridperch-agent
// 或直接跑 ./build-win7.sh
module github.com/sangxi/gridperch

go 1.20

require (
	github.com/go-ping/ping v1.1.0
	github.com/shirou/gopsutil/v3 v3.24.5
	golang.org/x/text v0.19.0
)

require (
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
)
