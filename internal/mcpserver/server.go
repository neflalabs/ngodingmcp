// Package mcpserver exposes the workspace as MCP tools over the official Go SDK.
//
// Layout: this file builds the server and decides which tools are active.
// Each tool domain lives in its own tools_<domain>.go file with an
// add<Domain>Tools entry point; shared helpers are in helpers.go and the
// cross-cutting middleware in safe_error.go and activity.go.
package mcpserver

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
	"ngodingmcp/internal/workspace"
)

const instructions = `This server is a sandboxed text editor for a workspace on disk.

Paths are always relative to the workspace root; leading slashes are ignored and
".." cannot escape the root. Prefer replace_text over write_file for targeted
edits, and read a file before modifying it.

Typical workflow:
1. list_files or search_text to locate what you need.
2. read_file to inspect exact content (lines are numbered in the text output).
3. replace_text / insert_text / delete_lines for precise edits.
4. write_file only when creating a file or rewriting it wholesale.`

// Options selects the opt-in tools registered on top of the editor tools.
type Options struct {
	// Runner enables run_command when non-nil.
	Runner *runner.Runner
	// Processes enables start_process, list_processes, process_logs and
	// stop_process when non-nil. Manager must also have a Runner.
	Processes *procman.Manager
	// Downloader enables download_file when non-nil.
	Downloader *fetch.Client
	// Logger, when non-nil, receives one concise activity line per tool call
	// (tool, target, ✓/✗, duration). Contents and secrets are never logged.
	Logger *slog.Logger
}

// New builds an MCP server bound to ws.
func New(ws *workspace.Workspace, version string) *mcp.Server {
	return NewWithOptions(ws, version, Options{})
}

// NewWithRunner builds an MCP server bound to ws. When run is non-nil, the
// opt-in run_command tool is registered as well.
func NewWithRunner(ws *workspace.Workspace, version string, run *runner.Runner) *mcp.Server {
	return NewWithOptions(ws, version, Options{Runner: run})
}

// NewWithOptions builds an MCP server bound to ws with the opt-in tools enabled
// by opts.
func NewWithOptions(ws *workspace.Workspace, version string, opts Options) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "ngodingmcp", Title: "NgodingMCP", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)

	// Core tools: always registered (26 in total).
	addFileTools(s, ws)    // read_file, write_file, list_files, file_info, mkdir_path, move_path, delete_path
	addEditTools(s, ws)    // replace_text, edit_file, insert_text, delete_lines, replace_lines
	addSearchTools(s, ws)  // search_text, search_and_replace
	addInspectTools(s, ws) // workspace_stats, validate_syntax, count_tokens
	addGitTools(s, ws)     // git_status, git_diff, git_log + git_init, git_add, git_unstage, git_commit, git_create_branch, git_switch_branch

	// Opt-in tools: registered only when the matching option is set.
	if opts.Runner != nil {
		addRunCommand(s, opts.Runner) // run_command
	}
	if opts.Processes != nil {
		addProcessTools(s, opts.Processes) // start_process, list_processes, process_logs, stop_process, restart_process, stop_all_processes
	}
	if opts.Downloader != nil {
		addDownloadFile(s, ws, opts.Downloader) // download_file
	}

	// Middleware: scrub host absolute paths from every failed tool result,
	// then (optionally) log one activity line per call.
	s.AddReceivingMiddleware(safeErrorMiddleware(ws))
	if opts.Logger != nil {
		s.AddReceivingMiddleware(activityMiddleware(opts.Logger))
	}

	return s
}
