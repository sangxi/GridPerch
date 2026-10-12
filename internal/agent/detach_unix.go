//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setDetach 让新拉起的采集端脱离当前会话，父进程退出不会带它一起走。
func setDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
