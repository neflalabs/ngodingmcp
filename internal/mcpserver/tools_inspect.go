package mcpserver

// Inspection tools (all read-only): workspace_stats, validate_syntax and
// count_tokens. They summarise files without returning their contents.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

// addInspectTools registers workspace_stats, validate_syntax and count_tokens.
func addInspectTools(s *mcp.Server, ws *workspace.Workspace) {
	addWorkspaceStats(s, ws)
	addValidateSyntax(s, ws)
	addCountTokens(s, ws)
}

// --- workspace_stats -------------------------------------------------------

type workspaceStatsArgs struct{}

type workspaceStatsOut struct {
	workspace.WorkspaceStats
}

func addWorkspaceStats(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_stats",
		Description: "Return aggregate statistics for the entire workspace: file counts, line counts, bytes, breakdown by extension, largest and most recently modified files.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ workspaceStatsArgs) (*mcp.CallToolResult, workspaceStatsOut, error) {
		stats, err := ws.Stats()
		if err != nil {
			return nil, workspaceStatsOut{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "total: %d files, %d lines, %d bytes\n", stats.TotalFiles, stats.TotalLines, stats.TotalBytes)
		if len(stats.ByExtension) > 0 {
			b.WriteString("\nby extension:\n")
			for _, ext := range extsByCount(stats.ByExtension) {
				es := stats.ByExtension[ext]
				fmt.Fprintf(&b, "  %-8s: %d files, %d lines, %d bytes\n", ext, es.Files, es.Lines, es.Bytes)
			}
		}
		if len(stats.LargestFiles) > 0 {
			b.WriteString("\nlargest files (by lines):\n")
			for _, lf := range stats.LargestFiles {
				fmt.Fprintf(&b, "  %s (%d lines)\n", lf.Path, lf.Lines)
			}
		}
		if len(stats.RecentFiles) > 0 {
			b.WriteString("\nrecently modified:\n")
			for _, rf := range stats.RecentFiles {
				fmt.Fprintf(&b, "  %s (%s)\n", rf.Path, rf.ModTime.Format("2006-01-02 15:04:05"))
			}
		}
		return textResult(b.String()), workspaceStatsOut{WorkspaceStats: stats}, nil
	})
}

// extsByCount returns the extension keys of m ordered by file count
// (descending), then alphabetically, so the output is deterministic.
func extsByCount(m map[string]workspace.ExtStats) []string {
	exts := make([]string, 0, len(m))
	for ext := range m {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool {
		ci, cj := m[exts[i]].Files, m[exts[j]].Files
		if ci != cj {
			return ci > cj
		}
		return exts[i] < exts[j]
	})
	return exts
}

// --- validate_syntax -------------------------------------------------------

type validateSyntaxArgs struct {
	Path string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
}

type validateSyntaxOut struct {
	workspace.SyntaxResult
}

func addValidateSyntax(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "validate_syntax",
		Description: "Check the syntax of a file without running it. Go (.go) and JSON (.json) get a full parse; JavaScript (.js/.mjs/.cjs), TypeScript (.ts/.mts/.cts) and CSS (.css) get a delimiter check (balanced brackets, closed strings, template literals, regexes and comments). Other types return valid=true with check=none.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in validateSyntaxArgs) (*mcp.CallToolResult, validateSyntaxOut, error) {
		res, err := ws.ValidateSyntax(in.Path)
		if err != nil {
			return nil, validateSyntaxOut{}, err
		}
		out := validateSyntaxOut{SyntaxResult: res}
		var text string
		if res.Valid {
			text = fmt.Sprintf("syntax OK: %s (%s, check: %s)", res.Path, res.Language, res.Check)
			if res.Check == "none" {
				text += " - no checker for this file type"
			}
		} else {
			var b strings.Builder
			fmt.Fprintf(&b, "syntax errors in %s (%s):\n", res.Path, res.Language)
			for _, e := range res.Errors {
				fmt.Fprintf(&b, "  line %d, col %d: %s\n", e.Line, e.Col, e.Message)
			}
			text = strings.TrimRight(b.String(), "\n")
		}
		return textResult(text), out, nil
	})
}

// --- count_tokens ----------------------------------------------------------

type countTokensArgs struct {
	Paths []string `json:"paths" jsonschema:"list of file or directory paths relative to the workspace root"`
}

type countTokensOut struct {
	Items                []workspace.TokenCount `json:"items"`
	TotalBytes           int64                  `json:"totalBytes"`
	TotalEstimatedTokens int                    `json:"totalEstimatedTokens"`
}

func addCountTokens(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "count_tokens",
		Description: "Estimate the number of LLM tokens for one or more files or directories without returning their contents. Uses bytes/4 as a rough proxy.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in countTokensArgs) (*mcp.CallToolResult, countTokensOut, error) {
		items, err := ws.CountTokens(in.Paths)
		if err != nil {
			return nil, countTokensOut{}, err
		}
		var totalBytes int64
		var totalTokens int
		var totalLines int
		for _, it := range items {
			totalBytes += it.Bytes
			totalTokens += it.EstimatedTokens
			totalLines += it.Lines
		}
		out := countTokensOut{
			Items:                items,
			TotalBytes:           totalBytes,
			TotalEstimatedTokens: totalTokens,
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%-30s %8s %7s %8s\n", "path", "bytes", "lines", "~tokens")
		for _, it := range items {
			fmt.Fprintf(&b, "%-30s %8d %7d %8d\n", it.Path, it.Bytes, it.Lines, it.EstimatedTokens)
		}
		b.WriteString(strings.Repeat("─", 57) + "\n")
		fmt.Fprintf(&b, "%-30s %8d %7d %8d\n", "total", totalBytes, totalLines, totalTokens)
		return textResult(b.String()), out, nil
	})
}
