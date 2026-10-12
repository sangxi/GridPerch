// Package notify 提供告警邮件推送，支持自定义标题前缀与签名。
package notify

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// MailConfig 邮件发送参数。
type MailConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       string // 多个收件人以逗号分隔
	TLS      bool   // 隐式 TLS（通常为 465 端口）
	StartTLS bool   // 明文连接后升级为 TLS（通常为 25/587 端口）
	Prefix   string // 告警标题前缀
	Sign     string // 邮件签名
}

// Enabled 判断邮件配置是否完整可用。
func (c MailConfig) Enabled() bool {
	return c.Host != "" && c.From != "" && c.To != ""
}

// Alert 待推送的告警条目。
type Alert struct {
	Hostname   string
	Type       string
	Level      string
	Content    string
	CreateTime string
}

// Subject 生成带前缀的邮件标题。
func (c MailConfig) Subject(a Alert) string {
	prefix := strings.TrimSpace(c.Prefix)
	if prefix == "" {
		prefix = "[OpsMonitor]"
	}
	return fmt.Sprintf("%s %s 告警：%s", prefix, levelCN(a.Level), a.Hostname)
}

// Body 生成邮件正文。
func (c MailConfig) Body(alerts []Alert) string {
	var sb strings.Builder
	sb.WriteString("<div style=\"font-family:Microsoft YaHei,Arial;font-size:14px;color:#222\">")
	sb.WriteString(fmt.Sprintf("<h3 style=\"color:#dc2626\">检测到 %d 条告警</h3>", len(alerts)))
	sb.WriteString("<table border=\"1\" cellspacing=\"0\" cellpadding=\"6\" style=\"border-collapse:collapse;border-color:#e5e7eb\">")
	sb.WriteString("<tr style=\"background:#f3f4f6\"><th>主机</th><th>类型</th><th>级别</th><th>内容</th><th>时间</th></tr>")
	for _, a := range alerts {
		sb.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",
			escape(a.Hostname), escape(typeCN(a.Type)), escape(levelCN(a.Level)), escape(a.Content), escape(a.CreateTime)))
	}
	sb.WriteString("</table>")
	if sign := strings.TrimSpace(c.Sign); sign != "" {
		sb.WriteString(fmt.Sprintf("<p style=\"margin-top:16px;color:#6b7280\">%s</p>", escape(sign)))
	}
	sb.WriteString("</div>")
	return sb.String()
}

// Send 发送一批告警邮件，整体失败返回错误。
func Send(cfg MailConfig, alerts []Alert) error {
	if !cfg.Enabled() || len(alerts) == 0 {
		return nil
	}
	port := cfg.Port
	if port == 0 {
		port = 25
	}
	if cfg.TLS {
		port = pickDefault(port, 465)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	subject := cfg.Subject(alerts[0])
	body := cfg.Body(alerts)
	recipients := splitTo(cfg.To)
	msg := buildMessage(cfg.From, recipients, subject, body)

	from := cfg.From
	if strings.Contains(from, "<") {
		if i := strings.Index(from, "<"); i >= 0 {
			from = from[i+1:]
			from = strings.TrimSuffix(from, ">")
		}
	}
	var err error
	if cfg.TLS {
		err = sendTLS(addr, cfg.Host, cfg.User, cfg.Password, from, recipients, []byte(msg))
	} else {
		var c *smtp.Client
		c, err = smtp.Dial(addr)
		if err == nil {
			defer c.Close()
			if host, _, serr := net.SplitHostPort(addr); serr == nil {
				if herr := c.Hello(host); herr != nil {
					log.Printf("SMTP HELO 失败: %v", herr)
				}
			}
			ok, _ := c.Extension("STARTTLS")
			switch {
			case cfg.StartTLS && !ok:
				return fmt.Errorf("服务端不支持 STARTTLS，如需加密请改用隐式 TLS（465 端口）")
			case ok:
				if terr := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); terr != nil {
					if cfg.StartTLS {
						return fmt.Errorf("STARTTLS 升级失败: %w", terr)
					}
					log.Printf("STARTTLS 升级失败，回退明文发送: %v", terr)
				}
			}
			if cfg.User != "" {
				if aerr := c.Auth(plainAuthFallback(cfg.User, cfg.Password)); aerr != nil {
					return fmt.Errorf("SMTP 认证失败: %w", aerr)
				}
			}
			if merr := c.Mail(from); merr == nil {
				for _, rcpt := range recipients {
					if rerr := c.Rcpt(rcpt); rerr != nil {
						err = rerr
						break
					}
				}
				if err == nil {
					w, derr := c.Data()
					if derr != nil {
						err = derr
					} else {
						_, err = w.Write([]byte(msg))
						_ = w.Close()
					}
				}
			} else {
				err = merr
			}
		}
	}
	if err != nil {
		return err
	}
	log.Printf("已推送 %d 条告警至 %s", len(alerts), strings.Join(recipients, ","))
	return nil
}

// sendTLS 通过隐式 TLS 连接发送邮件。
func sendTLS(addr, host, user, password, from string, recipients []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(plainAuthFallback(user, password)); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

// plainAuthFallback 兼容不校验身份的服务器。
func plainAuthFallback(user, password string) smtp.Auth {
	return &loginAuth{user: user, password: password}
}

type loginAuth struct {
	user     string
	password string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("服务端请求了额外的认证步骤")
	}
	return nil, nil
}

// buildMessage 构造 MIME 邮件内容，标题支持 UTF-8 编码。
func buildMessage(from string, recipients []string, subject, html string) string {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + strings.Join(recipients, ",") + "\r\n")
	sb.WriteString("Subject: =?UTF-8?B?" + base64Encode(subject) + "?=\r\n")
	sb.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	sb.WriteString(base64Wrap(html))
	sb.WriteString("\r\n")
	return sb.String()
}

func base64Encode(s string) string {
	return base64Wrap(s)
}

func levelCN(level string) string {
	switch level {
	case "critical":
		return "严重"
	case "warning":
		return "警告"
	case "info":
		return "提示"
	default:
		return level
	}
}

func typeCN(typ string) string {
	names := map[string]string{
		"cpu": "CPU", "mem": "内存", "disk": "磁盘", "load": "负载", "offline": "离线",
		"process": "进程", "port": "端口", "http": "HTTP", "ping": "PING", "docker": "Docker",
		"log": "日志", "file": "文件防篡改", "nginxlog": "Nginx日志", "script": "自定义脚本",
		"tcp": "TCP端口", "httpapi": "HTTP接口", "icmp": "ICMP", "snmp": "SNMP", "mysql": "MySQL",
		"redis": "Redis", "kafka": "Kafka", "rabbitmq": "RabbitMQ", "elasticsearch": "Elasticsearch",
		"ftp": "FTP", "sftp": "SFTP", "ssh": "SSH", "ipmi": "IPMI", "k8s": "Kubernetes",
		"login": "主机登录",
	}
	if v, ok := names[typ]; ok {
		return v
	}
	return typ
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

func splitTo(to string) []string {
	var out []string
	for _, p := range strings.Split(to, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pickDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
