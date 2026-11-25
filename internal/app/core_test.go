package app

import "testing"

func TestNewCoreBuildsRuntimeWithoutFrontEnd(t *testing.T) {
	core, err := NewCore(Config{Root: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	if core.Workspace == nil || core.MCPServer == nil {
		t.Fatal("core without workspace or MCP server")
	}
	if core.Logger == nil {
		t.Fatal("core without logger")
	}
	if core.Runner != nil || core.Processes != nil || core.Downloader != nil {
		t.Fatal("opt-in helpers should be off by default")
	}
	if core.ToolCount <= 0 {
		t.Fatalf("ToolCount = %d, want > 0", core.ToolCount)
	}
}

func TestNewCoreEnablesOptInHelpers(t *testing.T) {
	core, err := NewCore(Config{
		Root:          t.TempDir(),
		Version:       "test",
		AllowRun:      true,
		AllowDownload: true,
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	if core.Runner == nil || core.Processes == nil {
		t.Fatal("run helpers missing")
	}
	if core.Downloader == nil {
		t.Fatal("downloader missing")
	}
}
