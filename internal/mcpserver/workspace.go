package mcpserver

import (
	"io"

	"ngodingmcp/internal/workspace"
)

// Workspace is the part of workspace behaviour the MCP layer needs. The
// concrete workspace.Workspace satisfies it; a template can supply another
// implementation without changing the server or the tool registrations.
type Workspace interface {
	Root() string
	CleanPath(rel string) (string, error)
	AllowSensitive() bool
	CheckWritable(rel string, overwrite bool) (string, error)
	WriteStream(rel string, r io.Reader, opts workspace.StreamOptions) (workspace.StreamResult, error)
	ReadFile(rel string, offset, limit int) (workspace.ReadResult, error)
	WriteFile(rel, content string) (string, int, error)
	ReplaceText(rel, old, new string, all bool) (string, int, error)
	EditFile(rel string, edits []workspace.Edit, dryRun bool) (workspace.EditResult, error)
	InsertText(rel string, line int, text string) (string, error)
	DeleteLines(rel string, from, to int) (string, int, error)
	ReplaceLines(rel string, from, to int, newContent string) (string, int, error)
	ListFilesWithOptions(rel string, o workspace.ListOptions) ([]workspace.Entry, bool, error)
	Stat(rel string) (workspace.Info, error)
	Mkdir(rel string) (string, error)
	Rename(from, to string) (string, error)
	Delete(rel string, recursive ...bool) error
	SearchText(rel, pattern, glob string, maxResults int) ([]workspace.Match, error)
	SearchReplace(rel, pattern, replacement, glob string, dryRun bool, maxFiles int) (workspace.SearchReplaceResult, error)
	Stats() (workspace.WorkspaceStats, error)
	ValidateSyntax(rel string) (workspace.SyntaxResult, error)
	CountTokens(rels []string) ([]workspace.TokenCount, error)
	Outline(rel string) (workspace.Outline, error)
	ReadSymbol(rel, name string) (workspace.ReadResult, workspace.Symbol, error)
	SafeError(err error) error
	ScrubPaths(s string) string
	Hold() (release func())
}
