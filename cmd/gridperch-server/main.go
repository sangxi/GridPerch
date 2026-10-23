// Command gridperch-server 启动监控服务端：负责接收指标、存储并提供 Web 控制台。
package main

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"runtime"
	"strings"

	"github.com/sangxi/gridperch/internal/config"
	"github.com/sangxi/gridperch/internal/license"
	"github.com/sangxi/gridperch/internal/server"
	"github.com/sangxi/gridperch/internal/store"
	"github.com/sangxi/gridperch/internal/version"
)

func main() {
	cfg := config.ParseServer()
	if cfg.PrintVersion {
		fmt.Printf("gridperch-server %s (%s/%s)", version.Version, runtime.GOOS, runtime.GOARCH)
		if version.BuildTime != "" {
			fmt.Printf(" 构建于 %s", version.BuildTime)
		}
		fmt.Println()
		return
	}
	// 更名后默认库文件名从 opsmonitor.db 改成了 gridperch.db。老部署若仍按默认值启动，
	// 这里自动沿用旧文件，避免在旁边新建空库（现象就是「数据全没了」）。
	if p, ok := config.ResolveLegacyDBPath(cfg.DBPath); ok {
		log.Printf("沿用更名前的数据库文件：%s（新默认值为 gridperch.db，需要归位改名后重启即可）", p)
		cfg.DBPath = p
	}
	st, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	log.Printf("gridperch-server %s，控制台账号：%s / %s（首次启动后请在启动参数中修改）", version.Display(version.Version), cfg.AdminUser, cfg.AdminPass)
	// 把内置的全部验签公钥指纹都打出来：授权中心换过密钥对时，靠这一行就能判断
	// 「客户手里的码为什么验不过」——两侧指纹不一致就是钥匙不配对。
	if ver, errs := license.NewVerifierFromStrings(config.LicensePublicKeys()); ver.Ready() {
		for _, e := range errs {
			log.Printf("警告：内置授权验签公钥有条目无法解析：%v", e)
		}
		log.Printf("授权验签公钥指纹：%s（首要公钥，应与授权中心显示的一致%s）",
			ver.Fingerprint(), legacyKeyHint(ver.Fingerprints()))
	} else {
		log.Printf("警告：未内置可用的授权验签公钥，新格式授权码将无法校验")
	}
	// 未显式配置对外地址时提前提醒：安装命令会按「控制台访问地址」反推，
	// 云主机（网卡只有内网 IP）或反向代理后面很容易反推出别的机器连不上的地址。
	if pub := strings.TrimSpace(cfg.PublicURL); pub == "" {
		log.Printf("提示：未设置 -public-url / GRIDPERCH_PUBLIC_URL，采集端安装命令将按控制台访问地址自动生成；" +
			"若服务端在云主机或反向代理后面，请显式设为对外可达地址（如 http://公网IP:9999），否则生成的命令可能指向内网地址")
	} else if publicURLLooksUnreachable(pub) {
		// 比「没设置」更隐蔽的一种坑：设了，但设成了内网/回环地址。
		// 云主机上 deploy/linux/install.sh 未传 --public-url 时会兜底成网卡内网 IP，
		// 生成的命令里就写死了这个地址，目标主机执行必然「无法连接平台」。
		log.Printf("警告：-public-url / GRIDPERCH_PUBLIC_URL 当前是 %q，看起来是内网/回环地址，其它主机访问不到；"+
			"采集端安装命令会原样写死这个地址，目标主机执行必然失败。"+
			"请改成目标主机能访问到的地址（云主机填公网地址或域名，如 https://你的域名）。", pub)
	}
	if err := server.New(cfg, st).Run(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}

// publicURLLooksUnreachable 粗略判断对外地址是否属于「其它主机连不上」的地址：
// 回环、RFC1918 私网、CGNAT（100.64/10）、链路本地。只用于启动时给运维一条醒目的告警，
// 判断从宽（宁可多提醒，不可漏掉云主机内网 IP 这种最常见的踩坑）。
func publicURLLooksUnreachable(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "0.0.0.0" || host == "::" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // 域名：视为可达，不做进一步解析（避免启动时阻塞在 DNS 上）
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	// 100.64.0.0/10（运营商级 NAT）不在 IsPrivate 里，单独判
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// legacyKeyHint 说明除首要公钥外还有几把历史公钥在生效。
// 换过密钥对的授权中心，旧码靠这些历史公钥继续验得过；启动日志里一并打出来，
// 免得看到「首要指纹与授权中心不一致」就以为配错了。
func legacyKeyHint(all []string) string {
	if len(all) <= 1 {
		return ""
	}
	return fmt.Sprintf("；另有 %d 把历史公钥继续生效：%s", len(all)-1, strings.Join(all[1:], "、"))
}
