// Command opsmonitor-server 启动监控服务端：负责接收指标、存储并提供 Web 控制台。
package main

import (
	"fmt"
	"log"
	"runtime"
	"strings"

	"github.com/sangxi/OpsMonitor/internal/config"
	"github.com/sangxi/OpsMonitor/internal/license"
	"github.com/sangxi/OpsMonitor/internal/server"
	"github.com/sangxi/OpsMonitor/internal/store"
	"github.com/sangxi/OpsMonitor/internal/version"
)

func main() {
	cfg := config.ParseServer()
	if cfg.PrintVersion {
		fmt.Printf("opsmonitor-server %s (%s/%s)", version.Version, runtime.GOOS, runtime.GOARCH)
		if version.BuildTime != "" {
			fmt.Printf(" 构建于 %s", version.BuildTime)
		}
		fmt.Println()
		return
	}
	st, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	log.Printf("opsmonitor-server %s，控制台账号：%s / %s（首次启动后请在启动参数中修改）", version.Display(version.Version), cfg.AdminUser, cfg.AdminPass)
	if pub, err := license.ParsePublicKey(config.LicensePublicKey); err == nil {
		log.Printf("授权验签公钥指纹：%s（应与授权中心启动日志一致）", license.Fingerprint(pub))
	} else {
		log.Printf("警告：内置授权验签公钥无法解析：%v", err)
	}
	// 未显式配置对外地址时提前提醒：安装命令会按「控制台访问地址」反推，
	// 云主机（网卡只有内网 IP）或反向代理后面很容易反推出别的机器连不上的地址。
	if strings.TrimSpace(cfg.PublicURL) == "" {
		log.Printf("提示：未设置 -public-url / OPSM_PUBLIC_URL，采集端安装命令将按控制台访问地址自动生成；" +
			"若服务端在云主机或反向代理后面，请显式设为对外可达地址（如 http://公网IP:9999），否则生成的命令可能指向内网地址")
	}
	if err := server.New(cfg, st).Run(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
