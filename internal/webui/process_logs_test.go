package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/workspace"
)

const webuiHelperEnv = "WEBUI_HELPER_PROCESS"

// TestWebUIHelperProcess is not a real test: the process manager re-executes
// the test binary as a stand-in for a long-running dev server.
func TestWebUIHelperProcess(t *testing.T) {
	if os.Getenv(webuiHelperEnv) != "1" {
		return
	}
	fmt.Println("hello one")
	fmt.Println("hello two")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

type procAPI struct {
	handler http.Handler // /api/ mount, with auth, as in app.go
	mgr     *procman.Manager
}

const testToken = "test-token-123"

func newProcAPI(t *testing.T) procAPI {
	t.Helper()
	t.Setenv(webuiHelperEnv, "1")
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Options{Root: root, Allowlist: []string{os.Args[0]}})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := procman.New(procman.Options{Root: root, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	authn := auth.New(testToken, "session")
	ui := New(ws, authn, "test")
	ui.SetProcesses(mgr)
	mux := http.NewServeMux()
	mux.Handle("/api/", authn.API(http.StripPrefix("/api", ui.API())))
	return procAPI{handler: mux, mgr: mgr}
}

func (p procAPI) do(t *testing.T, method, path string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if authed {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	return rec
}

func (p procAPI) startHelper(t *testing.T, name string) {
	t.Helper()
	_, err := p.mgr.StartWithOptions(procman.StartOptions{
		Name:         name,
		Argv:         []string{os.Args[0], "-test.run=TestWebUIHelperProcess"},
		ReadyLog:     "hello two",
		ReadyTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("start helper: %v", err)
	}
}

type logsResp struct {
	Process procman.Info       `json:"process"`
	Logs    procman.LogsResult `json:"logs"`
}

func decodeLogs(t *testing.T, rec *httptest.ResponseRecorder) logsResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out logsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestProcessLogsStopRestartRequireAuth(t *testing.T) {
	p := newProcAPI(t)
	p.startHelper(t, "dev")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/processes/dev/logs"},
		{http.MethodPost, "/api/processes/dev/stop"},
		{http.MethodPost, "/api/processes/dev/restart"},
	} {
		if rec := p.do(t, c.method, c.path, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without auth = %d, want 401", c.method, c.path, rec.Code)
		}
	}
	// Nothing was stopped by the unauthenticated requests.
	info, err := p.mgr.Get("dev")
	if err != nil || !info.Running {
		t.Fatalf("process should still run: %+v err=%v", info, err)
	}
}

func TestProcessLogsIncremental(t *testing.T) {
	p := newProcAPI(t)
	p.startHelper(t, "dev")

	first := decodeLogs(t, p.do(t, http.MethodGet, "/api/processes/dev/logs?tail=100", true))
	var text []string
	for _, l := range first.Logs.Lines {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "hello one") || !strings.Contains(joined, "hello two") {
		t.Fatalf("logs missing helper output: %q", joined)
	}
	if first.Process.Name != "dev" || !first.Process.Running {
		t.Errorf("process info = %+v", first.Process)
	}
	if first.Logs.NextSeq <= 0 {
		t.Fatalf("nextSeq = %d, want > 0", first.Logs.NextSeq)
	}

	// Passing the cursor returns only newer lines: none yet, as an array.
	rec := p.do(t, http.MethodGet, fmt.Sprintf("/api/processes/dev/logs?after=%d", first.Logs.NextSeq), true)
	if strings.Contains(rec.Body.String(), `"lines":null`) {
		t.Error("lines should be an empty array, not null")
	}
	next := decodeLogs(t, rec)
	if len(next.Logs.Lines) != 0 {
		t.Errorf("incremental read returned old lines: %+v", next.Logs.Lines)
	}
	if next.Logs.NextSeq != first.Logs.NextSeq {
		t.Errorf("nextSeq moved without output: %d -> %d", first.Logs.NextSeq, next.Logs.NextSeq)
	}
}

func TestProcessLogsBadInputAndUnknown(t *testing.T) {
	p := newProcAPI(t)
	for _, path := range []string{
		"/api/processes/dev/logs?tail=abc",
		"/api/processes/dev/logs?tail=-1",
		"/api/processes/dev/logs?after=-5",
	} {
		if rec := p.do(t, http.MethodGet, path, true); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := p.do(t, http.MethodGet, "/api/processes/missing/logs", true); rec.Code != http.StatusNotFound {
		t.Errorf("logs for unknown process = %d, want 404", rec.Code)
	}
	if rec := p.do(t, http.MethodPost, "/api/processes/missing/restart", true); rec.Code != http.StatusNotFound {
		t.Errorf("restart unknown process = %d, want 404", rec.Code)
	}
}

func TestProcessRestartAndStop(t *testing.T) {
	p := newProcAPI(t)
	p.startHelper(t, "dev")
	before, err := p.mgr.Get("dev")
	if err != nil {
		t.Fatal(err)
	}

	rec := p.do(t, http.MethodPost, "/api/processes/dev/restart", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("restart = %d: %s", rec.Code, rec.Body.String())
	}
	var restarted procman.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &restarted); err != nil {
		t.Fatal(err)
	}
	if !restarted.Running || restarted.PID == before.PID {
		t.Errorf("restart should run a new PID: before %d, after %+v", before.PID, restarted)
	}

	rec = p.do(t, http.MethodPost, "/api/processes/dev/stop", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := p.mgr.Get("dev")
		if err != nil || !info.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process still running 5s after stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
