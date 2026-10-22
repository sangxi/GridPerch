package agent

import (
	"encoding/json"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
	psnet "github.com/shirou/gopsutil/v3/net"
)

// CollectHardware 采集硬件与运行时扩展指标：CPU 温度、硬盘 SMART、
// TCP 连接数、MAC 地址与对外 IP。
//
// 这一类指标在虚拟化与云主机上常常取不到（没有温度传感器、SMART 需要
// root、云盘不暴露 SMART），因此全部按「取不到就留空」处理，
// 绝不让一个可选指标拖垮整轮采集。
func CollectHardware(hostname string) *model.HostExtra {
	ext := &model.HostExtra{Hostname: hostname, CollectTime: time.Now().Format("2006-01-02 15:04:05")}

	ext.CpuTemp = collectCPUTemp()
	ext.SmartStatus = collectSmart()
	ext.ConnCount, ext.ConnEstablished = collectConns()
	ext.MacAddr, ext.IpAddr = collectAddr()

	if ext.CpuTemp <= 0 && ext.SmartStatus == "" && ext.ConnCount == 0 && ext.MacAddr == "" {
		return nil
	}
	return ext
}

// collectCPUTemp 返回 CPU 温度（摄氏度）。
//
// 温度来源两条路：Linux 下读 hwmon（内核已把主板 SuperIO 芯片
// 暴露成 /sys/class/hwmon 下的温度通道），这是最通用也最轻量的方式；
// 拿不到时回退到 lm-sensors 的 sensors 命令。取不到返回 0。
func collectCPUTemp() float64 {
	var maxTemp float64

	// Linux：遍历 hwmon 下的温度通道，只认名字带 cpu/core/package 的
	if entries, err := readHwmonTemps(); err == nil {
		maxTemp = entries
	}

	if maxTemp > 0 {
		return roundFloat(maxTemp)
	}

	// 回退：lm-sensors
	if runtime.GOOS == "linux" {
		if _, out, err := runCommand("sensors -u 2>/dev/null | head -200", 5*time.Second); err == nil {
			// 输出形如 "temp1_input: 42.000" 或 "coretemp_package_id_0: 45.000"
			for _, line := range strings.Split(out, "\n") {
				l := strings.ToLower(line)
				if !strings.Contains(l, "temp") && !strings.Contains(l, "core") &&
					!strings.Contains(l, "package") && !strings.Contains(l, "tdie") {
					continue
				}
				if v, ok := lastFloatAfterColon(line); ok && v > maxTemp {
					maxTemp = v
				}
			}
		}
	}
	return roundFloat(maxTemp)
}

// lastFloatAfterColon 从 "key: 42.5" 中解析出最后一个浮点数。
func lastFloatAfterColon(line string) (float64, bool) {
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(line[i+1:]))
	if len(fields) == 0 {
		return 0, false
	}
	var f float64
	if _, err := fmtSscan(fields[0], &f); err != nil {
		return 0, false
	}
	return f, true
}

// collectSmart 返回硬盘 SMART 健康度：pass / warn / fail，取不到返回空串。
//
// SMART 需要 smartmontools 且通常要 root，容器与云主机上不可用是常态，
// 因此这里的失败静默处理，由前端显示「未知」而不是「异常」。
func collectSmart() string {
	if runtime.GOOS == "windows" {
		// Windows 下 gopsutil 未提供跨平台 SMART 读取，直接标记为未知
		return ""
	}
	if _, err := exec.LookPath("smartctl"); err != nil {
		return ""
	}
	_, out, err := runCommand("smartctl -H -j /dev/sda 2>/dev/null || smartctl -H -j /dev/nvme0 2>/dev/null", 15*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	return parseSmartJSON(out)
}

// parseSmartJSON 从 smartctl -j 的输出里解析整体健康状态。
//
// 只取 nvme_smart_health 或 smart_status.passed 两个字段，
// 其他格式（逐属性判断）不展开——大屏只需要一个总体结论。
func parseSmartJSON(raw string) string {
	var doc struct {
		SmartStatus struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		NVMeHealth struct {
			Value string `json:"value"`
		} `json:"nvme_smart_health"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}
	if v := strings.ToLower(strings.TrimSpace(doc.NVMeHealth.Value)); v != "" {
		switch {
		case strings.Contains(v, "passed") || strings.Contains(v, "healthy"):
			return "pass"
		default:
			return "fail"
		}
	}
	if doc.SmartStatus.Passed {
		return "pass"
	}
	// smart_status 存在但 passed=false 才判失败；结构体为零值时无法区分，
	// 这里检查原始文本里是否出现了明确的失败关键字。
	low := strings.ToLower(raw)
	if strings.Contains(low, "\"passed\": false") || strings.Contains(low, "\"passed\":false") {
		return "fail"
	}
	return ""
}

// collectConns 返回 TCP 连接总数与已建立数。
func collectConns() (total int, established int) {
	conns, err := psnet.Connections("tcp")
	if err != nil {
		return 0, 0
	}
	for _, c := range conns {
		total++
		if strings.EqualFold(c.Status, "ESTABLISHED") {
			established++
		}
	}
	return total, established
}

// collectAddr 返回主网卡 MAC 与对外 IPv4。
func collectAddr() (mac string, ip string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	var candidates []net.Interface
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		candidates = append(candidates, ifc)
	}
	// 优先选有 MAC 的第一块网卡作为「主网卡」
	for _, ifc := range candidates {
		if len(ifc.HardwareAddr) > 0 {
			mac = strings.ToLower(ifc.HardwareAddr.String())
			break
		}
	}
	for _, ifc := range candidates {
		addrs, aerr := ifc.Addrs()
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			return mac, v4.String()
		}
	}
	return mac, ""
}
