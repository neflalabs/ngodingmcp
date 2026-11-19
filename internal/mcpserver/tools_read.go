package mcpserver

// Symbol and batch reading tools: outline, read_symbol and read_files. They
// cut the number of turns needed to inspect code: one call lists declarations,
// another reads only the one that matters, and several small files or ranges
// can be fetched under one byte budget.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

const (
	// defaultBatchReadBytes keeps one read_files call comparable to a large
	// read_file slice.
	defaultBatchReadBytes = 64 << 10
	// maxBatchReadBytes is the hard ceiling; a single file is still bounded
	// by the workspace read limit.
	maxBatchReadBytes = 1 << 20
)

func addReadTools(s *mcp.Server, ws *workspace.Workspace) {
	addOutline(s, ws)
	addReadSymbol(s, ws)
	addReadFiles(s, ws)
}

// --- outline ---------------------------------------------------------------

type outlineArgs struct {
	Path           string `json:"path" jsonschema:"path relative to workspace root"`
	IfChangedSince string `json:"if_changed_since,omitempty" jsonschema:"hash from a previous read; unchanged when equal"`
}

func addOutline(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "outline",
		Description: "List top-level symbols with line ranges; Go AST plus a JS/TS fallback.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in outlineArgs) (*mcp.CallToolResult, workspace.Outline, error) {
		out, err := ws.Outline(in.Path)
		if err != nil {
			return nil, workspace.Outline{}, err
		}
		if unchangedSince(in.IfChangedSince, out.Hash) {
			return textResult(fmt.Sprintf("unchanged %s (hash %s)", out.Path, out.Hash)), out, nil
		}
		return textResult(formatOutline(out)), out, nil
	})
}

func formatOutline(out workspace.Outline) string {
	var b strings.Builder
	lang := out.Language
	if lang == "" {
		lang = "unsupported"
	}
	fmt.Fprintf(&b, "%s (%s, %d lines, hash %s)\n", out.Path, lang, out.TotalLines, out.Hash)
	if out.Language == "" {
		b.WriteString("(outline not supported for this file type; use read_file)\n")
		return b.String()
	}
	if len(out.Symbols) == 0 {
		b.WriteString("(no top-level symbols)\n")
		return b.String()
	}
	for _, s := range out.Symbols {
		fmt.Fprintf(&b, "%-6s %s  L%d-L%d\n", s.Kind, s.Name, s.Start, s.End)
	}
	return b.String()
}

// --- read_symbol -----------------------------------------------------------

type readSymbolArgs struct {
	Path           string `json:"path" jsonschema:"path relative to workspace root"`
	Symbol         string `json:"symbol" jsonschema:"name as shown by outline"`
	IfChangedSince string `json:"if_changed_since,omitempty" jsonschema:"hash from a previous read; unchanged when equal"`
}

func addReadSymbol(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "read_symbol",
		Description: "Read one top-level function or type by name.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readSymbolArgs) (*mcp.CallToolResult, workspace.ReadResult, error) {
		res, sym, err := ws.ReadSymbol(in.Path, in.Symbol)
		if err != nil {
			return nil, workspace.ReadResult{}, err
		}
		if unchangedSince(in.IfChangedSince, res.Hash) {
			return textResult(fmt.Sprintf("unchanged %s (hash %s)", res.Path, res.Hash)), res, nil
		}
		header := fmt.Sprintf("%s: %s %s (lines %d-%d of %d, %d bytes, hash %s)",
			res.Path, sym.Kind, sym.Name, res.StartLine, res.EndLine, res.TotalLines, res.Size, res.Hash)
		return textResult(header + "\n" + workspace.NumberLines(res.Content, res.StartLine)), res, nil
	})
}

// --- read_files ------------------------------------------------------------

// readFileSpec is one file or range in a read_files call.
type readFileSpec struct {
	Path           string `json:"path" jsonschema:"path relative to workspace root"`
	Offset         int    `json:"offset,omitempty" jsonschema:"1-based first line"`
	Limit          int    `json:"limit,omitempty" jsonschema:"max lines"`
	IfChangedSince string `json:"if_changed_since,omitempty" jsonschema:"hash from a previous read; unchanged when equal"`
}

type readFilesArgs struct {
	Files    []readFileSpec `json:"files" jsonschema:"file slices to read, in order"`
	MaxBytes int            `json:"max_bytes,omitempty" jsonschema:"total content budget (default 65536, max 1048576)"`
}

func addReadFiles(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "read_files",
		Description: "Read several files or ranges in one call; total content capped (default 64 KiB).",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readFilesArgs) (*mcp.CallToolResult, struct{}, error) {
		if len(in.Files) == 0 {
			return nil, struct{}{}, errors.New("files is required")
		}
		budget := in.MaxBytes
		if budget <= 0 {
			budget = defaultBatchReadBytes
		}
		if budget > maxBatchReadBytes {
			budget = maxBatchReadBytes
		}

		var b strings.Builder
		used := 0
		next := len(in.Files)
		for i, spec := range in.Files {
			res, err := ws.ReadFile(spec.Path, spec.Offset, spec.Limit)
			if err != nil {
				writeReadBlockSeparator(&b)
				fmt.Fprintf(&b, "== %s\n!! %s\n", spec.Path, ws.SafeError(err))
				continue
			}
			if unchangedSince(spec.IfChangedSince, res.Hash) {
				writeReadBlockSeparator(&b)
				fmt.Fprintf(&b, "== %s (unchanged, hash %s)\n", res.Path, res.Hash)
				continue
			}
			room := budget - used
			if room <= 0 {
				next = i
				break
			}
			content := res.Content
			truncated := false
			if len(content) > room {
				content = cutToLine(content, room)
				if content == "" {
					next = i
					break
				}
				truncated = true
			}
			used += len(content)
			lines := textLineCount(content)
			writeReadBlockSeparator(&b)
			fmt.Fprintf(&b, "== %s (lines %d-%d of %d, %d bytes, hash %s)\n",
				res.Path, res.StartLine, res.StartLine+lines-1, res.TotalLines, res.Size, res.Hash)
			b.WriteString(workspace.NumberLines(content, res.StartLine))
			if truncated {
				fmt.Fprintf(&b, "[batch budget %d bytes reached; file truncated]\n", budget)
				next = i + 1
				break
			}
		}
		if next < len(in.Files) {
			skipped := make([]string, 0, len(in.Files)-next)
			for _, spec := range in.Files[next:] {
				skipped = append(skipped, spec.Path)
			}
			writeReadBlockSeparator(&b)
			fmt.Fprintf(&b, "[batch budget %d bytes reached; not shown: %s]\n", budget, strings.Join(skipped, ", "))
		}
		if b.Len() == 0 {
			b.WriteString("(no files)\n")
		}
		return textResult(b.String()), struct{}{}, nil
	})
}

func writeReadBlockSeparator(b *strings.Builder) {
	if b.Len() > 0 {
		b.WriteString("\n")
	}
}

// cutToLine returns at most room bytes, ending at a line boundary. Empty means
// there was no complete line to keep.
func cutToLine(content string, room int) string {
	if room >= len(content) {
		return content
	}
	if room <= 0 {
		return ""
	}
	cut := content[:room]
	if idx := strings.LastIndexByte(cut, '\n'); idx >= 0 {
		return cut[:idx+1]
	}
	return ""
}

func textLineCount(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}
