package procman

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"ngodingmcp/internal/runner"
)

const helperEnv = "PROCMAN_HELPER_PROCESS"

// TestHelperProcess is not a real test. It is re-executed by the manager as a
// stand-in for a long-running command.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "logs":
		fmt.Println("line one")
		fmt.Println("line two")
		fmt.Println("line three")
		os.Exit(0)
	case "ansi":
		fmt.Print("\x1b[31mred\x1b[0m\rprogress\n")
		os.Exit(0)
	case "exit":
		fmt.Println("exiting")
		os.Exit(3)
	case "sleep":
		fmt.Println("sleeping")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "ready":
		fmt.Println("booting")
		time.Sleep(200 * time.Millisecond)
		fmt.Println("ready to serve")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "slow":
		fmt.Println("started")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "inspect":
		wd, _ := os.Getwd()
		fmt.Printf("cwd=%s\n", wd)
		fmt.Printf("PORT=%s\n", os.Getenv("PORT"))
		os.Exit(0)
	case "listen":
		port := os.Getenv("PORT")
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			fmt.Println("listen error:", err)
			os.Exit(1)
		}
		fmt.Println("listening")
		for {
			conn, err := ln.Accept()
			if err != nil {
				os.Exit(0)
			}
			_ = conn.Close()
		}
	}
	os.Exit(2)
}

func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=TestHelperProcess", "--", mode}
}

func newManager(t *testing.T, max int) *Manager {
	t.Helper()
	m, _ := newManagerRoot(t, max)
	return m
}

