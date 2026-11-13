package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Commander runs the systemctl and journalctl commands. The service code only
// talks to systemd through it, so tests swap in a fake and never touch the
// real user manager.
type Commander interface {
	// Output runs a command and returns its standard output. A non-zero exit
	// comes back as an error, together with whatever the command printed.
	Output(ctx context.Context, name string, args ...string) (string, error)
	// Attach runs a command on the user's terminal (stdin, stdout, stderr),
	// for example `journalctl -f`, and waits for it to end.
	Attach(ctx context.Context, name string, args ...string) error
}

// ExecCommander runs real commands with os/exec.
type ExecCommander struct{}

// Output implements Commander.
func (ExecCommander) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		line := strings.TrimSpace(name + " " + strings.Join(args, " "))
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), fmt.Errorf("%s: %w: %s", line, err, msg)
		}
		return string(out), fmt.Errorf("%s: %w", line, err)
	}
	return string(out), nil
}

// Attach implements Commander.
func (ExecCommander) Attach(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
