//go:build windows

package scaffold

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func prepareGeneratedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func cancelGeneratedProcess(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}
