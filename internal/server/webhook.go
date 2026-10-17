package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/sangxi/strix/internal/model"
	"github.com/sangxi/strix/internal/notify"
	"github.com/sangxi/strix/internal/store"
)

/* ---------------- Webhook 机器人配置管理 ---------------- */

// webhookView 返回给控制台的视图对象，密钥不回传明文。
type webhookView struct {
	store.Webhook
	SecretSet bool `json:"secretSet"`
}

// handleWebhooks 列出全部机器人配置（GET）或新增一个（POST）。
func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListWebhooks()
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		views := make([]webhookView, 0, len(list))
		for _, h := range list {
			views = append(views, webhookView{Webhook: h, SecretSet: h.Secret != ""})
		}
		writeJSON(w, map[string]any{
			"ok":    true,
			"list":  views,
			"kinds": notify.WebhookKinds(),
		})
	case http.MethodPost:
		var req webhookForm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		hook, err := s.webhookFromForm(req, nil)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		if err := s.store.SaveWebhook(hook); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": hook.ID})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// handleWebhookItem 更新（PUT）或删除（DELETE）单个机器人配置。
func (s *Server) handleWebhookItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少 ID"})
		return
	}
	old, err := s.store.GetWebhook(id)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if old == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "该机器人配置不存在"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req webhookForm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
			return
		}
		hook, err := s.webhookFromForm(req, old)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		hook.ID = id
		hook.Create = old.Create
		if err := s.store.SaveWebhook(hook); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": id})
	case http.MethodDelete:
		if err := s.store.DeleteWebhook(id); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}

// webhookForm 控制台提交的表单字段。
type webhookForm struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Secret      string `json:"secret"`
	AtMobiles   string `json:"atMobiles"`
	AtAll       bool   `json:"atAll"`
	Enabled     bool   `json:"enabled"`
	Levels      string `json:"levels"`
	ClearSecret bool   `json:"clearSecret"`
}

// webhookFromForm 校验并转换表单，密钥沿用旧值时保留原密文。
func (s *Server) webhookFromForm(req webhookForm, old *store.Webhook) (*store.Webhook, error) {
	kind := strings.TrimSpace(req.Kind)
	valid := false
	for _, k := range notify.WebhookKinds() {
		if k.Key == kind {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("不支持的机器人类型：%s", kind)
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		return nil, fmt.Errorf("请填写 Webhook 地址")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("Webhook 地址需以 http:// 或 https:// 开头")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = kindName(kind)
	}
	// 密钥留空表示「不修改」，显式 clearSecret 才清空，避免编辑时误删
	secret := ""
	if old != nil {
		secret = old.Secret
	}
	switch {
	case req.ClearSecret:
		secret = ""
	case strings.TrimSpace(req.Secret) != "":
		enc, err := s.store.EncryptSecret(strings.TrimSpace(req.Secret))
		if err != nil {
			return nil, fmt.Errorf("密钥加密失败: %w", err)
		}
		secret = enc
	}
	return &store.Webhook{
		Name:      name,
		Kind:      kind,
		URL:       url,
		Secret:    secret,
		AtMobiles: strings.TrimSpace(req.AtMobiles),
		AtAll:     req.AtAll,
		Enabled:   req.Enabled,
		Levels:    strings.TrimSpace(req.Levels),
	}, nil
}

func kindName(kind string) string {
	for _, k := range notify.WebhookKinds() {
		if k.Key == kind {
			return k.Name
		}
	}
	return kind
}

// handleWebhookTest 向指定机器人发一条测试消息，验证地址与加签是否正确。
func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	hook, err := s.store.GetWebhook(id)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	if hook == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "该机器人配置不存在"})
		return
	}
	alerts := []notify.Alert{{
		Hostname: "测试主机", Type: "info", Level: "info",
		Content:    "这是一条来自 Strix 的测试消息，说明该机器人通道配置正确。",
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
	}}
	cfg := s.webhookConfig(*hook)
	if err := notify.SendWebhook(cfg, alerts); err != nil {
		_ = s.store.MarkWebhookResult(id, "fail", err.Error())
		s.store.AppendMailLog(model.MailLog{Kind: "webhook", Subject: "测试消息", Recipients: hook.Name,
			ItemCount: 1, Status: "fail", Error: err.Error()})
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "发送失败：" + err.Error()})
		return
	}
	_ = s.store.MarkWebhookResult(id, "ok", "")
	s.store.AppendMailLog(model.MailLog{Kind: "webhook", Subject: "测试消息", Recipients: hook.Name,
		ItemCount: 1, Status: "ok"})
	writeJSON(w, map[string]any{"ok": true, "msg": "测试消息已发送到「" + hook.Name + "」"})
}

// webhookConfig 把库里的记录转换为发送配置，密钥在此解密。
func (s *Server) webhookConfig(h store.Webhook) notify.WebhookConfig {
	return notify.WebhookConfig{
		ID:        h.ID,
		Name:      h.Name,
		Kind:      h.Kind,
		URL:       h.URL,
		Secret:    s.decryptSetting(h.Secret),
		AtMobiles: splitList(h.AtMobiles),
		AtAll:     h.AtAll,
		Prefix:    s.alertPrefix(),
		Enabled:   h.Enabled,
	}
}

