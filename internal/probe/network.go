package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-ping/ping"
	"golang.org/x/crypto/ssh"
	"golang.org/x/text/encoding/simplifiedchinese"
)

//nolint:gochecknoinits // 探测类型在包初始化时注册，便于集中维护类型清单
func init() {
	Register("icmp", probeICMP)
	Register("tcp", probeTCP)
	Register("dns", probeDNS)
	Register("httpapi", probeHTTPAPI)
	Register("ftp", probeFTP)
	Register("ssh", probeSSH)
	Register("sftp", probeSFTP)
	Register("ipmi", probeIPMI)
}

// probeICMP 通过 ICMP 探测网络设备可达性与丢包率、时延。
//
// 优先用 go-ping（raw socket）。但 raw socket 在部分环境不可用或时好时坏：
// Windows 非管理员、Linux 容器未授予 NET_RAW 时，创建 socket 会随机报
// "socket: The requested protocol has not been configured into the system"。
// 因此库方式一旦出错就回退到系统 ping 命令（普通权限即可），保证监控结果稳定。
func probeICMP(ctx context.Context, t Target, timeout time.Duration) Result {
	host := t.Str("host", t.Str("ip", ""))
	if host == "" {
		return fail("", "缺少参数 host")
	}
	count := t.Int("count", 3)
	if count < 1 {
		count = 1
	}
	if r, usable := pingLibrary(ctx, host, count, timeout); usable {
		return r
	}
	return pingByCommand(ctx, host, count, timeout)
}

// pingLibrary 用 go-ping 探测；返回 usable=false 表示库不可用（应回退命令方式）。
// 仅当「socket 层出错」才回退；正常跑完（哪怕 100% 丢包）都算可用，避免重复探测。
func pingLibrary(ctx context.Context, host string, count int, timeout time.Duration) (Result, bool) {
	run := func(privileged bool) (*ping.Statistics, error) {
		p, err := ping.NewPinger(host)
		if err != nil {
			return nil, err
		}
		p.Count = count
		p.Timeout = timeout
		p.SetPrivileged(privileged)
		done := make(chan error, 1)
		go func() { done <- p.Run() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			p.Stop()
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		return p.Statistics(), nil
	}
	stats, err := run(false)
	if err != nil && ctx.Err() == nil {
		// 非特权失败时再试特权模式（需要 root/管理员）
		stats, err = run(true)
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail("100", fmt.Sprintf("PING %s 超时", host)), true
		}
		return Result{}, false
	}
	return summarizePing(stats, count), true
}

