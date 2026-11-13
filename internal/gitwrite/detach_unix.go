//go:build unix

package gitwrite

import (
	"os/exec"
	"syscall"
	"time"
)

// detach starts a network git command in a new session with no controlling
// terminal, so neither git nor ssh can open /dev/tty to ask for a password
// or passphrase. On timeout the whole session (git plus its ssh or
// credential-helper children) is killed, and WaitDelay makes sure Wait
// returns even if a child still holds the output pipes.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
