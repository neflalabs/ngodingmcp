package procman

import (
	"errors"
	"ngodingmcp/internal/runner"
	"os/exec"
	"sync"
	"time"
)

// Process is one supervised child process.
type Process struct {
	name      string
	argv      []string
	dir       string
	env       map[string]string
	cmd       *exec.Cmd
	tree      *processTree
	logs      *logBuffer
	startedAt time.Time
	done      chan struct{}

	mu       sync.Mutex
	pid      int
	running  bool
	stopping bool
	ready    bool
	exitCode int
	errMsg   string
	endedAt  time.Time
}

func (p *Process) wait() {
	err := p.cmd.Wait()

	p.mu.Lock()
	p.running = false
	p.endedAt = time.Now()
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrWaitDelay):
			// A daemonized descendant held the log pipes open. The direct
			// child did exit, so keep its real status, not a fake error.
			p.exitCode = p.cmd.ProcessState.ExitCode()
		case errors.As(err, &exitErr):
			p.exitCode = exitErr.ExitCode()
		default:
			p.exitCode = -1
			p.errMsg = err.Error()
		}
	}
	if p.tree != nil {
		p.tree.Close()
		p.tree = nil
	}
	p.mu.Unlock()
	close(p.done)
}

func (p *Process) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Process) endedTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.endedAt
}

func (p *Process) setReady() {
	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
}

func (p *Process) terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tree == nil {
		return nil
	}
	return p.tree.Terminate()
}

func (p *Process) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tree == nil {
		return nil
	}
	return p.tree.Kill()
}

// escalate force-kills a process that ignored the graceful signal.
func (p *Process) escalate(grace time.Duration) {
	select {
	case <-p.done:
		return
	case <-time.After(grace):
		_ = p.kill()
	}
}

func (p *Process) info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked()
}

func (p *Process) infoLocked() Info {
	info := Info{
		Name:       p.name,
		Argv:       append([]string(nil), p.argv...),
		Dir:        p.dir,
		PID:        p.pid,
		Running:    p.running,
		Stopping:   p.stopping,
		Ready:      p.ready,
		ExitCode:   p.exitCode,
		Error:      p.errMsg,
		StartedAt:  p.startedAt,
		LogBytes:   p.logs.Len(),
		LogDropped: p.logs.Dropped(),
	}
	end := p.endedAt
	if p.running {
		end = time.Now()
	}
	if !end.IsZero() {
		info.DurationMS = end.Sub(p.startedAt).Milliseconds()
		info.EndedAt = p.endedAt
	}
	return info
}

// ArgvString renders the command for messages.
func (i Info) ArgvString() string { return runner.FormatArgv(i.Argv) }
