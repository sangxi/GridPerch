package server

import (
	"net/http"
	"strconv"

	"github.com/sangxi/gridperch/internal/store"
)

// openAPI 校验开放接口凭证后放行，凭证权限需覆盖接口要求的 perm。
func (s *Server) openAPI(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get("apiKey")
		}
		if key == "" {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "缺少凭证：请在 X-API-Key 请求头或 apiKey 参数中携带开放接口密钥"})
			return
		}
		if !s.store.CheckAPIToken(key, perm) {
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "凭证无效或无该接口权限"})
			return
		}
		s.store.TouchAPIToken(key)
		next(w, r)
	}
}

// openHosts 开放主机清单与实时概要。
func (s *Server) openHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(hosts), "data": hosts})
}

// openAlerts 开放告警列表。
func (s *Server) openAlerts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	list, err := s.store.Alerts(limit)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(list), "data": list})
}

// openMetrics 开放主机指标时序数据。
func (s *Server) openMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	host := q.Get("host")
	if host == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少参数 host"})
		return
	}
	typ := q.Get("type")
	if typ == "" {
		typ = "cpu"
	}
	minutes, _ := strconv.Atoi(q.Get("minutes"))
	if minutes <= 0 {
		minutes = 60
	}
	series, err := s.store.Metrics(host, typ, minutes)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "host": host, "type": typ, "data": series})
}

// openMonitors 开放监控项及其最新状态。
func (s *Server) openMonitors(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.store.ListTasks()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	states, err := s.store.ListMonitorStates(r.URL.Query().Get("taskId"))
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "tasks": tasks, "states": states, "types": store.TaskTypes})
}

// openAssets 开放资产台账。
func (s *Server) openAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.store.ListAssets(q.Get("keyword"), q.Get("category"), q.Get("status"))
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(list), "data": list})
}
