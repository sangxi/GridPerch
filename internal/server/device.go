package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sangxi/gridperch/internal/probe"
)

// TestDeviceAccount 使用保存的凭据对目标设备发起一次真实连通性检测。
func (s *Server) TestDeviceAccount(id string) (*probe.Result, error) {
	acc, err := s.store.DeviceCredential(id)
	if err != nil {
		return nil, fmt.Errorf("读取设备账号失败: %w", err)
	}
	typ, ok := probe.DeviceProbeType[strings.ToLower(acc.Protocol)]
	if !ok {
		return nil, fmt.Errorf("协议 %s 暂不支持连通性检测", acc.Protocol)
	}
	target := probe.CredentialTarget(acc)
	if target["host"] == "" {
		return nil, fmt.Errorf("该设备账号未填写地址，无法检测")
	}
	res := probe.Run(typ, target, 8*time.Second)
	return &res, nil
}

func (s *Server) handleDeviceAccountTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
		return
	}
	res, err := s.TestDeviceAccount(r.PathValue("id"))
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":     res.Status != probe.StatusFail,
		"status": res.Status,
		"value":  res.Value,
		"msg":    res.Msg,
	})
}
