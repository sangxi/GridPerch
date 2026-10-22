package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/probe"
)

// toolProbeTypes 工具箱允许按需执行的探测类型。
//
// 只放网络诊断类 —— 它们与目标主机操作系统无关，由服务端直接发包即可完成；
// SSH / 数据库 / 中间件等带凭据的探测没有列入，避免这个接口变成对任意目标的凭据试探入口
// （那类需求走「设备账号」页的连通性检测）。
var toolProbeTypes = map[string]bool{"icmp": true, "tcp": true, "dns": true, "httpapi": true}

// handleToolProbe 按需执行一次服务端探测，即工具箱里的「探测模式」。
// 结果同步返回，不写监控状态表、不参与告警 —— 它只是一次一次性诊断。
func (s *Server) handleToolProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type       string            `json:"type"`
		Params     map[string]string `json:"params"`
		TimeoutSec int               `json:"timeoutSec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "参数解析失败"})
		return
	}
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if !toolProbeTypes[typ] {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "不支持的探测类型：" + req.Type})
		return
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = 8 * time.Second
	}
	res := probe.Run(typ, req.Params, timeout)
	writeJSON(w, map[string]any{
		"ok":     res.Status != probe.StatusFail,
		"status": res.Status,
		"value":  res.Value,
		"msg":    res.Msg,
	})
}

// handleCommandGet 查询单条指令的执行结果，供工具箱「主机本地执行」轮询回包。
// 指令本身就是走既有的指令通道下发，这里只是补一个按 ID 取单条的读取口。
func (s *Server) handleCommandGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	c, err := s.store.CommandByID(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "指令不存在或已被清理"})
		return
	}
	writeJSON(w, c)
}
