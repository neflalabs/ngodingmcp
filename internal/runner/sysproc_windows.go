//go:build windows

package runner

import (
	"os"
	"os/exec"
	"time"
)

// ConfigureProcessGroup is a no-op on Windows; the process itself is managed.
func ConfigureProcessGroup(cmd *exec.Cmd) {}

// TerminateProcessGroup stops the process.
func TerminateProcessGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// KillProcessGroup force-kills the process.
func KillProcessGroup(pid int) error {
	return TerminateProcessGroup(pid)
}

// setProcessGroup keeps the default process handling on Windows.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
