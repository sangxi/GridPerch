package server

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/model"
	"github.com/sangxi/gridperch/internal/notify"
)

// 告警催办（未认领升级）。
//
// 背景：告警发出去之后最常见的失效方式不是「没发」，而是「发了没人管」——
// 半夜一条告警进群，第二天翻聊天记录才发现。这里在告警产生 N 分钟仍未
// 被任何人认领时再推一轮，提示语明确写「未认领」，让它和普通新告警区分开。
//
// 与常规推送的三点差别（改之前先读）：
//  1. 不走 mailLastSent / webhookLastSent 时间游标，靠 alert.escalated 标记去重，
//     否则游标早被常规推送推过去了，催办永远查不到数据。
//  2. 静默规则照样生效：静默期内连催办也不发；但**照常打标记** ——
//     否则静默一结束，静默期内攒下的历史告警会集中爆一轮催办。
//  3. 关闭方式是 alertEscalateMin <= 0，默认 30 分钟。

// escalateUnackedAlerts 把超时无人认领的告警再推一轮。
func (s *Server) escalateUnackedAlerts() {
	settings, err := s.store.Settings()
	if err != nil {
		return
	}
	// 注意别用 intValue：它把 <=0 一律当无效值回落默认，
	// 那就没法用 0 表示「关闭催办」了。这里单独解析。
	minutes := 30
	if raw := strings.TrimSpace(settings["alertEscalateMin"]); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil {
			minutes = n
		}
	}
	if minutes <= 0 {
		return
	}
	items, err := s.store.UnackedAlerts(time.Duration(minutes) * time.Minute)
	if err != nil {
		log.Printf("查询待催办告警失败: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	silenced := s.store.SilenceChecker()
	ids := make([]string, 0, len(items))
	all := make([]notify.Alert, 0, len(items))
	for _, a := range items {
		ids = append(ids, a.ID)
		if silenced(a.Hostname, a.Type, a.Level, a.CreateTime) {
			continue
		}
		all = append(all, notify.Alert{
			Hostname: a.Hostname, Type: a.Type, Level: a.Level,
			Content:    fmt.Sprintf("已超过 %d 分钟无人认领 · %s", minutes, a.Content),
			CreateTime: a.CreateTime,
		})
	}
	// 先打标记再发送：发送失败也不该下一轮重发同一批，否则机器人挂了会变成刷屏。
	_ = s.store.MarkAlertEscalated(ids)
	if len(all) == 0 {
		return
	}
	prefix := fmt.Sprintf("【未认领催办·%d分钟】", minutes)
	s.escalateByWebhook(prefix, all)
	s.escalateByMail(settings, prefix, all)
}

func (s *Server) escalateByWebhook(prefix string, items []notify.Alert) {
	hooks, err := s.store.ListWebhooks()
	if err != nil {
		return
	}
	for _, h := range hooks {
		if !h.Enabled {
			continue
		}
		levels := levelFilter(h.Levels)
		list := make([]notify.Alert, 0, len(items))
		for _, a := range items {
			if levels[a.Level] {
				list = append(list, a)
			}
		}
		if len(list) == 0 {
			continue
		}
		cfg := s.webhookConfig(h)
		// 标题前缀换成催办，值班人一眼能看出这是「还没人管」而不是新故障
		cfg.Prefix = prefix + cfg.Prefix
		if err := notify.SendWebhook(cfg, list); err != nil {
			log.Printf("告警催办（%s）推送失败: %v", h.Name, err)
		}
	}
}

func (s *Server) escalateByMail(settings map[string]string, prefix string, items []notify.Alert) {
	if settings["mailEnabled"] != "true" {
		return
	}
	cfg := s.mailConfigFrom(settings)
	if !cfg.Enabled() {
		return
	}
	if inQuietHours(settings["mailQuietFrom"], settings["mailQuietTo"], time.Now()) {
		return
	}
	levels := levelFilter(settings["mailLevels"])
	list := make([]notify.Alert, 0, len(items))
	for _, a := range items {
		if levels[a.Level] {
			list = append(list, a)
		}
	}
	if len(list) == 0 {
		return
	}
	cfg.Prefix = prefix + cfg.Prefix
	subject := cfg.Subject(list[0])
	if err := notify.Send(cfg, list); err != nil {
		log.Printf("告警催办邮件发送失败: %v", err)
		s.store.AppendMailLog(model.MailLog{Kind: "escalate", Subject: subject, Recipients: cfg.To,
			ItemCount: len(list), Status: "fail", Error: err.Error()})
		return
	}
	s.store.AppendMailLog(model.MailLog{Kind: "escalate", Subject: subject, Recipients: cfg.To,
		ItemCount: len(list), Status: "ok"})
}
