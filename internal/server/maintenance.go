package server

import (
	"log"
	"net/http"
	"sync"
	"time"
)

/*
运维手动作业入口。

定时清理（Purge）按 PURGE_INTERVAL 跑，但有两种情况需要立刻清一次：
  - 保留期策略刚修好/刚调整，历史积压很多，等下一个整点太慢；
  - 准备做备份，需要先把过期数据清掉，快照才装得下。

清理本身是分批删（每批几千行、独立事务），跑起来会把写连接占满，
所以放到后台协程里执行，接口立刻返回，前端/脚本轮询状态即可。
*/

// purgeJob 记录一次手动清理的状态，供接口回显。
type purgeJob struct {
	mu       sync.Mutex
	running  bool
	lastAt   string
	lastErr  string
	lastCost string
}

// handlePurge 触发或查询手动清理。
//
//	POST /api/maintenance/purge  开始一次清理（已在跑则返回 409）
//	GET  /api/maintenance/purge  查询进度与上次结果
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	j := &s.purge
	switch r.Method {
	case http.MethodGet:
		j.mu.Lock()
		out := map[string]any{
			"ok": true, "running": j.running,
			"lastAt": j.lastAt, "lastErr": j.lastErr, "lastCost": j.lastCost,
		}
		j.mu.Unlock()
		writeJSON(w, out)
	case http.MethodPost:
		j.mu.Lock()
		if j.running {
			j.mu.Unlock()
			writeJSONStatus(w, http.StatusConflict, map[string]any{"ok": false, "msg": "清理正在进行中，请稍候"})
			return
		}
		j.running = true
		j.mu.Unlock()

		go func() {
			start := time.Now()
			err := s.store.Purge()
			cost := time.Since(start).Round(time.Second).String()
			j.mu.Lock()
			j.running = false
			j.lastAt = time.Now().Format("2006-01-02 15:04:05")
			j.lastCost = cost
			if err != nil {
				j.lastErr = err.Error()
			} else {
				j.lastErr = ""
			}
			j.mu.Unlock()
			if err != nil {
				log.Printf("手动清理失败（耗时 %s）: %v", cost, err)
				return
			}
			log.Printf("手动清理完成，耗时 %s", cost)
		}()
		writeJSON(w, map[string]any{"ok": true, "msg": "已开始在后台清理，可轮询进度"})
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "msg": "不支持的方法"})
	}
}
