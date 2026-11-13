//go:build unix

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// ConfigureProcessGroup starts the command in its own process group so it can
// be terminated together with the children it spawns.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// TerminateProcessGroup asks the whole process group to stop gracefully.
func TerminateProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

// KillProcessGroup force-kills the whole process group.
func KillProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// setProcessGroup wires exec cancellation to the process group. Without this a
// timed-out command could leave orphaned workers behind.
func setProcessGroup(cmd *exec.Cmd) {
	ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return KillProcessGroup(cmd.Process.Pid)
	}
	cmd.WaitDelay = 5 * time.Second
}
