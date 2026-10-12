//go:build windows

package agent

import (
	"os/exec"
	"syscall"
)

const (
	// createNewProcessGroup 新进程独立进程组
	createNewProcessGroup = 0x00000200
	// detachedProcess 不继承父进程控制台
	detachedProcess = 0x00000008
)

// setDetach 让新拉起的采集端脱离当前控制台，父进程退出不会把它一起关掉。
func setDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
