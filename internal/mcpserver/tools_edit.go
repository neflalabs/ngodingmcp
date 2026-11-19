package mcpserver

// Editing tools for existing text files: replace_text, edit_file (atomic
// multi-edit with a unified diff), insert_text, delete_lines and
// replace_lines. Every write is atomic and goes through internal/workspace.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

// addEditTools registers the in-place editing tools.
func addEditTools(s *mcp.Server, ws *workspace.Workspace) {
	addReplaceText(s, ws)
	addEditFile(s, ws)
	addInsertText(s, ws)
	addDeleteLines(s, ws)
	addReplaceLines(s, ws)
}

// --- replace_text ----------------------------------------------------------

type replaceTextArgs struct {
	Path       string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	Old        string `json:"old" jsonschema:"exact text; unique unless replace_all"`
	New        string `json:"new" jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence"`
}

type replaceTextOut struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

func addReplaceText(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "replace_text",
		Description: "Replace an exact substring; it must occur once unless replace_all=true.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in replaceTextArgs) (*mcp.CallToolResult, replaceTextOut, error) {
		p, n, err := ws.ReplaceText(in.Path, in.Old, in.New, in.ReplaceAll)
		if err != nil {
			return nil, replaceTextOut{}, err
		}
		out := replaceTextOut{Path: p, Count: n}
		return textResult(fmt.Sprintf("replaced %d occurrence(s) in %s", n, p)), out, nil
	})
}

// --- edit_file -------------------------------------------------------------

type editFileArgs struct {
	Path   string           `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	Edits  []workspace.Edit `json:"edits" jsonschema:"edits in order; old must be unique unless replace_all"`
	DryRun bool             `json:"dry_run,omitempty" jsonschema:"if true, only return the diff and leave the file untouched"`
}

type editFileOut struct {
	workspace.EditResult
}

func addEditFile(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "edit_file",
		Description: "Apply exact-text edits to one file in order, all or nothing, and return a unified diff. dry_run=true previews.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in editFileArgs) (*mcp.CallToolResult, editFileOut, error) {
		res, err := ws.EditFile(in.Path, in.Edits, in.DryRun)
		if err != nil {
			return nil, editFileOut{}, err
		}
		var b strings.Builder
		if res.DryRun {
			fmt.Fprintf(&b, "[DRY RUN] %d edit(s), %d replacement(s) in %s (not written)\n", res.Edits, res.Replacements, res.Path)
		} else {
			fmt.Fprintf(&b, "applied %d edit(s), %d replacement(s) to %s; %d lines total\n", res.Edits, res.Replacements, res.Path, res.TotalLines)
		}
		if res.Diff == "" {
			b.WriteString("(no changes)\n")
		} else {
			b.WriteString("\n")
			b.WriteString(res.Diff)
		}
		return textResult(b.String()), editFileOut{EditResult: res}, nil
	})
}

// --- insert_text -----------------------------------------------------------

type insertTextArgs struct {
	Path string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	Line int    `json:"line" jsonschema:"1-based line to insert before; use total_lines+1 to append"`
	Text string `json:"text" jsonschema:"text to insert; a trailing newline is added if missing"`
}

type insertTextOut struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

func addInsertText(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "insert_text",
		Description: "Insert text before a 1-based line; line = total_lines + 1 appends.",
		Annotations: additive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in insertTextArgs) (*mcp.CallToolResult, insertTextOut, error) {
		p, err := ws.InsertText(in.Path, in.Line, in.Text)
		if err != nil {
			return nil, insertTextOut{}, err
		}
		out := insertTextOut{Path: p, Line: in.Line}
		return textResult(fmt.Sprintf("inserted text at line %d in %s", in.Line, p)), out, nil
	})
}

// --- delete_lines ----------------------------------------------------------

type deleteLinesArgs struct {
	Path string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	From int    `json:"from" jsonschema:"first line to delete (1-based, inclusive)"`
	To   int    `json:"to" jsonschema:"last line to delete (1-based, inclusive)"`
}

type deleteLinesOut struct {
	Path       string `json:"path"`
	TotalLines int    `json:"totalLines"`
}

func addDeleteLines(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "delete_lines",
		Description: "Delete an inclusive range of 1-based lines from a file.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deleteLinesArgs) (*mcp.CallToolResult, deleteLinesOut, error) {
		p, remaining, err := ws.DeleteLines(in.Path, in.From, in.To)
		if err != nil {
			return nil, deleteLinesOut{}, err
		}
		out := deleteLinesOut{Path: p, TotalLines: remaining}
		return textResult(fmt.Sprintf("deleted lines %d-%d in %s; %d lines remain", in.From, in.To, p, remaining)), out, nil
	})
}

// --- replace_lines ---------------------------------------------------------

type replaceLinesArgs struct {
	Path       string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	From       int    `json:"from" jsonschema:"first line to replace (1-based, inclusive)"`
	To         int    `json:"to" jsonschema:"last line to replace (1-based, inclusive)"`
	NewContent string `json:"new_content" jsonschema:"replacement text; replaces the entire line range"`
}

type replaceLinesOut struct {
	Path       string `json:"path"`
	TotalLines int    `json:"totalLines"`
}

func addReplaceLines(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "replace_lines",
		Description: "Replace an inclusive range of 1-based lines with new content.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in replaceLinesArgs) (*mcp.CallToolResult, replaceLinesOut, error) {
		p, total, err := ws.ReplaceLines(in.Path, in.From, in.To, in.NewContent)
		if err != nil {
			return nil, replaceLinesOut{}, err
		}
		out := replaceLinesOut{Path: p, TotalLines: total}
		return textResult(fmt.Sprintf("replaced lines %d-%d in %s; %d lines total", in.From, in.To, p, total)), out, nil
	})
}
