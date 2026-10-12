package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/sangxi/OpsMonitor/internal/model"
)

// commandLoop 周期性拉取待执行指令，执行后回传结果。
// 是否允许执行由平台「采集端配置」统一控制，改完无需重启采集端。
func commandLoop(rt *Runtime, client *http.Client, base, token string) {
	log.Printf("指令下发通道已就绪：是否允许执行由平台「采集端配置」控制")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		allowCommand := rt.Snapshot().AllowCommand
		list, err := fetchCommands(client, base, token, rt.Hostname())
		if err != nil {
			log.Printf("拉取指令失败: %v", err)
			continue
		}
		if len(list) == 0 {
			continue
		}
		var outcomes []model.CommandOutcome
		for _, cmd := range list {
			timeout := time.Duration(cmd.TimeoutSec) * time.Second
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			if !allowCommand {
				outcomes = append(outcomes, model.CommandOutcome{
					ID: cmd.ID, ExitCode: -1, ErrorMsg: "采集端已按平台配置关闭指令执行能力",
				})
				continue
			}
			code, out, err := runCommand(cmd.Cmd, timeout)
			o := model.CommandOutcome{ID: cmd.ID, ExitCode: code, Output: out}
			if err != nil {
				o.ErrorMsg = err.Error()
			}
			outcomes = append(outcomes, o)
			log.Printf("执行指令完成：%s（退出码 %d）", truncateAgent(cmd.Cmd, 60), code)
		}
		if err := postJSON(client, base+"/agent/command", &model.CommandResult{
			WgToken: token, Hostname: rt.Hostname(), Results: outcomes,
		}); err != nil {
			log.Printf("指令结果上报失败: %v", err)
		}
	}
}

// fetchCommands 向服务端请求待执行的指令。
func fetchCommands(client *http.Client, base, token, hostname string) ([]model.CommandPull, error) {
	url := fmt.Sprintf("%s/agent/commands?hostname=%s&wgToken=%s", base, urlEscape(hostname), token)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var list []model.CommandPull
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	return list, nil
}
