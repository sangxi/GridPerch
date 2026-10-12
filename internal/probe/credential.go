package probe

import (
	"fmt"
	"strconv"

	"github.com/sangxi/OpsMonitor/internal/model"
)

// DeviceProbeType 把「设备账号」的协议映射到已有的探测实现。
// rdp 只做端口可达性判断（完整握手需专用客户端），web 复用 HTTP 接口探测。
var DeviceProbeType = map[string]string{
	"ssh":   "ssh",
	"sftp":  "sftp",
	"ftp":   "ftp",
	"mysql": "mysql",
	"redis": "redis",
	"snmp":  "snmp",
	"ipmi":  "ipmi",
	"rdp":   "tcp",
	"web":   "httpapi",
	"tcp":   "tcp",
}

// DeviceDefaultPort 各协议的默认端口，未显式填写端口时使用。
var DeviceDefaultPort = map[string]int{
	"ssh": 22, "sftp": 22, "ftp": 21, "rdp": 3389, "mysql": 3306,
	"redis": 6379, "snmp": 161, "ipmi": 623, "web": 80, "tcp": 0,
}

// CredentialTarget 把设备账号转换为探测参数表。
//
// 探测实现里的凭据字段名并不统一（ssh/mysql 读 username，httpapi 读 user），
// 因此这里同时写入 username/user/password/secret，保证任意协议都能取到口令。
// 该函数被「设备账号连通性检测」与「监控项引用 deviceId」两条链路共同复用。
func CredentialTarget(a *model.DeviceAccount) map[string]string {
	t := map[string]string{}
	if a == nil {
		return t
	}
	if a.Host != "" {
		t["host"] = a.Host
	}
	port := a.Port
	if port <= 0 {
		port = DeviceDefaultPort[a.Protocol]
	}
	if port > 0 {
		t["port"] = strconv.Itoa(port)
	}
	if a.Username != "" {
		t["username"] = a.Username
		t["user"] = a.Username
	}
	if a.Secret != "" {
		t["password"] = a.Secret
		t["secret"] = a.Secret
	}
	// SNMP v2c 场景下设备账号的「用户名」通常填的是团体名
	if a.Protocol == "snmp" && a.Username != "" {
		if _, ok := t["community"]; !ok {
			t["community"] = a.Username
		}
	}
	if a.Protocol == "web" && a.Host != "" && port != 80 {
		scheme := "http"
		if port == 443 {
			scheme = "https"
		}
		t["url"] = fmt.Sprintf("%s://%s:%d/", scheme, a.Host, port)
	}
	return t
}
