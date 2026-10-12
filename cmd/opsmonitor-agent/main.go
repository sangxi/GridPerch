// Command opsmonitor-agent 启动采集端：周期性采集本机指标并上报服务端。
package main

import (
	"github.com/sangxi/OpsMonitor/internal/agent"
	"github.com/sangxi/OpsMonitor/internal/config"
)

func main() {
	agent.Run(config.ParseAgent())
}
