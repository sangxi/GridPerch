package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sangxi/OpsMonitor/internal/store"
)

// resolveAgentHost 把采集端上报的主机名解析为平台当前使用的主机名。
// 主机改名后，旧采集端仍以旧名上报，这里统一归位，避免旧名“复活”成一台新主机。
// 未发生改名时原样返回，开销仅一次带主键索引的查表。
func (s *Server) resolveAgentHost(name string) string {
	if name == "" {
		return name
	}
	return s.store.ResolveHostname(name)
}

// hostRenameError 把 store 层的主机改名错误翻译成界面能直接展示的提示。
func hostRenameError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrHostNameEmpty):
		return http.StatusBadRequest, "主机名不能为空"
	case errors.Is(err, store.ErrHostNameSame):
		return http.StatusBadRequest, "新主机名与原名相同，无需修改"
	case errors.Is(err, store.ErrHostNotFound):
		return http.StatusNotFound, "主机不存在，可能已被删除"
	case errors.Is(err, store.ErrHostNameTaken):
		return http.StatusConflict, "该主机名已被其他主机占用，请换一个"
	default:
		return http.StatusInternalServerError, err.Error()
	}
}

// handleHostRename 重命名主机：把该主机的全部数据迁移到新名字下，
// 并登记「旧名 → 新名」映射，使仍在用旧名上报的采集端自动归位。
func (s *Server) handleHostRename(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请求格式不正确"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "主机名不能为空"})
		return
	}
	// 与一键安装脚本走同一套清洗：中文等文字保留，shell/PowerShell 元字符替换为 -
	name := sanitizeAgentName(req.Name)
	if err := s.store.RenameHost(host, name); err != nil {
		code, msg := hostRenameError(err)
		writeJSONStatus(w, code, map[string]any{"ok": false, "msg": msg})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name, "msg": "已重命名为 " + name})
}

// handleHostDelete 删除主机及其全部历史数据。
// 采集端若仍在运行，会在下次上报时被重新发现——需要在目标主机上停用采集端才会彻底消失。
func (s *Server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if err := s.store.DeleteHost(host); err != nil {
		code, msg := hostRenameError(err)
		writeJSONStatus(w, code, map[string]any{"ok": false, "msg": msg})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "msg": "已删除主机 " + host})
}
