//go:build unix

package procman

import (
	"os/exec"
	"syscall"
)

// prepareCmd starts the process in its own process group so the whole tree can
// be signalled together.
func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type processTree struct {
	pid int
}

func attachTree(pid int) (*processTree, error) {
	return &processTree{pid: pid}, nil
}

// Terminate asks the whole process group to stop gracefully.
func (t *processTree) Terminate() error {
	if t.pid <= 0 {
		return nil
	}
	return syscall.Kill(-t.pid, syscall.SIGTERM)
}

// Kill force-kills the whole process group.
func (t *processTree) Kill() error {
	if t.pid <= 0 {
		return nil
	}
	return syscall.Kill(-t.pid, syscall.SIGKILL)
}

func (t *processTree) Close() {}
