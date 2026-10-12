package probe

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// 常用 OID，便于在 target 中直接使用别名。
var snmpOIDAliases = map[string]string{
	"sysDescr":    ".1.3.6.1.2.1.1.1.0",
	"sysUpTime":   ".1.3.6.1.2.1.1.3.0",
	"sysName":     ".1.3.6.1.2.1.1.5.0",
	"ifNumber":    ".1.3.6.1.2.1.2.1.0",
	"ifInOctets":  ".1.3.6.1.2.1.2.2.1.10.1",
	"ifOutOctets": ".1.3.6.1.2.1.2.2.1.16.1",
	"cpuUsage":    ".1.3.6.1.4.1.9.2.1.56.0",
	"memUsed":     ".1.3.6.1.4.1.9.2.1.57.0",
	"memFree":     ".1.3.6.1.4.1.9.2.1.8.0",
}

//nolint:gochecknoinits
func init() {
	Register("snmp", probeSNMP)
}

// probeSNMP 通过 SNMP 采集网络设备信息。
//
// target 参数：host、port(默认161)、community(默认public)、version(1|2c|3)、oids(逗号分隔，支持别名)、
// 取到第一个数值型 OID 作为指标值返回。
func probeSNMP(ctx context.Context, t Target, timeout time.Duration) Result {
	host := t.Str("host", t.Str("ip", ""))
	if host == "" {
		return fail("", "缺少参数 host")
	}
	oidsRaw := t.Str("oids", "sysUpTime,sysDescr")
	var oids []string
	for _, o := range strings.Split(oidsRaw, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if alias, ok := snmpOIDAliases[o]; ok {
			o = alias
		}
		if !strings.HasPrefix(o, ".") {
			o = "." + o
		}
		oids = append(oids, o)
	}
	if len(oids) == 0 {
		oids = []string{snmpOIDAliases["sysUpTime"]}
	}
	params, err := buildSNMPParams(t, host, timeout)
	if err != nil {
		return fail("", err.Error())
	}
	type snmpResp struct {
		packet *gosnmp.SnmpPacket
		err    error
	}
	ch := make(chan snmpResp, 1)
	go func() {
		connectErr := params.Connect()
		if connectErr != nil {
			ch <- snmpResp{nil, connectErr}
			return
		}
		defer params.Conn.Close()
		pkt, getErr := params.Get(oids)
		ch <- snmpResp{pkt, getErr}
	}()
	var packet *gosnmp.SnmpPacket
	select {
	case r := <-ch:
		if r.err != nil {
			return fail("", fmt.Sprintf("SNMP 查询 %s 失败: %v", host, r.err))
		}
		packet = r.packet
	case <-ctx.Done():
		return fail("", fmt.Sprintf("SNMP 查询 %s 超时", host))
	}
	if packet.Error != gosnmp.NoError {
		return fail("", fmt.Sprintf("设备返回错误 %v(%s)", packet.Error, packet.Error.String()))
	}
	var value string
	var desc []string
	for _, v := range packet.Variables {
		text := snmpValueString(v)
		if value == "" {
			if n, ok := numericOf(text); ok {
				value = n
			}
		}
		name := v.Name
		for alias, oid := range snmpOIDAliases {
			if oid == v.Name {
				name = alias
				break
			}
		}
		desc = append(desc, fmt.Sprintf("%s=%s", name, truncate(text, 40)))
	}
	if len(desc) == 0 {
		return fail("", "SNMP 未返回任何 OID 数据")
	}
	if value == "" {
		value = "1"
	}
	return ok(value, fmt.Sprintf("SNMP %s 正常，%s", host, strings.Join(desc, "，")))
}

// buildSNMPParams 依据 target 构造 SNMP 会话参数，支持 v1/v2c/v3。
func buildSNMPParams(t Target, host string, timeout time.Duration) (*gosnmp.GoSNMP, error) {
	version := strings.ToLower(t.Str("version", "2c"))
	params := &gosnmp.GoSNMP{
		Target:  host,
		Port:    uint16(t.Int("port", 161)),
		Timeout: timeout,
		Retries: 1,
		MaxOids: 8,
	}
	switch version {
	case "1":
		params.Version = gosnmp.Version1
	case "2c", "v2c", "2":
		params.Version = gosnmp.Version2c
	case "3", "v3":
		params.Version = gosnmp.Version3
		params.SecurityModel = gosnmp.UserSecurityModel
		params.MsgFlags = gosnmp.NoAuthNoPriv
		if params.SecurityModel == gosnmp.UserSecurityModel {
			params.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: t.Str("username", t.Str("community", "public"))}
		}
	default:
		return nil, fmt.Errorf("不支持的 SNMP 版本: %s", version)
	}
	if params.Version != gosnmp.Version3 {
		params.Community = t.Str("community", "public")
	}
	return params, nil
}

// snmpValueString 将返回的变量转为可读字符串。
func snmpValueString(v gosnmp.SnmpPDU) string {
	switch v.Type {
	case gosnmp.OctetString, gosnmp.BitString:
		if b, ok := v.Value.([]byte); ok {
			return strings.TrimSpace(string(b))
		}
	case gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32, gosnmp.Integer, gosnmp.Uinteger32, gosnmp.TimeTicks:
		return fmt.Sprintf("%d", gosnmp.ToBigInt(v.Value).Int64())
	}
	return fmt.Sprintf("%v", v.Value)
}

// numericOf 提取字符串中的首个数值。
func numericOf(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(n, 'f', 2, 64), true
	}
	return "", false
}