// alertPrefix 读取统一配置的告警标题前缀，邮件与机器人共用。
func (s *Server) alertPrefix() string {
	settings, err := s.store.Settings()
	if err != nil {
		return ""
	}
	return settings["alertPrefix"]
}

/* ---------------- 告警推送到机器人 ---------------- */

// webhookLastRun 与邮件一样做聚合节流，避免告警风暴时刷屏。
var webhookLastRun time.Time

// pushAlertWebhook 把新增告警推送到全部启用的机器人。
//
// 与邮件各自维护游标，互不影响：只开机器人不开邮件时机器人照样能收到，
// 反之亦然。
func (s *Server) pushAlertWebhook() {
	all, err := s.store.ListWebhooks()
	if err != nil {
		return
	}
	type target struct {
		cfg    notify.WebhookConfig
		levels string
	}
	targets := make([]target, 0, len(all))
	for _, h := range all {
		if !h.Enabled {
			continue
		}
		targets = append(targets, target{cfg: s.webhookConfig(h), levels: h.Levels})
	}
	if len(targets) == 0 {
		return
	}

	settings, err := s.store.Settings()
	if err != nil {
		return
	}
	interval := intValue(settings["webhookIntervalSec"], 60)
	if interval < 15 {
		interval = 15
	}
	if !webhookLastRun.IsZero() && time.Since(webhookLastRun) < time.Duration(interval)*time.Second {
		return
	}
	webhookLastRun = time.Now()

	items, err := s.store.AlertsSince(settings["webhookLastSent"])
	if err != nil {
		log.Printf("查询待推送告警失败: %v", err)
		return
	}
	maxTime := settings["webhookLastSent"]
	silenced := s.store.SilenceChecker()
	list := make([]notify.Alert, 0, len(items))
	for _, a := range items {
		if a.CreateTime > maxTime {
			maxTime = a.CreateTime
		}
		// 命中静默规则：照常留痕，不推送
		if silenced(a.Hostname, a.Type, a.Level, a.CreateTime) {
			continue
		}
		list = append(list, notify.Alert{Hostname: a.Hostname, Type: a.Type, Level: a.Level,
			Content: a.Content, CreateTime: a.CreateTime})
	}
	if settings["webhookRecover"] == "true" {
		events, eerr := s.store.RecentRecoveriesSince(settings["webhookLastSent"])
		if eerr != nil {
			log.Printf("查询恢复事件失败: %v", eerr)
		}
		for _, e := range events {
			if e.CheckTime > maxTime {
				maxTime = e.CheckTime
			}
			list = append(list, notify.Alert{Hostname: e.Hostname, Type: e.Type, Level: "info",
				Content: fmt.Sprintf("监控项 %s 已恢复正常（%s）", e.Name, e.Msg), CreateTime: e.CheckTime})
		}
	}

	// 游标独立于邮件推进：即使全部被级别过滤也要推进，
	// 否则这批告警会一直占住查询窗口，把后面真正要发的告警挡在外面。
	defer func() {
		if maxTime != settings["webhookLastSent"] {
			_ = s.store.SaveSettings(map[string]string{"webhookLastSent": maxTime})
		}
	}()

	if len(list) == 0 {
		return
	}
	for _, t := range targets {
		subset := filterByLevels(list, t.levels)
		if len(subset) == 0 {
			continue
		}
		if err := notify.SendWebhook(t.cfg, subset); err != nil {
			log.Printf("机器人「%s」推送失败: %v", t.cfg.Name, err)
			_ = s.store.MarkWebhookResult(t.cfg.ID, "fail", err.Error())
			s.store.AppendMailLog(model.MailLog{Kind: "webhook", Subject: "告警推送", Recipients: t.cfg.Name,
				ItemCount: len(subset), Status: "fail", Error: err.Error()})
			continue
		}
		_ = s.store.MarkWebhookResult(t.cfg.ID, "ok", "")
		s.store.AppendMailLog(model.MailLog{Kind: "webhook", Subject: "告警推送", Recipients: t.cfg.Name,
			ItemCount: len(subset), Status: "ok"})
		log.Printf("已推送 %d 条告警至机器人「%s」", len(subset), t.cfg.Name)
	}
	_ = s.store.SaveSettings(map[string]string{"webhookLastTime": timeNowString()})
}

// filterByLevels 按机器人订阅的级别过滤告警，空配置表示订阅全部级别。
func filterByLevels(list []notify.Alert, levels string) []notify.Alert {
	want := map[string]bool{}
	for _, p := range strings.Split(levels, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	if len(want) == 0 {
		return list
	}
	out := make([]notify.Alert, 0, len(list))
	for _, a := range list {
		if want[a.Level] {
			out = append(out, a)
		}
	}
	return out
}
