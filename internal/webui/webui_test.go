package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ngodingmcp/internal/auth"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/workspace"
)

func TestProcessEndpoints(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Options{Root: root, Allowlist: []string{"go test"}})
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

	ui := New(ws, auth.New("token", "session"), "test")
	ui.SetProcesses(mgr)
	api := ui.API()

	// GET /processes returns an empty array, not null.
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/processes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /processes = %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Processes []procman.Info `json:"processes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Processes == nil {
		t.Error("processes should be an empty array, not null")
	}

	// Unknown names map to 404.
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/processes/missing/stop", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST stop missing = %d, want 404", rec.Code)
	}

	// Stop-all succeeds even with no processes.
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/processes/stop-all", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("POST stop-all = %d: %s", rec.Code, rec.Body.String())
	}
}
