package procman

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSwitchRootStopsAndForgetsProcesses(t *testing.T) {
	m, _ := newManagerRoot(t, 0)

	if _, err := m.Start("sleeper", helperArgv("sleep")); err != nil {
		t.Fatalf("Start sleeper: %v", err)
	}
	if _, err := m.Start("logger", helperArgv("logs")); err != nil {
		t.Fatalf("Start logger: %v", err)
	}
	waitExit(t, m, "logger")

	newRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.run.SetRoot(newRoot) // the app moves the runner in the same step

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := m.SwitchRoot(ctx, newRoot, 2*time.Second)
	if len(stopped) != 1 || stopped[0] != "sleeper" {
		t.Errorf("stopped = %v, want [sleeper]", stopped)
	}
	if list := m.List(); len(list) != 0 {
		t.Errorf("processes left after SwitchRoot: %+v", list)
	}
	if m.Root() != newRoot {
		t.Errorf("Root = %q, want %q", m.Root(), newRoot)
	}

	// New processes start in the new root.
	if _, err := m.Start("inspect", helperArgv("inspect")); err != nil {
		t.Fatalf("Start after switch: %v", err)
	}
	waitExit(t, m, "inspect")
	_, res, err := m.Logs("inspect", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range res.Lines {
		if strings.HasPrefix(l.Text, "cwd=") {
			found = true
			if got := strings.TrimPrefix(l.Text, "cwd="); got != newRoot {
				t.Errorf("cwd = %q, want %q", got, newRoot)
			}
		}
	}
	if !found {
		t.Errorf("no cwd line in %+v", res.Lines)
	}
}
