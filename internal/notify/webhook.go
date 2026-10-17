package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WebhookKind 机器人类型。不同群机器人的消息体格式差异较大，
// 这里按类型分别组装，避免用户自己拼 JSON。
const (
	KindDingTalk = "dingtalk" // 钉钉自定义机器人
	KindWeCom    = "wecom"    // 企业微信群机器人
	KindFeishu   = "feishu"   // 飞书（Lark）自定义机器人
	KindSlack    = "slack"    // Slack Incoming Webhook
	KindCustom   = "custom"   // 通用 Webhook，POST 结构化 JSON
)

// WebhookKindInfo 描述一种机器人类型，供控制台渲染表单提示。
type WebhookKindInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	URLHint     string `json:"urlHint"`
	SecretLabel string `json:"secretLabel"` // 空表示该类型无需密钥
	SecretHint  string `json:"secretHint"`
	AtSupport   bool   `json:"atSupport"` // 是否支持 @ 指定成员
}

// WebhookKinds 返回全部支持的机器人类型。
func WebhookKinds() []WebhookKindInfo {
	return []WebhookKindInfo{
		{
			Key: KindDingTalk, Name: "钉钉群机器人", AtSupport: true,
			URLHint:     "https://oapi.dingtalk.com/robot/send?access_token=xxx",
			SecretLabel: "加签密钥（可选）",
			SecretHint:  "安全设置选「加签」时填 SEC 开头的密钥；选「自定义关键词」时留空",
		},
		{
			Key: KindWeCom, Name: "企业微信群机器人", AtSupport: true,
			URLHint:     "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx",
			SecretLabel: "",
			SecretHint:  "企业微信群机器人不支持加签，留空即可",
		},
		{
			Key: KindFeishu, Name: "飞书自定义机器人",
			URLHint:     "https://open.feishu.cn/open-apis/bot/v2/hook/xxx",
			SecretLabel: "加签校验（可选）",
			SecretHint:  "安全设置勾选「签名校验」时填写对应密钥，否则留空",
		},
		{
			Key: KindSlack, Name: "Slack Incoming Webhook",
			URLHint: "https://hooks.slack.com/services/xxx/yyy/zzz",
		},
		{
			Key: KindCustom, Name: "通用 Webhook（自定义 JSON）",
			URLHint:    "https://your-gateway.example.com/alert",
			SecretHint: "按需填写，会作为 X-Strix-Secret 请求头发送（同时兼容旧头 X-Opsm-Secret）",
		},
	}
}

// WebhookConfig 一个机器人推送配置。
type WebhookConfig struct {
	ID        string
	Name      string
	Kind      string
	URL       string
	Secret    string
	AtMobiles []string
	AtAll     bool
	Prefix    string
	Enabled   bool
}

// Usable 判断该 Webhook 是否具备发送条件。
func (c WebhookConfig) Usable() bool {
	return c.URL != "" && c.Kind != ""
}

// kindName 返回类型的中文名，用于日志与错误信息。
func kindName(kind string) string {
	for _, k := range WebhookKinds() {
		if k.Key == kind {
			return k.Name
		}
	}
	return kind
}

// maxWebhookAlerts 单条消息最多列出的告警条数，超出部分折叠为统计行，
// 否则主机批量离线时消息会长到被机器人平台拒收。
const maxWebhookAlerts = 20

// timeLayout 消息体里的时间格式，与库中告警时间保持一致。
const timeLayout = "2006-01-02 15:04:05"

// alertMarkdown 生成机器人消息正文，标题与正文分开返回。
func alertMarkdown(prefix string, alerts []Alert) (title, text string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "[Strix]"
	}
	title = fmt.Sprintf("%s 检测到 %d 条新告警", prefix, len(alerts))

	var sb strings.Builder
	sb.WriteString("### " + title + "\n")
	shown := alerts
	if len(shown) > maxWebhookAlerts {
		shown = shown[:maxWebhookAlerts]
	}
	for _, a := range shown {
		sb.WriteString(fmt.Sprintf("> **%s** ｜ %s ｜ %s\n>\n> %s\n>\n> 时间：%s\n\n",
			oneLine(a.Hostname), levelCN(a.Level), typeCN(a.Type), oneLine(a.Content), oneLine(a.CreateTime)))
	}
	if rest := len(alerts) - len(shown); rest > 0 {
		sb.WriteString(fmt.Sprintf("> 其余 %d 条请在控制台「告警中心」查看。\n", rest))
	}
	return title, sb.String()
}

// plainText 生成纯文本正文，供不支持 markdown 的通道使用。
func plainText(prefix string, alerts []Alert) string {
	title, _ := alertMarkdown(prefix, alerts)
	lines := []string{title}
	for _, a := range alerts[:minInt(len(alerts), maxWebhookAlerts)] {
		lines = append(lines, fmt.Sprintf("宿主：%s 级别：%s 类型：%s", oneLine(a.Hostname), levelCN(a.Level), typeCN(a.Type)))
		lines = append(lines, fmt.Sprintf("内容：%s", oneLine(a.Content)))
		lines = append(lines, fmt.Sprintf("时间：%s", oneLine(a.CreateTime)))
		lines = append(lines, "")
	}
	if len(alerts) > maxWebhookAlerts {
		lines = append(lines, fmt.Sprintf("其余 %d 条请在控制台「告警中心」查看。", len(alerts)-maxWebhookAlerts))
	}
	return strings.Join(lines, "\n")
}

