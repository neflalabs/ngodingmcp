package app

import (
	"strings"
	"testing"
)

func TestNewUsesDefaultRunPolicy(t *testing.T) {
	srv, err := New(Config{
		Root:     t.TempDir(),
		Addr:     "127.0.0.1:0",
		Version:  "test",
		AllowRun: true,
	})
	if err != nil {
		t.Fatalf("New with default run policy: %v", err)
	}
	if srv.Root() == "" {
		t.Error("empty root")
	}
}

func TestNewRunDisabledNeedsNoPolicy(t *testing.T) {
	if _, err := New(Config{Root: t.TempDir(), Addr: "127.0.0.1:0", Version: "test"}); err != nil {
		t.Fatalf("New without run policy: %v", err)
	}
}

func TestNewDisableTools(t *testing.T) {
	if _, err := New(Config{Root: t.TempDir(), Addr: "127.0.0.1:0", Version: "test",
		DisableTools: []string{"inspect", "git_remote"}}); err != nil {
		t.Fatalf("New with disabled modules: %v", err)
	}
	_, err := New(Config{Root: t.TempDir(), Addr: "127.0.0.1:0", Version: "test", DisableTools: []string{"gitremote"}})
	if err == nil || !strings.Contains(err.Error(), "unknown tool module") {
		t.Fatalf("unknown module: err = %v, want an error", err)
	}
}
