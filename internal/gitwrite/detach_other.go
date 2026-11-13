//go:build !unix

package gitwrite

import (
	"os/exec"
	"time"
)

// detach has no session support outside unix; prompts stay disabled through
// GIT_TERMINAL_PROMPT=0, ssh BatchMode and the timeout.
func detach(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
