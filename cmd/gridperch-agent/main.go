// Command gridperch-agent 启动采集端：周期性采集本机指标并上报服务端。
package main

import (
	"github.com/sangxi/gridperch/internal/agent"
	"github.com/sangxi/gridperch/internal/config"
)

func main() {
	agent.Run(config.ParseAgent())
}
