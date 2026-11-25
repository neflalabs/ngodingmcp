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

// DefaultSearchResults keeps one search_text call cheap; callers can raise
// max_results up to MaxSearchResults.
const (
	DefaultSearchResults = 30
	MaxSearchResults     = 1000
)

// addSearchTools registers search_text and search_and_replace.
func addSearchTools(s *mcp.Server, ws Workspace) {
	addSearchText(s, ws)
	addSearchAndReplace(s, ws)
}

// --- search_text -----------------------------------------------------------

type searchTextArgs struct {
	Pattern    string `json:"pattern" jsonschema:"Go regular expression to search for"`
	Path       string `json:"path,omitempty" jsonschema:"directory to search; omit for the workspace root"`
	Glob       string `json:"glob,omitempty" jsonschema:"glob filter on relative paths, e.g. *.go or **/*.go"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"maximum number of matches to return (default 30, max 1000)"`
}

type searchTextOut struct {
	Matches []workspace.Match `json:"matches"`
}

func addSearchText(s *mcp.Server, ws Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "search_text",
		Description: "Regex search over file contents, grouped by file (default 30 matches; dependency/cache dirs skipped).",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchTextArgs) (*mcp.CallToolResult, searchTextOut, error) {
		limit := in.MaxResults
		if limit <= 0 {
			limit = DefaultSearchResults
		}
		if limit > MaxSearchResults {
			limit = MaxSearchResults
		}
		matches, err := ws.SearchText(in.Path, in.Pattern, in.Glob, limit)
		if err != nil {
			return nil, searchTextOut{}, err
		}
		text, byteCapped := formatSearchMatches(matches)
		switch {
		case byteCapped:
			text += fmt.Sprintf("[output truncated at %d KiB; narrow with path or glob]\n", maxSearchOutputBytes/1024)
		case len(matches) >= limit:
			text += fmt.Sprintf("[limit %d matches reached; raise max_results or narrow with path/glob]\n", limit)
		}
		return textResult(text), searchTextOut{Matches: matches}, nil
	})
}

// formatSearchMatches groups hits under one path header, so the path is not
// repeated on every line. byteCapped reports that the output budget stopped
// the listing before every hit was written.
func formatSearchMatches(matches []workspace.Match) (text string, byteCapped bool) {
	if len(matches) == 0 {
		return "no matches\n", false
	}
	var b strings.Builder
	for i := 0; i < len(matches); {
		if b.Len() >= maxSearchOutputBytes {
			return b.String(), true
		}
		j := i + 1
		for j < len(matches) && matches[j].Path == matches[i].Path {
			j++
		}
		noun := "matches"
		if j-i == 1 {
			noun = "match"
		}
		fmt.Fprintf(&b, "%s (%d %s)\n", matches[i].Path, j-i, noun)
		for ; i < j; i++ {
			if b.Len() >= maxSearchOutputBytes {
				return b.String(), true
			}
			fmt.Fprintf(&b, "  %d: %s\n", matches[i].Line, matches[i].Text)
		}
	}
	return b.String(), false
}

// --- search_and_replace ----------------------------------------------------

type searchReplaceArgs struct {
	Pattern     string `json:"pattern" jsonschema:"Go regular expression to search for"`
	Replacement string `json:"replacement" jsonschema:"replacement string; supports $1, $2 for capture groups"`
	Path        string `json:"path,omitempty" jsonschema:"directory to search; omit for the workspace root"`
	Glob        string `json:"glob,omitempty" jsonschema:"glob filter, e.g. *.go or **/*.go"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"preview only"`
	MaxFiles    int    `json:"max_files,omitempty" jsonschema:"maximum number of files to modify (default 50)"`
}

type searchReplaceOut struct {
	workspace.SearchReplaceResult
}

func addSearchAndReplace(s *mcp.Server, ws Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "search_and_replace",
		Description: "Regex replace across files; dry_run=true previews (dependency/cache dirs skipped).",
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
