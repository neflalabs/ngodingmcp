// Package selfrestart lets the ngodingmcp systemd user service restart
// itself when run_command receives the exact command "ngodingmcp restart",
// for example after the agent rebuilt the binary with go build.
//
// A restart stops the very process that is answering the tool call, so the
// restart is never run inline: Schedule only queues it and returns at once,
// and about a second later (after the tool result has been written)
// `systemctl --user --no-block restart ngodingmcp.service` hands the job to
// systemd. --no-block makes systemctl return as soon as systemd accepted the
// job, so it does not matter that systemd then stops the cgroup systemctl
// itself runs in.
//
// Only a process that really runs inside ngodingmcp.service may do this. A
// server started by hand (serve or the TUI) refuses: restarting the unit
// would start a second server instead of restarting this one.
package selfrestart

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// Unit is the systemd user unit that `ngodingmcp -d` installs. It must match
// service.Unit (internal/service imports app, so it cannot be imported here;
// a test in internal/service keeps the two in step).
const Unit = "ngodingmcp.service"

// DefaultDelay is how long Schedule waits before it asks systemd to restart,
// so the tool result reaches the client first.
const DefaultDelay = time.Second

// ErrNotService is returned when the server does not run inside Unit.
var ErrNotService = errors.New("ngodingmcp restart only works when this server runs as the systemd user service (`ngodingmcp -d`); restart a server started by hand yourself")

// Restarter schedules restarts of the service. The zero value is not usable;
// call New.
type Restarter struct {
	unit    string
	delay   time.Duration
	inUnit  func() bool
	run     func(name string, args ...string) error
	after   func(time.Duration, func())
	logger  *slog.Logger
	pending atomic.Bool
}

// New returns a Restarter for the real service.
func New(logger *slog.Logger) *Restarter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Restarter{
		unit:   Unit,
		delay:  DefaultDelay,
		inUnit: func() bool { return InUnit("/proc/self/cgroup", Unit) },
		run: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
		after:  func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		logger: logger,
	}
}

// Schedule queues a restart of the service and returns the message for the
// tool result. It returns ErrNotService when the process does not run in the
// unit. A second call while a restart is already queued does nothing.
func (r *Restarter) Schedule() (string, error) {
	if !r.inUnit() {
		return "", ErrNotService
	}
	if !r.pending.CompareAndSwap(false, true) {
		return "A restart of " + r.unit + " is already scheduled.", nil
	}
	r.after(r.delay, func() {
		args := []string{"--user", "--no-block", "restart", r.unit}
		r.logger.Info("restarting service on request from run_command", "unit", r.unit)
		if err := r.run("systemctl", args...); err != nil {
			r.logger.Error("self restart failed", "unit", r.unit, "err", err)
			r.pending.Store(false)
		}
	})
	return "Restart of " + r.unit + " scheduled in " + r.delay.String() +
		". This server stops right after this reply and systemd starts it again with the binary on disk; " +
		"tool calls fail for a few seconds, then work again without reconnecting.", nil
}

// InUnit reports whether the cgroup file (normally /proc/self/cgroup) puts
// this process inside unit, e.g. "0::/user.slice/.../app.slice/ngodingmcp.service".
func InUnit(cgroupFile, unit string) bool {
	data, err := os.ReadFile(cgroupFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		path := line
		if i := strings.LastIndex(line, ":"); i >= 0 {
			path = line[i+1:]
		}
		for _, part := range strings.Split(path, "/") {
			if part == unit {
				return true
			}
		}
	}
	return false
}
