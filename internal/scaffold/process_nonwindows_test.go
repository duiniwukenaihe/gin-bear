//go:build !windows

package scaffold

import (
	"os"
	"os/exec"
)

func prepareGeneratedProcess(_ *exec.Cmd) {}

func cancelGeneratedProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}
