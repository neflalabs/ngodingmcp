package procman

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func compileReady(expr string) (*regexp.Regexp, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid wait_for_log regex: %w", err)
	}
	return re, nil
}

func (m *Manager) waitReady(p *Process, re *regexp.Regexp, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !p.isRunning() {
			info := p.info()
			return fmt.Errorf("%w: %s exited with code %d before becoming ready", ErrNotReady, p.name, info.ExitCode)
		}
		logOK := re == nil || p.logs.Match(re)
		portOK := port == 0 || portOpen(port)
		if logOK && portOK {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w after %s (log matched: %t, port %d open: %t)",
				ErrNotReady, timeout.Round(time.Millisecond), logOK, port, portOK)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func portOpen(port int) bool {
	hosts := []string{"127.0.0.1", "::1"}
	for _, host := range hosts {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}