func newManagerRoot(t *testing.T, max int) (*Manager, string) {
	t.Helper()
	t.Setenv(helperEnv, "1")
	root := t.TempDir()
	r, err := runner.New(runner.Options{
		Root:      root,
		Allowlist: []string{os.Args[0]},
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	m, err := New(Options{Root: root, Runner: r, MaxProcesses: max})
	if err != nil {
		t.Fatalf("procman.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m, root
}

func TestStartLogsAndExitStatus(t *testing.T) {
	m := newManager(t, 0)

	info, err := m.Start("logger", helperArgv("logs"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info.PID <= 0 {
		t.Errorf("PID = %d", info.PID)
	}
	if !info.Running {
		t.Errorf("process should be running right after Start")
	}
	if !info.Ready {
		t.Errorf("process without readiness checks should be ready")
	}
	waitExit(t, m, "logger")

	info, err = m.Get("logger")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", info.ExitCode)
	}

	_, res, err := m.Logs("logger", 2, 0)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[0].Text != "line two" || res.Lines[1].Text != "line three" {
		t.Errorf("tail lines = %+v", res.Lines)
	}
	if res.NextSeq != 3 {
		t.Errorf("NextSeq = %d, want 3", res.NextSeq)
	}
	if res.Lines[0].Time.IsZero() {
		t.Error("log line has zero timestamp")
	}

	// Cursor read after the last line must return nothing.
	_, res, err = m.Logs("logger", 0, res.NextSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 0 {
		t.Errorf("cursor read returned %d lines, want 0", len(res.Lines))
	}
}

func TestLogCursorSkipsOlderLines(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("logger", helperArgv("logs")); err != nil {
		t.Fatal(err)
	}
	waitExit(t, m, "logger")

	_, first, err := m.Logs("logger", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Lines) != 1 || first.Lines[0].Text != "line three" {
		t.Fatalf("tail = %+v", first.Lines)
	}

	_, all, err := m.Logs("logger", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, newer, err := m.Logs("logger", 0, all.Lines[0].Seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(newer.Lines) != 2 {
		t.Errorf("lines after first = %d, want 2", len(newer.Lines))
	}
}

func TestLogsStripANSI(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("ansi", helperArgv("ansi")); err != nil {
		t.Fatal(err)
	}
	waitExit(t, m, "ansi")

	_, res, err := m.Logs("ansi", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].Text != "progress" {
		t.Errorf("sanitized lines = %+v", res.Lines)
	}
}

func TestStartNonZeroExit(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("bad", helperArgv("exit")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitExit(t, m, "bad")
	info, err := m.Get("bad")
	if err != nil {
		t.Fatal(err)
	}
	if info.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", info.ExitCode)
	}
}

func TestStartAutoNameAvoidsCollision(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("proc-1", helperArgv("sleep")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	info, err := m.Start("", helperArgv("sleep"))
	if err != nil {
		t.Fatalf("auto Start: %v", err)
	}
	if info.Name != "proc-2" {
		t.Errorf("auto name = %q, want proc-2", info.Name)
	}

	if _, err := m.Start("proc-1", helperArgv("sleep")); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate err = %v, want ErrExists", err)
	}
}

func TestStartDeniedByAllowlist(t *testing.T) {
	t.Setenv(helperEnv, "1")
	r, err := runner.New(runner.Options{
		Root:      t.TempDir(),
		Allowlist: []string{"go test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Root: t.TempDir(), Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("x", []string{"sh", "-c", "echo pwned"}); !errors.Is(err, runner.ErrNotAllowed) {
		t.Errorf("err = %v, want ErrNotAllowed", err)
	}
}

func TestStartInvalidName(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("bad name!", helperArgv("sleep")); err == nil {
		t.Error("expected invalid name error")
	}
}

func TestStartCwdAndEnv(t *testing.T) {
	m, root := newManagerRoot(t, 0)
	sub := root + string(os.PathSeparator) + "apps"
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := m.StartWithOptions(StartOptions{
		Name: "inspect",
		Argv: helperArgv("inspect"),
		Dir:  "apps",
		Env:  map[string]string{"PORT": "4567"},
	})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	if info.Dir != sub {
		t.Errorf("Dir = %q, want %q", info.Dir, sub)
	}
	waitExit(t, m, "inspect")

	_, res, err := m.Logs("inspect", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, l := range res.Lines {
		out.WriteString(l.Text + "\n")
	}
	if !strings.Contains(out.String(), "cwd="+sub) {
		t.Errorf("logs missing cwd: %s", out.String())
	}
	if !strings.Contains(out.String(), "PORT=4567") {
		t.Errorf("logs missing PORT: %s", out.String())
	}
}

func TestStartRejectsUnsafeOptions(t *testing.T) {
	m := newManager(t, 0)

	if _, err := m.StartWithOptions(StartOptions{Name: "bad-env", Argv: helperArgv("sleep"), Env: map[string]string{"LD_PRELOAD": "/tmp/x"}}); !errors.Is(err, ErrInvalidEnv) {
		t.Errorf("env err = %v, want ErrInvalidEnv", err)
	}
	if _, err := m.StartWithOptions(StartOptions{Name: "bad-dir", Argv: helperArgv("sleep"), Dir: "../.."}); !errors.Is(err, ErrInvalidDir) {
		t.Errorf("dir err = %v, want ErrInvalidDir", err)
	}
	if _, err := m.StartWithOptions(StartOptions{Name: "bad-re", Argv: helperArgv("sleep"), ReadyLog: "["}); err == nil {
		t.Error("expected invalid regex error")
	}
	if _, err := m.StartWithOptions(StartOptions{Name: "bad-port", Argv: helperArgv("sleep"), ReadyPort: 70000}); !errors.Is(err, ErrInvalidPort) {
		t.Errorf("port err = %v, want ErrInvalidPort", err)
	}
}

func TestWaitForLog(t *testing.T) {
	m := newManager(t, 0)
	start := time.Now()
	info, err := m.StartWithOptions(StartOptions{
		Name:         "ready",
		Argv:         helperArgv("ready"),
		ReadyLog:     "ready to serve",
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	if !info.Ready {
		t.Error("Ready = false")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("returned too early (%s); readiness wait did not happen", elapsed)
	}
}

func TestWaitForLogTimeout(t *testing.T) {
	m := newManager(t, 0)
	info, err := m.StartWithOptions(StartOptions{
		Name:         "never",
		Argv:         helperArgv("sleep"),
		ReadyLog:     "never appears",
		ReadyTimeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if info.Name != "never" {
		t.Errorf("Info.Name = %q, want never", info.Name)
	}
	if !info.Running {
		t.Error("process should stay running after readiness timeout")
	}
}

func TestWaitForPort(t *testing.T) {
	m := newManager(t, 0)
	port := freePort(t)
	_, err := m.StartWithOptions(StartOptions{
		Name:         "http",
		Argv:         helperArgv("listen"),
		Env:          map[string]string{"PORT": fmt.Sprintf("%d", port)},
		ReadyPort:    port,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestStartTooManyRunning(t *testing.T) {
	m := newManager(t, 2)
	for i := 0; i < 2; i++ {
		if _, err := m.Start("", helperArgv("sleep")); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	if _, err := m.Start("", helperArgv("sleep")); !errors.Is(err, ErrTooMany) {
		t.Errorf("err = %v, want ErrTooMany", err)
	}
}

func TestStopRunning(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("sleeper", helperArgv("sleep")); err != nil {
		t.Fatalf("Start: %v", err)
	}

	info, err := m.Stop("sleeper", 2*time.Second, true)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if info.Running {
		t.Error("process still running after Stop")
	}
	if info.EndedAt.IsZero() {
		t.Error("EndedAt is zero")
	}
}

func TestStopNoWait(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("sleeper", helperArgv("sleep")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := m.Stop("sleeper", 2*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("wait=false took %s, want immediate return", elapsed)
	}
	waitExit(t, m, "sleeper")
}

func TestRestart(t *testing.T) {
	m := newManager(t, 0)
	first, err := m.Start("svc", helperArgv("slow"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Restart("svc", 2*time.Second)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if second.PID == first.PID {
		t.Errorf("PID = %d, want a new process", second.PID)
	}
	if !second.Running {
		t.Error("restarted process is not running")
	}
	if second.Name != "svc" {
		t.Errorf("Name = %q", second.Name)
	}
}

func TestStopAll(t *testing.T) {
	m := newManager(t, 0)
	for _, name := range []string{"a", "b"} {
		if _, err := m.Start(name, helperArgv("sleep")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	procs := m.StopAll(ctx, 2*time.Second)
	for _, p := range procs {
		if p.Running {
			t.Errorf("%s still running", p.Name)
		}
	}
}

func TestStopUnknown(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Stop("nope", 0, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := m.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
	if _, _, err := m.Logs("nope", 10, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Logs err = %v, want ErrNotFound", err)
	}
}

func TestLogBufferDropsOldOutput(t *testing.T) {
	b := newLogBuffer(8)
	if _, err := b.Write([]byte("12345\n67890\nabcde\n")); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 8 {
		t.Errorf("Len = %d, want <= 8", b.Len())
	}
	if !b.Dropped() {
		t.Error("Dropped = false, want true")
	}
	res := b.Read(100, 0)
	if len(res.Lines) != 1 || res.Lines[0].Text != "abcde" {
		t.Errorf("lines = %+v", res.Lines)
	}
	if !res.Dropped {
		t.Error("result should report dropped output")
	}
	if res.NextSeq != 3 {
		t.Errorf("NextSeq = %d, want 3", res.NextSeq)
	}
}

func TestLogBufferPartialLine(t *testing.T) {
	b := newLogBuffer(1024)
	if _, err := b.Write([]byte("no newline yet")); err != nil {
		t.Fatal(err)
	}
	res := b.Read(100, 0)
	if len(res.Lines) != 1 || res.Lines[0].Text != "no newline yet" {
		t.Errorf("partial = %+v", res.Lines)
	}
	// Continuing the line must not duplicate the partial text.
	if _, err := b.Write([]byte(" done\n")); err != nil {
		t.Fatal(err)
	}
	res = b.Read(100, 0)
	if len(res.Lines) != 1 || res.Lines[0].Text != "no newline yet done" {
		t.Errorf("completed = %+v", res.Lines)
	}
}

func TestShutdownStopsProcesses(t *testing.T) {
	m := newManager(t, 0)
	if _, err := m.Start("sleeper", helperArgv("sleep")); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	info, err := m.Get("sleeper")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Running {
		t.Error("process still running after Shutdown")
	}
	if _, err := m.Start("late", helperArgv("sleep")); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Shutdown = %v, want ErrClosed", err)
	}
}

func waitExit(t *testing.T, m *Manager, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := m.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not exit", name)
}
