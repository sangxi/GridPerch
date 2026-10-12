//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

// setDetachSelf 让新拉起的服务端脱离当前会话，父进程退出不会把它一起带走。
func setDetachSelf(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