var (
	pingLossRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%\s*(?:packet\s+loss|loss|丢失)`)
	// Linux/macOS: rtt min/avg/max/mdev = 0.1/0.2/0.3/0.1 ms
	pingRTTUnixRe = regexp.MustCompile(`(?:rtt|round-trip)\s+min/avg/max\S*\s*=\s*[\d.]+/([\d.]+)/`)
	// Windows 中文/英文: 平均 = 1ms / Average = 1ms
	pingRTTWinRe = regexp.MustCompile(`(?:平均|Average)\s*=\s*([\d.]+)\s*ms`)
)

// decodePingOutput 把 ping 的原始输出转成 UTF-8。
// 中文 Windows 控制台输出为 GBK，不转码的话中文标签「平均/丢失」匹配不上，
// 会导致时延解析不出、丢包率靠兜底值。GBK 兼容 ASCII，纯 ASCII 输入不受影响。
func decodePingOutput(raw []byte) string {
	if runtime.GOOS != "windows" {
		return string(raw)
	}
	if s, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw); err == nil {
		return string(s)
	}
	return string(raw)
}

// pingByCommand 调用系统 ping 命令探测，作为 raw socket 不可用时的兜底实现。
func pingByCommand(ctx context.Context, host string, count int, timeout time.Duration) Result {
	per := max(1, int(timeout.Milliseconds())/count)
	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"-n", strconv.Itoa(count), "-w", strconv.Itoa(per), host}
	} else {
		args = []string{"-c", strconv.Itoa(count), "-W", strconv.Itoa(max(1, per/1000)), host}
	}
	out, err := exec.CommandContext(ctx, "ping", args...).CombinedOutput()
	text := strings.TrimSpace(decodePingOutput(out))
	if text == "" {
		return fail("", fmt.Sprintf("PING %s 失败: %v", host, err))
	}
	loss := "100"
	if m := pingLossRe.FindStringSubmatch(text); m != nil {
		loss = m[1]
	} else if err == nil {
		loss = "0" // 命令成功但未解析到丢包率（非中英文环境），按无丢包处理
	}
	rtt := ""
	if m := pingRTTUnixRe.FindStringSubmatch(text); m != nil {
		rtt = m[1]
	} else if m := pingRTTWinRe.FindStringSubmatch(text); m != nil {
		rtt = m[1]
	}
	suffix := fmt.Sprintf("丢包率 %s%%", loss)
	if rtt != "" {
		suffix = fmt.Sprintf("平均时延 %sms，%s", rtt, suffix)
	}
	if err == nil {
		return ok(loss, suffix)
	}
	if ctx.Err() != nil {
		return fail("100", fmt.Sprintf("PING %s 超时", host))
	}
	return fail(loss, fmt.Sprintf("%s 不可达，%s", host, suffix))
}

func summarizePing(stats *ping.Statistics, count int) Result {
	if stats.PacketsRecv == 0 {
		return fail("100", fmt.Sprintf("%s 不可达，%d 个包全部丢失", stats.Addr, count))
	}
	loss := stats.PacketLoss
	value := fmt.Sprintf("%.0f", loss)
	msg := fmt.Sprintf("平均时延 %.2fms，丢包率 %.0f%%", float64(stats.AvgRtt)/float64(time.Millisecond), loss)
	if loss >= 100 {
		return fail(value, msg)
	}
	if loss > 0 {
		return Result{Status: StatusOK, Value: value, Msg: msg + "（存在丢包）"}
	}
	return ok(value, msg)
}

// probeTCP 检测 TCP 端口连通性。
func probeTCP(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr("80")
	if addr == ":" || strings.HasPrefix(addr, ":") {
		return fail("", "缺少参数 host")
	}
	start := time.Now()
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fail("", fmt.Sprintf("连接 %s 失败: %v", addr, err))
	}
	defer conn.Close()
	cost := time.Since(start)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return ok(fmt.Sprintf("%.0f", float64(cost.Milliseconds())), fmt.Sprintf("%s 端口连通，响应 %dms", addr, cost.Milliseconds()))
}

// probeDNS 解析域名并返回记录值，供工具箱的「DNS 查询」使用。
// 默认查 A/AAAA，可用 type 指定 CNAME/MX/TXT/NS/SRV；server 可指定 DNS 服务器（留空用本机解析器）。
func probeDNS(ctx context.Context, t Target, timeout time.Duration) Result {
	name := t.Str("name", t.Str("host", ""))
	if name == "" {
		return fail("", "缺少参数 name（待解析的域名）")
	}
	r := net.DefaultResolver
	if srv := t.Str("server", ""); srv != "" {
		addr := srv
		if !strings.Contains(addr, ":") {
			addr += ":53"
		}
		d := &net.Dialer{Timeout: timeout}
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "udp", addr)
		}}
	}
	typ := strings.ToUpper(t.Str("type", "A"))
	start := time.Now()
	var records []string
	var err error
	switch typ {
	case "A", "AAAA", "IP":
		var ips []net.IP
		if ips, err = r.LookupIP(ctx, "ip", name); err == nil {
			for _, ip := range ips {
				switch {
				case typ == "A" && ip.To4() != nil:
					records = append(records, ip.String())
				case typ == "AAAA" && ip.To4() == nil:
					records = append(records, ip.String())
				case typ == "IP":
					records = append(records, ip.String())
				}
			}
		}
	case "CNAME":
		var cname string
		if cname, err = r.LookupCNAME(ctx, name); err == nil && cname != "" {
			records = append(records, strings.TrimSuffix(cname, "."))
		}
	case "MX":
		var mxs []*net.MX
		if mxs, err = r.LookupMX(ctx, name); err == nil {
			for _, mx := range mxs {
				records = append(records, fmt.Sprintf("%s（优先级 %d）", strings.TrimSuffix(mx.Host, "."), mx.Pref))
			}
		}
	case "TXT":
		if records, err = r.LookupTXT(ctx, name); err != nil {
			records = nil
		}
	case "NS":
		var nss []*net.NS
		if nss, err = r.LookupNS(ctx, name); err == nil {
			for _, ns := range nss {
				records = append(records, strings.TrimSuffix(ns.Host, "."))
			}
		}
	case "SRV":
		var srvs []*net.SRV
		if _, srvs, err = r.LookupSRV(ctx, "", "", name); err == nil {
			for _, s := range srvs {
				records = append(records, fmt.Sprintf("%s:%d（优先级 %d，权重 %d）",
					strings.TrimSuffix(s.Target, "."), s.Port, s.Priority, s.Weight))
			}
		}
	default:
		return fail("", "不支持的记录类型："+typ+"（可用 A / AAAA / CNAME / MX / TXT / NS / SRV）")
	}
	if err != nil {
		return fail("", fmt.Sprintf("解析 %s 的 %s 记录失败: %v", name, typ, err))
	}
	if len(records) == 0 {
		return fail("", fmt.Sprintf("%s 没有 %s 记录", name, typ))
	}
	return ok(fmt.Sprintf("%d", len(records)),
		fmt.Sprintf("%s 的 %s 记录（%d 条，用时 %dms）：%s",
			name, typ, len(records), time.Since(start).Milliseconds(), strings.Join(records, "，")))
}

// probeHTTPAPI 检查 HTTP 接口：状态码、关键字、响应时间。
func probeHTTPAPI(ctx context.Context, t Target, timeout time.Duration) Result {
	url := t.Str("url", "")
	if url == "" {
		url = "http://" + t.Addr("80")
	}
	method := strings.ToUpper(t.Str("method", "GET"))
	expect := t.Int("expectCode", 200)
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: t.Str("skipTls", "false") == "true"},
	}}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return fail("", fmt.Sprintf("构造请求失败: %v", err))
	}
	if user := t.Str("user", ""); user != "" {
		req.SetBasicAuth(user, t.Str("password", ""))
	}
	if token := t.Str("token", ""); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fail("", fmt.Sprintf("请求 %s 失败: %v", url, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	cost := time.Since(start)
	if resp.StatusCode != expect {
		return fail(fmt.Sprintf("%d", resp.StatusCode),
			fmt.Sprintf("%s 返回状态码 %d，期望 %d", url, resp.StatusCode, expect))
	}
	msg := fmt.Sprintf("%s 响应 %dms，状态码 %d", url, cost.Milliseconds(), resp.StatusCode)
	if kw := t.Str("keyword", ""); kw != "" && !strings.Contains(string(body), kw) {
		return fail(fmt.Sprintf("%d", cost.Milliseconds()), msg+fmt.Sprintf("，响应体未包含关键字 %s", kw))
	}
	return ok(fmt.Sprintf("%d", cost.Milliseconds()), msg)
}

// probeFTP 通过 FTP 协议验证服务可用与账号有效。
func probeFTP(ctx context.Context, t Target, timeout time.Duration) Result {
	addr := t.Addr(defaultFTPPort)
	start := time.Now()
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fail("", fmt.Sprintf("连接 FTP %s 失败: %v", addr, err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := newLineReader(conn)
	if line, err := reader.read(); err != nil {
		return fail("", fmt.Sprintf("读取 FTP 欢迎语失败: %v", err))
	} else if !strings.HasPrefix(line, "220") {
		return fail("", "FTP 服务欢迎语异常："+line)
	}
	user := t.Str("username", t.Str("user", "anonymous"))
	password := t.Str("password", t.Str("secret", "anonymous@"))
	if err := sendFTP(conn, "USER "+user); err != nil {
		return fail("", err.Error())
	}
	line, err := reader.read()
	if err != nil {
		return fail("", err.Error())
	}
	if !strings.HasPrefix(line, "331") && !strings.HasPrefix(line, "230") {
		return fail("", "FTP 用户名阶段异常："+line)
	}
	if err := sendFTP(conn, "PASS "+password); err != nil {
		return fail("", err.Error())
	}
	if line, err = reader.read(); err != nil {
		return fail("", err.Error())
	}
	if !strings.HasPrefix(line, "230") {
		return fail("", fmt.Sprintf("FTP 登录失败：%s", line))
	}
	if err := sendFTP(conn, "NOOP"); err == nil {
		if l, err2 := reader.read(); err2 == nil && !strings.HasPrefix(l, "200") {
			return fail("", "FTP NOOP 异常："+l)
		}
	}
	_ = sendFTP(conn, "QUIT")
	cost := time.Since(start)
	return ok(fmt.Sprintf("%d", cost.Milliseconds()), fmt.Sprintf("FTP %s 登录成功，用时 %dms", addr, cost.Milliseconds()))
}

func sendFTP(conn net.Conn, cmd string) error {
	_, err := conn.Write([]byte(cmd + "\r\n"))
	return err
}

// lineReader 简单封装支持多行响应的 FTP 读取。
type lineReader struct {
	conn net.Conn
	buf  []byte
}

func newLineReader(conn net.Conn) *lineReader {
	return &lineReader{conn: conn, buf: make([]byte, 0, 512)}
}

func (l *lineReader) read() (string, error) {
	var out strings.Builder
	for {
		chunk := make([]byte, 1)
		n, err := l.conn.Read(chunk)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		if chunk[0] == '\n' {
			line := strings.TrimRight(out.String(), "\r")
			// 形如 "220-xxx" 表示多行响应继续
			if len(line) > 4 && line[3] == '-' {
				out.Reset()
				continue
			}
			return line, nil
		}
		out.WriteByte(chunk[0])
	}
}

// sshConfig 构造 SSH 客户端配置，支持口令与私钥两种凭据。
func sshConfig(t Target, timeout time.Duration) (*ssh.ClientConfig, error) {
	user := t.Str("username", t.Str("user", "root"))
	var auths []ssh.AuthMethod
	if pass := t.Str("password", t.Str("secret", "")); pass != "" {
		auths = append(auths, ssh.Password(pass))
	}
	if key := t.Str("privateKey", ""); key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err == nil {
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	if len(auths) == 0 {
		auths = append(auths, ssh.Password(""))
	}
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 监控场景仅验证连通性
		Timeout:         timeout,
	}, nil
}

// probeSSH 验证 SSH 登录并在远端执行一条轻量命令。
func probeSSH(ctx context.Context, t Target, timeout time.Duration) Result {
	cfg, err := sshConfig(t, timeout)
	if err != nil {
		return fail("", err.Error())
	}
	if cfg.User == "" {
		return fail("", "缺少参数 username")
	}
	addr := t.Addr(defaultSSHPort)
	start := time.Now()
	dial := func() (*ssh.Client, error) {
		return ssh.Dial("tcp", addr, cfg)
	}
	type dialResult struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, e := dial()
		ch <- dialResult{c, e}
	}()
	var client *ssh.Client
	select {
	case res := <-ch:
		if res.err != nil {
			return fail("", fmt.Sprintf("SSH 连接 %s 失败: %v", addr, res.err))
		}
		client = res.client
	case <-ctx.Done():
		return fail("", fmt.Sprintf("SSH 连接 %s 超时", addr))
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fail("", fmt.Sprintf("创建 SSH 会话失败: %v", err))
	}
	defer sess.Close()
	cmd := t.Str("cmd", "echo strix-ok")
	out, err := sess.CombinedOutput(cmd)
	cost := time.Since(start)
	if err != nil {
		return fail("", fmt.Sprintf("执行 %q 失败: %v", cmd, err))
	}
	text := strings.TrimSpace(string(out))
	return ok(fmt.Sprintf("%d", cost.Milliseconds()), fmt.Sprintf("SSH %s 正常，执行命令返回：%s", addr, truncate(text, 120)))
}

// probeSFTP 在 SSH 之上校验 SFTP 子系统可用。
func probeSFTP(ctx context.Context, t Target, timeout time.Duration) Result {
	cfg, err := sshConfig(t, timeout)
	if err != nil {
		return fail("", err.Error())
	}
	addr := t.Addr(defaultSSHPort)
	type dialResult struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, e := ssh.Dial("tcp", addr, cfg)
		ch <- dialResult{c, e}
	}()
	var client *ssh.Client
	select {
	case res := <-ch:
		if res.err != nil {
			return fail("", fmt.Sprintf("SFTP 连接 %s 失败: %v", addr, res.err))
		}
		client = res.client
	case <-ctx.Done():
		return fail("", fmt.Sprintf("SFTP 连接 %s 超时", addr))
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fail("", fmt.Sprintf("创建会话失败: %v", err))
	}
	defer sess.Close()
	if err := sess.RequestSubsystem("sftp"); err != nil {
		return fail("", fmt.Sprintf("服务端未启用 SFTP 子系统: %v", err))
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return fail("", fmt.Sprintf("打开通道失败: %v", err))
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fail("", fmt.Sprintf("读取通道失败: %v", err))
	}
	// 发送 SSH_FXP_INIT，服务端应回复 SSH_FXP_VERSION
	init := []byte{0x00, 0x00, 0x00, 0x05, 0x01, 0x00, 0x00, 0x00, 0x03}
	if _, err := stdin.Write(init); err != nil {
		return fail("", fmt.Sprintf("握手失败: %v", err))
	}
	header := make([]byte, 9)
	if _, err := io.ReadFull(stdout, header); err != nil {
		return fail("", fmt.Sprintf("未收到 SFTP 版本响应: %v", err))
	}
	return ok(fmt.Sprintf("%d", int(header[8])), fmt.Sprintf("SFTP %s 可用，协议版本 %d", addr, int(header[8])))
}

// probeIPMI 通过 RMCP Presence Ping 探测 IPMI/BMC 是否在线。
func probeIPMI(ctx context.Context, t Target, timeout time.Duration) Result {
	host := t.Str("host", t.Str("ip", ""))
	if host == "" {
		return fail("", "缺少参数 host")
	}
	addr := host + ":" + t.Str("port", defaultIPMI)
	d := &net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return fail("", fmt.Sprintf("连接 %s 失败: %v", addr, err))
	}
	defer conn.Close()
	// ASF/RMCP Presence Ping 报文
	pkt := []byte{
		0x06, 0x00, 0xff, 0x06,
		0x00, 0x00, 0x11, 0xbe,
		0x80, 0x00, 0x00, 0x00,
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(pkt); err != nil {
		return fail("", fmt.Sprintf("发送 RMCP 探测失败: %v", err))
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return fail("", fmt.Sprintf("未收到 IPMI 响应: %v", err))
	}
	if n < 9 || buf[8] != 0x40 {
		return fail("", fmt.Sprintf("IPMI 响应异常，数据长度 %d", n))
	}
	return ok("1", fmt.Sprintf("IPMI %s 在线，BMC 已响应 Presence Pong", addr))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
