package mcpserver

// Search tools: search_text (read-only regex search) and search_and_replace
// (regex rewrite across many files, with dry_run). Matching, glob filtering
// and the sensitive-file guard live in internal/workspace.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

// maxSearchOutputBytes bounds the text a single search_text call returns
// (hits are also clipped per line by workspace.MaxMatchTextBytes). The check
// runs before each line is written, so the output may exceed the cap by one
// formatted line (~400 bytes) plus the truncation notice.
const maxSearchOutputBytes = 64 * 1024

// addSearchTools registers search_text and search_and_replace.
func addSearchTools(s *mcp.Server, ws *workspace.Workspace) {
	addSearchText(s, ws)
	addSearchAndReplace(s, ws)
}

// --- search_text -----------------------------------------------------------

type searchTextArgs struct {
	Pattern    string `json:"pattern" jsonschema:"Go regular expression to search for"`
	Path       string `json:"path,omitempty" jsonschema:"directory to search; omit for the workspace root"`
	Glob       string `json:"glob,omitempty" jsonschema:"glob filter on relative paths, e.g. *.go or **/*.go"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"maximum number of matches to return (default 100)"`
}

type searchTextOut struct {
	Matches []workspace.Match `json:"matches"`
}

func addSearchText(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "search_text",
		Description: "Regex search over file contents; returns paths and line numbers (dependency/cache dirs skipped unless passed as path).",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchTextArgs) (*mcp.CallToolResult, searchTextOut, error) {
		matches, err := ws.SearchText(in.Path, in.Pattern, in.Glob, in.MaxResults)
		if err != nil {
			return nil, searchTextOut{}, err
		}
		var b strings.Builder
		kept := matches
		for i, m := range matches {
			if b.Len() >= maxSearchOutputBytes {
				fmt.Fprintf(&b, "... output truncated at %d matches of %d; narrow with path or glob\n", i, len(matches))
				kept = matches[:i]
				break
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
		}
		if b.Len() == 0 {
			b.WriteString("no matches\n")
		}
		return textResult(b.String()), searchTextOut{Matches: kept}, nil
	})
}

// --- search_and_replace ----------------------------------------------------

type searchReplaceArgs struct {
	Pattern     string `json:"pattern" jsonschema:"Go regular expression to search for"`
	Replacement string `json:"replacement" jsonschema:"replacement string; supports $1, $2 for capture groups"`
	Path        string `json:"path,omitempty" jsonschema:"directory to search; omit for the workspace root"`
	Glob        string `json:"glob,omitempty" jsonschema:"glob filter, e.g. *.go or **/*.go"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"if true, show what would change without writing"`
	MaxFiles    int    `json:"max_files,omitempty" jsonschema:"maximum number of files to modify (default 50)"`
}

type searchReplaceOut struct {
	workspace.SearchReplaceResult
}

func addSearchAndReplace(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "search_and_replace",
		Description: "Regex replace across workspace files; dry_run=true previews (dependency/cache dirs skipped unless passed as path).",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchReplaceArgs) (*mcp.CallToolResult, searchReplaceOut, error) {
		res, err := ws.SearchReplace(in.Path, in.Pattern, in.Replacement, in.Glob, in.DryRun, in.MaxFiles)
		if err != nil {
			return nil, searchReplaceOut{}, err
		}
		var b strings.Builder
		if in.DryRun {
			fmt.Fprintf(&b, "[DRY RUN] would replace %q → %q in %d files:\n", in.Pattern, in.Replacement, res.FilesChanged)
		} else {
			fmt.Fprintf(&b, "replaced %q → %q in %d files:\n", in.Pattern, in.Replacement, res.FilesChanged)
		}
		for _, f := range res.Files {
			plural := "replacements"
			if f.Replacements == 1 {
				plural = "replacement"
			}
			fmt.Fprintf(&b, "  %s (%d %s)\n", f.Path, f.Replacements, plural)
		}
		if in.DryRun {
			fmt.Fprintf(&b, "total: %d replacements across %d files (not written)\n", res.TotalReplacements, res.FilesChanged)
		} else {
			fmt.Fprintf(&b, "total: %d replacements across %d files\n", res.TotalReplacements, res.FilesChanged)
		}
		if res.Truncated {
			maxF := in.MaxFiles
			if maxF <= 0 {
				maxF = 50
			}
			fmt.Fprintf(&b, "(truncated at %d files; use max_files to increase)\n", maxF)
		}
		return textResult(b.String()), searchReplaceOut{SearchReplaceResult: res}, nil
	})
}
