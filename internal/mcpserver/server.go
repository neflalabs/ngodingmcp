// Package mcpserver exposes the workspace as MCP tools over the official Go SDK.
//
// Layout: this file builds the server and decides which tools are active.
// Each tool domain lives in its own tools_<domain>.go file with an
// add<Domain>Tools entry point; shared helpers are in helpers.go and the
// cross-cutting middleware in safe_error.go and activity.go.
package mcpserver

import (
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/procman"
	"ngodingmcp/internal/runner"
)

const (
	defaultName          = "ngodingmcp"
	defaultTitle         = "NgodingMCP"
	defaultAgentsURI     = "ngodingmcp://agents"
	defaultMetaNamespace = "ngodingmcp"
)

const defaultInstructions = `This server is a sandboxed text editor for a workspace on disk.

Paths are relative to the workspace root; leading slashes are ignored and ".."
cannot escape. If available, read the %s resource first: it
holds project structure, conventions and test commands.

Reading: list_files/search_text to locate; outline then read_symbol to read one
declaration; read_file for exact lines or read_files for several ranges. Pass
the returned hash as if_changed_since to skip unchanged files.

Editing: read first; prefer replace_text or edit_file; use replace_lines,
insert_text or delete_lines for line edits; write_file only for new files or a
wholesale rewrite.`

// Options selects the opt-in tools registered on top of the editor tools.
type Options struct {
	// Runner enables run_command when non-nil.
	Runner *runner.Runner
	// Processes enables start_process, list_processes, process_logs and
	// stop_process when non-nil. Manager must also have a Runner.
	Processes *procman.Manager
	// Downloader enables download_file when non-nil.
	Downloader *fetch.Client
	// Disabled switches core tool modules off (see Module); validate user
	// input with ParseModules.
	Disabled []Module
	// Logger, when non-nil, receives one concise activity line per tool call
	// (tool, target, ✓/✗, duration). Contents and secrets are never logged.
	Logger *slog.Logger
	// Name and Title override the MCP implementation identity. Empty keeps
	// the defaults, so existing clients see no change.
	Name  string
	Title string
	// Instructions overrides the handshake instructions; empty keeps the
	// editor workflow below.
	Instructions string
	// AgentsURI overrides the URI of the AGENTS.md resource (default
	// ngodingmcp://agents).
	AgentsURI string
	// MetaNamespace prefixes the implementation-specific _meta keys of
	// run_command (default "ngodingmcp"). The model does not see _meta, but
	// clients and the activity log do.
	MetaNamespace string
}

// New builds an MCP server bound to ws.
func New(ws Workspace, version string) *mcp.Server {
	return NewWithOptions(ws, version, Options{})
}

// NewWithRunner builds an MCP server bound to ws. When run is non-nil, the
// opt-in run_command tool is registered as well.
func NewWithRunner(ws Workspace, version string, run *runner.Runner) *mcp.Server {
	return NewWithOptions(ws, version, Options{Runner: run})
}

// NewWithOptions builds an MCP server bound to ws with the opt-in tools enabled
// by opts.
func NewWithOptions(ws Workspace, version string, opts Options) *mcp.Server {
	name, title, uri, namespace := opts.Name, opts.Title, opts.AgentsURI, opts.MetaNamespace
	if name == "" {
		name = defaultName
	}
	if title == "" {
		title = defaultTitle
	}
	if uri == "" {
		uri = defaultAgentsURI
	}
	if namespace == "" {
		namespace = defaultMetaNamespace
	}
	instructions := opts.Instructions
	if instructions == "" {
		instructions = fmt.Sprintf(defaultInstructions, uri)
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: name, Title: title, Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	addResources(s, ws, uri)

	// Core tools: registered unless their module is disabled (38 in total).
	on := newModuleSet(opts.Disabled)
	addFileTools(s, ws)   // read_file, outline, read_symbol, read_files, write_file, list_files, file_info, mkdir_path, move_path, delete_path
	addEditTools(s, ws)   // replace_text, edit_file, insert_text, delete_lines, replace_lines
	addSearchTools(s, ws) // search_text, search_and_replace
	if on.enabled(ModuleInspect) {
		addInspectTools(s, ws) // workspace_stats, validate_syntax, count_tokens
	}
	addGitTools(s, ws, on) // git_status, git_diff, git_log + modules git_write and git_remote

	// Opt-in tools: registered only when the matching option is set.
	if opts.Runner != nil {
		addRunCommand(s, opts.Runner, namespace) // run_command
	}
	if opts.Processes != nil {
		addProcessTools(s, opts.Processes) // start_process, list_processes, process_logs, stop_process, restart_process, stop_all_processes
	}
	if opts.Downloader != nil {
		addDownloadFile(s, ws, opts.Downloader) // download_file
	}

	// Middleware: scrub host absolute paths from every failed tool result,
	// hold the workspace for the whole call (a Web UI switch waits for it),
	// then (optionally) log one activity line per call.
	s.AddReceivingMiddleware(safeErrorMiddleware(ws))
	s.AddReceivingMiddleware(holdWorkspaceMiddleware(ws))
	if opts.Logger != nil {
		s.AddReceivingMiddleware(activityMiddleware(opts.Logger, namespace))
	}

	return s
}