// SendWebhook 推送一批告警到机器人，返回错误表示整体未送达。
func SendWebhook(cfg WebhookConfig, alerts []Alert) error {
	if !cfg.Usable() || len(alerts) == 0 {
		return nil
	}
	target, payload, err := buildWebhookRequest(cfg, alerts)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if cfg.Secret != "" {
		// 新旧两个头都发：X-Opsm-Secret 是更名前就有的名字，现网接收端可能已经按它
		// 校验了，单独改名会让这些接收端全部失败；X-Strix-Secret 供新接入方使用。
		req.Header.Set("X-Strix-Secret", cfg.Secret)
		req.Header.Set("X-Opsm-Secret", cfg.Secret)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s 失败: %w", kindName(cfg.Kind), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 返回 HTTP %d: %s", kindName(cfg.Kind), resp.StatusCode, truncateText(string(raw), 200))
	}
	// 机器人平台普遍用 HTTP 200 + 业务错误码表示失败，必须再解析一层
	if err := parseWebhookReply(cfg.Kind, raw); err != nil {
		return err
	}
	return nil
}

// buildWebhookRequest 按机器人类型组装请求地址与消息体。
func buildWebhookRequest(cfg WebhookConfig, alerts []Alert) (string, map[string]any, error) {
	title, md := alertMarkdown(cfg.Prefix, alerts)
	target := cfg.URL
	now := time.Now()

	switch cfg.Kind {
	case KindDingTalk:
		// 加签：timestamp + "\n" + secret 做 HMAC-SHA256，base64 后 URL 编码
		if cfg.Secret != "" {
			ts := strconv.FormatInt(now.UnixMilli(), 10)
			sign := signDingTalk(ts, cfg.Secret)
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target = fmt.Sprintf("%s%stimestamp=%s&sign=%s", target, sep, ts, url.QueryEscape(sign))
		}
		payload := map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": md},
		}
		if at := atPayload(cfg); len(at) > 0 || cfg.AtAll {
			body := map[string]any{}
			if len(at) > 0 {
				body["atMobiles"] = at
			}
			if cfg.AtAll {
				body["isAtAll"] = true
			}
			payload["at"] = body
		}
		return target, payload, nil

	case KindWeCom:
		content := md
		if at := wecomAt(cfg); at != "" {
			content += "\n" + at
		}
		return target, map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"content": content},
		}, nil

	case KindFeishu:
		// 飞书签名：以 "timestamp\nsecret" 为密钥、空串为消息做 HMAC-SHA256
		payload := map[string]any{
			"msg_type": "text",
			"content":  map[string]any{"text": plainText(cfg.Prefix, alerts)},
		}
		if cfg.Secret != "" {
			ts := strconv.FormatInt(now.Unix(), 10)
			payload["timestamp"] = ts
			payload["sign"] = signFeishu(ts, cfg.Secret)
		}
		return target, payload, nil

	case KindSlack:
		return target, map[string]any{"text": plainText(cfg.Prefix, alerts)}, nil

	case KindCustom:
		items := make([]map[string]string, 0, len(alerts))
		for _, a := range alerts {
			items = append(items, map[string]string{
				"hostname": a.Hostname, "type": a.Type, "level": a.Level,
				"content": a.Content, "time": a.CreateTime,
			})
		}
		return target, map[string]any{
			"title":   title,
			"content": md,
			"text":    plainText(cfg.Prefix, alerts),
			"count":   len(alerts),
			"alerts":  items,
			"source":  "Strix",
			"time":    now.Format(timeLayout),
		}, nil
	}
	return "", nil, fmt.Errorf("不支持的机器人类型：%s", cfg.Kind)
}

// parseWebhookReply 解析各平台返回体，识别 HTTP 200 但业务失败的情况。
func parseWebhookReply(kind string, raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// 部分通道（如 Slack）返回纯文本 ok
		if strings.EqualFold(text, "ok") {
			return nil
		}
		return nil
	}
	codeOf := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch n := v.(type) {
				case float64:
					return n, true
				case string:
					if f, err := strconv.ParseFloat(n, 64); err == nil {
						return f, true
					}
				}
			}
		}
		return 0, false
	}
	msgOf := func() string {
		for _, k := range []string{"errmsg", "msg", "message", "error"} {
			if v, ok := m[k].(string); ok && v != "" && v != "ok" && v != "success" {
				return v
			}
		}
		return text
	}
	// 钉钉 / 企业微信：errcode
	if code, ok := codeOf("errcode"); ok && code != 0 {
		return fmt.Errorf("%s 返回错误：%s", kindName(kind), msgOf())
	}
	// 飞书：code
	if code, ok := codeOf("code"); ok && code != 0 {
		return fmt.Errorf("%s 返回错误：%s", kindName(kind), msgOf())
	}
	// 飞书旧版：StatusCode
	if code, ok := codeOf("StatusCode"); ok && code != 0 {
		return fmt.Errorf("%s 返回错误：%s", kindName(kind), msgOf())
	}
	return nil
}

// signDingTalk 钉钉加签。
func signDingTalk(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// signFeishu 飞书加签：以 "timestamp\nsecret" 做密钥、空内容做 HMAC-SHA256。
func signFeishu(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	mac.Write([]byte{})
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// atPayload 钉钉 @ 手机号列表。
func atPayload(cfg WebhookConfig) []string {
	out := make([]string, 0, len(cfg.AtMobiles))
	for _, p := range cfg.AtMobiles {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// wecomAt 企业微信的 @ 语法写在正文里。
func wecomAt(cfg WebhookConfig) string {
	parts := atPayload(cfg)
	if len(parts) == 0 && !cfg.AtAll {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString("<@" + p + "> ")
	}
	if cfg.AtAll {
		sb.WriteString("<@all>")
	}
	return sb.String()
}

// oneLine 去掉换行，避免机器人把内容当成多条消息解析。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
