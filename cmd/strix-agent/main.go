// Command strix-agent 启动采集端：周期性采集本机指标并上报服务端。
package main

import (
	"github.com/sangxi/strix/internal/agent"
	"github.com/sangxi/strix/internal/config"
)

func main() {
	agent.Run(config.ParseAgent())
}
