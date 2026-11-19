package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/mcpserver"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/workspace"
)

func setupTestServerAndClient(t *testing.T) (*mcp.ClientSession, *workspace.Workspace) {
	t.Helper()

	tempDir := t.TempDir()
	ws, err := workspace.New(tempDir, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	server := mcpserver.New(ws, "v2026.11.1")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	_, err = server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	return clientSession, ws
}

func TestServer_CoreToolsRegisteredAndSchemas(t *testing.T) {
	clientSession, _ := setupTestServerAndClient(t)
	ctx := context.Background()

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	expectedTools := map[string]struct {
		isReadOnly    bool
		isDestructive *bool
	}{
		"read_file":              {isReadOnly: true, isDestructive: nil},
		"outline":                {isReadOnly: true, isDestructive: nil},
		"read_symbol":            {isReadOnly: true, isDestructive: nil},
		"read_files":             {isReadOnly: true, isDestructive: nil},
		"write_file":             {isReadOnly: false, isDestructive: boolPtr(true)},
		"replace_text":           {isReadOnly: false, isDestructive: boolPtr(true)},
		"insert_text":            {isReadOnly: false, isDestructive: boolPtr(false)},
		"delete_lines":           {isReadOnly: false, isDestructive: boolPtr(true)},
		"replace_lines":          {isReadOnly: false, isDestructive: boolPtr(true)},
		"list_files":             {isReadOnly: true, isDestructive: nil},
		"search_text":            {isReadOnly: true, isDestructive: nil},
		"file_info":              {isReadOnly: true, isDestructive: nil},
		"workspace_stats":        {isReadOnly: true, isDestructive: nil},
		"validate_syntax":        {isReadOnly: true, isDestructive: nil},
		"count_tokens":           {isReadOnly: true, isDestructive: nil},
		"search_and_replace":     {isReadOnly: false, isDestructive: boolPtr(true)},
		"mkdir_path":             {isReadOnly: false, isDestructive: boolPtr(false)},
		"move_path":              {isReadOnly: false, isDestructive: boolPtr(true)},
		"delete_path":            {isReadOnly: false, isDestructive: boolPtr(true)},
		"edit_file":              {isReadOnly: false, isDestructive: boolPtr(true)},
		"git_status":             {isReadOnly: true, isDestructive: nil},
		"git_diff":               {isReadOnly: true, isDestructive: nil},
		"git_log":                {isReadOnly: true, isDestructive: nil},
		"git_init":               {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_add":                {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_unstage":            {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_commit":             {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_create_branch":      {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_switch_branch":      {isReadOnly: false, isDestructive: boolPtr(true)},
		"git_remote":             {isReadOnly: false, isDestructive: boolPtr(true)},
		"git_fetch":              {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_pull":               {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_push":               {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_merge":              {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_github_create_repo": {isReadOnly: false, isDestructive: boolPtr(false)},
		"git_github_runs":        {isReadOnly: true, isDestructive: nil},
		"git_github_run_view":    {isReadOnly: true, isDestructive: nil},
		"git_github_run_rerun":   {isReadOnly: false, isDestructive: boolPtr(false)},
	}

	if len(res.Tools) != len(expectedTools) {
		t.Fatalf("expected %d tools registered, got %d", len(expectedTools), len(res.Tools))
	}

	toolMap := make(map[string]*mcp.Tool)
	for _, tool := range res.Tools {
		toolMap[tool.Name] = tool
	}

	for name, expected := range expectedTools {
		tool, exists := toolMap[name]
		if !exists {
			t.Errorf("tool %q is missing from registered tools", name)
			continue
		}

		if tool.Description == "" {
			t.Errorf("tool %q has empty description", name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", name)
		}

		// Check Annotations
		if tool.Annotations == nil {
			t.Errorf("tool %q has nil Annotations", name)
			continue
		}

		if tool.Annotations.ReadOnlyHint != expected.isReadOnly {
			t.Errorf("tool %q ReadOnlyHint = %v, want %v", name, tool.Annotations.ReadOnlyHint, expected.isReadOnly)
		}

		if expected.isDestructive != nil {
			if tool.Annotations.DestructiveHint == nil {
				t.Errorf("tool %q DestructiveHint is nil, want %v", name, *expected.isDestructive)
			} else if *tool.Annotations.DestructiveHint != *expected.isDestructive {
				t.Errorf("tool %q DestructiveHint = %v, want %v", name, *tool.Annotations.DestructiveHint, *expected.isDestructive)
			}
		}
	}
}

func TestServer_ToolCallsAndErrorHandling(t *testing.T) {
	clientSession, ws := setupTestServerAndClient(t)
	ctx := context.Background()

	// 1. write_file
	writeRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "write_file",
		Arguments: mustMarshal(t, map[string]any{
			"path":    "hello.go",
			"content": "package main\n\nfunc main() {\n\tprintln(\"hello world\")\n}\n",
		}),
	})
	if err != nil {
		t.Fatalf("write_file CallTool: %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("write_file returned tool error: %+v", writeRes)
	}

	// 2. read_file
	readRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_file",
		Arguments: mustMarshal(t, map[string]any{
			"path": "hello.go",
		}),
	})
	if err != nil {
		t.Fatalf("read_file CallTool: %v", err)
	}
	if readRes.IsError {
		t.Fatalf("read_file returned tool error: %+v", readRes)
	}
	text := getTextContent(readRes)
	if !strings.Contains(text, "hello.go") || !strings.Contains(text, "lines 1-5") {
		t.Errorf("unexpected read_file text output: %s", text)
	}

	// 3. validate_syntax (valid Go)
	valRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "validate_syntax",
		Arguments: mustMarshal(t, map[string]any{
			"path": "hello.go",
		}),
	})
	if err != nil {
		t.Fatalf("validate_syntax CallTool: %v", err)
	}
	if valRes.IsError {
		t.Fatalf("validate_syntax returned tool error: %+v", valRes)
	}
	if !strings.Contains(getTextContent(valRes), "syntax OK") {
		t.Errorf("expected 'syntax OK', got: %s", getTextContent(valRes))
	}

	// 4. count_tokens
	tokRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "count_tokens",
		Arguments: mustMarshal(t, map[string]any{
			"paths": []string{"hello.go"},
		}),
	})
	if err != nil {
		t.Fatalf("count_tokens CallTool: %v", err)
	}
	if tokRes.IsError {
		t.Fatalf("count_tokens returned tool error: %+v", tokRes)
	}
	if !strings.Contains(getTextContent(tokRes), "~tokens") {
		t.Errorf("expected table header with ~tokens, got: %s", getTextContent(tokRes))
	}

	// 5. workspace_stats
	statsRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "workspace_stats",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("workspace_stats CallTool: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("workspace_stats returned tool error: %+v", statsRes)
	}
	if !strings.Contains(getTextContent(statsRes), "total: 1 files") {
		t.Errorf("expected stats summary, got: %s", getTextContent(statsRes))
	}

	// 6. replace_lines
	repLinesRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "replace_lines",
		Arguments: mustMarshal(t, map[string]any{
			"path":        "hello.go",
			"from":        4,
			"to":          4,
			"new_content": "\tprintln(\"goodbye world\")\n",
		}),
	})
	if err != nil {
		t.Fatalf("replace_lines CallTool: %v", err)
	}
	if repLinesRes.IsError {
		t.Fatalf("replace_lines returned tool error: %+v", repLinesRes)
	}
	if !strings.Contains(getTextContent(repLinesRes), "replaced lines 4-4") {
		t.Errorf("expected replacement confirmation, got: %s", getTextContent(repLinesRes))
	}

	// 7. search_and_replace (dry_run=true)
	srDryRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_and_replace",
		Arguments: mustMarshal(t, map[string]any{
			"pattern":     "goodbye",
			"replacement": "bonjour",
			"dry_run":     true,
		}),
	})
	if err != nil {
		t.Fatalf("search_and_replace dry_run CallTool: %v", err)
	}
	if srDryRes.IsError {
		t.Fatalf("search_and_replace dry_run returned tool error: %+v", srDryRes)
	}
	if !strings.Contains(getTextContent(srDryRes), "[DRY RUN]") {
		t.Errorf("expected DRY RUN prefix, got: %s", getTextContent(srDryRes))
	}

	// 8. search_and_replace (real run)
	srRealRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_and_replace",
		Arguments: mustMarshal(t, map[string]any{
			"pattern":     "goodbye",
			"replacement": "bonjour",
			"dry_run":     false,
		}),
	})
	if err != nil {
		t.Fatalf("search_and_replace real run CallTool: %v", err)
	}
	if srRealRes.IsError {
		t.Fatalf("search_and_replace real run returned tool error: %+v", srRealRes)
	}
	if !strings.Contains(getTextContent(srRealRes), "replaced \"goodbye\" → \"bonjour\"") {
		t.Errorf("expected replacement summary, got: %s", getTextContent(srRealRes))
	}

	// Verify content was actually modified on disk
	content, err := os.ReadFile(filepath.Join(ws.Root(), "hello.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "bonjour world") {
		t.Errorf("expected file to contain 'bonjour world', got: %s", string(content))
	}

	// 9. Error Handling: Call with invalid range
	errRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "replace_lines",
		Arguments: mustMarshal(t, map[string]any{
			"path":        "hello.go",
			"from":        10,
			"to":          5,
			"new_content": "invalid",
		}),
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !errRes.IsError {
		t.Errorf("expected IsError=true for invalid line range, got false")
	}
	errText := getTextContent(errRes)
	if !strings.Contains(errText, "invalid line range") {
		t.Errorf("expected informative error message about line range, got: %s", errText)
	}

	// 10. Call mkdir_path, move_path, delete_path
	mkRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mkdir_path",
		Arguments: mustMarshal(t, map[string]any{"path": "subdir/nested"}),
	})
	if err != nil || mkRes.IsError {
		t.Fatalf("mkdir_path error: %v, %+v", err, mkRes)
	}

	mvRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "move_path",
		Arguments: mustMarshal(t, map[string]any{"from": "hello.go", "to": "subdir/nested/moved.go"}),
	})
	if err != nil || mvRes.IsError {
		t.Fatalf("move_path error: %v, %+v", err, mvRes)
	}

	delRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "delete_path",
		Arguments: mustMarshal(t, map[string]any{"path": "subdir", "recursive": true}),
	})
	if err != nil || delRes.IsError {
		t.Fatalf("delete_path error: %v, %+v", err, delRes)
	}

	// 11. Error Handling: Outside root escape
	escRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_file",
		Arguments: mustMarshal(t, map[string]any{
			"path": "../../../etc/passwd",
		}),
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !escRes.IsError {
		t.Errorf("expected IsError=true for path outside root, got false")
	}
	escText := getTextContent(escRes)
	if !strings.Contains(escText, "outside the workspace root") {
		t.Errorf("expected error containing 'outside the workspace root', got: %s", escText)
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestServer_RunCommandOptIn(t *testing.T) {
	tempDir := t.TempDir()
	ws, err := workspace.New(tempDir, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	// Without a runner, run_command must not be exposed.
	plain := mcpserver.New(ws, "test")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := plain.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "run_command" {
			t.Fatal("run_command should not be registered without a runner")
		}
	}

	cmdRunner, err := runner.New(runner.Options{
		Root:           tempDir,
		ExactAllowlist: []string{"go env GOOS"},
		Timeout:        10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	withRunner := mcpserver.NewWithRunner(ws, "test", cmdRunner)
	serverTransport2, clientTransport2 := mcp.NewInMemoryTransports()
	if _, err := withRunner.Connect(ctx, serverTransport2, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client2 := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session2, err := client2.Connect(ctx, clientTransport2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	runRes, err := session2.CallTool(ctx, &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: mustMarshal(t, map[string]any{"command": "go env", "args": []string{"GOOS"}}),
	})
	if err != nil {
		t.Fatalf("run_command CallTool: %v", err)
	}
	if runRes.IsError {
		t.Fatalf("run_command returned tool error: %s", getTextContent(runRes))
	}
	text := getTextContent(runRes)
	if !strings.Contains(text, "exit code: 0") {
		t.Errorf("expected success output, got: %s", text)
	}

	denied, err := session2.CallTool(ctx, &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: mustMarshal(t, map[string]any{"command": "sh", "args": []string{"-c", "echo pwned"}}),
	})
	if err != nil {
		t.Fatalf("denied CallTool protocol error: %v", err)
	}
	if !denied.IsError {
		t.Fatal("expected tool error for non-allowlisted command")
	}
	if !strings.Contains(getTextContent(denied), "not in the allowlist") {
		t.Errorf("unexpected denial message: %s", getTextContent(denied))
	}
}

func TestServer_ProcessTools(t *testing.T) {
	if os.Getenv("MCP_PROCESS_HELPER") == "1" {
		// Child process re-executed by the process manager.
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
		case "sleep":
			fmt.Println("sleeping")
			time.Sleep(30 * time.Second)
		case "logs":
			fmt.Println("hello from logs")
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}

	t.Setenv("MCP_PROCESS_HELPER", "1")
	tempDir := t.TempDir()
	ws, err := workspace.New(tempDir, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	cmdRunner, err := runner.New(runner.Options{Root: tempDir, Allowlist: []string{os.Args[0]}})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	mgr, err := procman.New(procman.Options{Root: tempDir, Runner: cmdRunner})
	if err != nil {
		t.Fatalf("procman.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})

	server := mcpserver.NewWithOptions(ws, "test", mcpserver.Options{Runner: cmdRunner, Processes: mgr})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	// All four process tools are present.
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tools := make(map[string]bool)
	for _, tool := range res.Tools {
		tools[tool.Name] = true
	}
	for _, name := range []string{
		"start_process", "list_processes", "process_logs", "stop_process",
		"restart_process", "stop_all_processes",
	} {
		if !tools[name] {
			t.Errorf("tool %q missing", name)
		}
	}

	helper := []string{"-test.run=TestServer_ProcessTools", "--"}

	// start_process with an auto-generated name.
	startRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "start_process",
		Arguments: mustMarshal(t, map[string]any{"command": os.Args[0], "args": append(helper, "sleep")}),
	})
	if err != nil {
		t.Fatalf("start_process: %v", err)
	}
	if startRes.IsError {
		t.Fatalf("start_process error: %s", getTextContent(startRes))
	}
	if !strings.Contains(getTextContent(startRes), "started proc-1") {
		t.Errorf("unexpected start output: %s", getTextContent(startRes))
	}

	// list_processes shows it running.
	listRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_processes", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("list_processes: %v", err)
	}
	listText := getTextContent(listRes)
	if !strings.Contains(listText, "proc-1") || !strings.Contains(listText, "running") {
		t.Errorf("unexpected list output: %s", listText)
	}

	// A short-lived process; its output becomes readable.
	startLog, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "start_process",
		Arguments: mustMarshal(t, map[string]any{"command": os.Args[0], "args": append(helper, "logs"), "name": "logger"}),
	})
	if err != nil || startLog.IsError {
		t.Fatalf("start logger: %v %+v", err, startLog)
	}
	var logs string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logRes, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "process_logs",
			Arguments: mustMarshal(t, map[string]any{"name": "logger", "tail_lines": 10}),
		})
		if err != nil || logRes.IsError {
			t.Fatalf("process_logs: %v %+v", err, logRes)
		}
		logs = getTextContent(logRes)
		if strings.Contains(logs, "hello from logs") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(logs, "hello from logs") {
		t.Errorf("logs missing output: %s", logs)
	}
	if !strings.Contains(logs, "next_seq:") {
		t.Errorf("logs missing cursor: %s", logs)
	}

	// stop_process terminates the running process.
	stopRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "stop_process",
		Arguments: mustMarshal(t, map[string]any{"name": "proc-1", "timeout_seconds": 2}),
	})
	if err != nil || stopRes.IsError {
		t.Fatalf("stop_process: %v %+v", err, stopRes)
	}
	if !strings.Contains(getTextContent(stopRes), "stopped") {
		t.Errorf("unexpected stop output: %s", getTextContent(stopRes))
	}

	// restart_process starts it again with the same argv.
	restartRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "restart_process",
		Arguments: mustMarshal(t, map[string]any{"name": "proc-1", "timeout_seconds": 2}),
	})
	if err != nil || restartRes.IsError {
		t.Fatalf("restart_process: %v %+v", err, restartRes)
	}
	if !strings.Contains(getTextContent(restartRes), "restarted proc-1") {
		t.Errorf("unexpected restart output: %s", getTextContent(restartRes))
	}

	// stop_all_processes cleans up everything.
	allRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "stop_all_processes",
		Arguments: mustMarshal(t, map[string]any{"timeout_seconds": 2}),
	})
	if err != nil || allRes.IsError {
		t.Fatalf("stop_all_processes: %v %+v", err, allRes)
	}
	if !strings.Contains(getTextContent(allRes), "0 still running") {
		t.Errorf("unexpected stop-all output: %s", getTextContent(allRes))
	}

	// Unknown names and non-allowlisted commands are tool errors.
	if r, _ := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "process_logs",
		Arguments: mustMarshal(t, map[string]any{"name": "missing"}),
	}); r == nil || !r.IsError {
		t.Error("process_logs for unknown name should fail")
	}
	if r, _ := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "start_process",
		Arguments: mustMarshal(t, map[string]any{"command": "sh", "args": []string{"-c", "echo pwned"}}),
	}); r == nil || !r.IsError {
		t.Error("start_process for denied command should fail")
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func getTextContent(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
