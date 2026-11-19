package mcpserver

// File and directory tools: read_file, write_file, list_files, file_info,
// mkdir_path, move_path and delete_path. Path resolution, the workspace-root
// jail and the sensitive-file guard all live in internal/workspace.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

// addFileTools registers the file and directory tools.
func addFileTools(s *mcp.Server, ws *workspace.Workspace) {
	addReadFile(s, ws)
	addReadTools(s, ws)
	addWriteFile(s, ws)
	addListFiles(s, ws)
	addFileInfo(s, ws)
	addMkdirPath(s, ws)
	addMovePath(s, ws)
	addDeletePath(s, ws)
}

// --- read_file -------------------------------------------------------------

type readFileArgs struct {
	Path           string `json:"path" jsonschema:"path relative to workspace root"`
	Offset         int    `json:"offset,omitempty" jsonschema:"1-based line to start reading from (default 1)"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum number of lines to return (default all)"`
	IfChangedSince string `json:"if_changed_since,omitempty" jsonschema:"hash from a previous read; unchanged when equal"`
}

type readFileOut = workspace.ReadResult

func addReadFile(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "read_file",
		Description: "Read text as numbered lines; offset/limit slice, if_changed_since skips unchanged.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readFileArgs) (*mcp.CallToolResult, readFileOut, error) {
		res, err := ws.ReadFile(in.Path, in.Offset, in.Limit)
		if err != nil {
			return nil, readFileOut{}, err
		}
		if unchangedSince(in.IfChangedSince, res.Hash) {
			return textResult(fmt.Sprintf("unchanged %s (hash %s)", res.Path, res.Hash)), res, nil
		}
		header := fmt.Sprintf("%s (lines %d-%d of %d, %d bytes, hash %s)", res.Path, res.StartLine, res.EndLine, res.TotalLines, res.Size, res.Hash)
		return textResult(header + "\n" + workspace.NumberLines(res.Content, res.StartLine)), res, nil
	})
}

// unchangedSince compares a caller-supplied hash with the current one. The
// "sha256:" prefix is accepted for readability; comparison is case-insensitive.
func unchangedSince(prev, current string) bool {
	prev = strings.ToLower(strings.TrimSpace(prev))
	prev = strings.TrimPrefix(prev, "sha256:")
	return prev != "" && prev == current
}

// --- write_file ------------------------------------------------------------

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"path relative to workspace root"`
	Content string `json:"content" jsonschema:"full file content to write"`
}

type writeFileOut struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

func addWriteFile(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file atomically; parent directories are created.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in writeFileArgs) (*mcp.CallToolResult, writeFileOut, error) {
		old, existed := "", false
		if prev, err := ws.ReadFile(in.Path, 0, 0); err == nil {
			old, existed = prev.Content, true
		}
		p, n, err := ws.WriteFile(in.Path, in.Content)
		if err != nil {
			return nil, writeFileOut{}, err
		}
		out := writeFileOut{Path: p, Bytes: n}
		msg := fmt.Sprintf("wrote %d bytes to %s", n, p)
		if existed && smallRewrite(old, in.Content) {
			msg += "\nnote: this replaced a similar file; replace_text or edit_file would send much less context"
		}
		return textResult(msg), out, nil
	})
}

// rewriteWarnMinBytes is the size from which a whole-file rewrite is worth
// warning about. Below it the reminder would be noise.
const rewriteWarnMinBytes = 4 << 10

// smallRewrite reports whether new differs from old in a small region only, so
// resending the whole file was avoidable. It compares a common prefix and
// suffix; no full diff is built.
func smallRewrite(old, new string) bool {
	if len(old) < rewriteWarnMinBytes {
		return false
	}
	prefix := commonPrefixLen(old, new)
	suffix := commonSuffixLen(old[prefix:], new[prefix:])
	changedOld := len(old) - prefix - suffix
	changedNew := len(new) - prefix - suffix
	changed := changedOld
	if changedNew > changed {
		changed = changedNew
	}
	total := len(old)
	if len(new) > total {
		total = len(new)
	}
	return changed <= 256 || changed*10 <= total
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// --- list_files ------------------------------------------------------------

type listFilesArgs struct {
	Path       string `json:"path,omitempty" jsonschema:"directory to list; omit or use . for the workspace root"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"walk the whole subtree instead of one level"`
	Depth      int    `json:"depth,omitempty" jsonschema:"levels below path; implies recursive"`
	MaxEntries int    `json:"max_entries,omitempty" jsonschema:"entry cap (recursive default 300, max 2000)"`
}

type listFilesOut struct {
	Path      string            `json:"path"`
	Entries   []workspace.Entry `json:"entries"`
	Truncated bool              `json:"truncated,omitempty"`
}

func addListFiles(s *mcp.Server, ws *workspace.Workspace) {
	addTextTool(s, &mcp.Tool{
		Name:        "list_files",
		Description: "List files, one level or recursively; recursive caps at 300 entries (depth/max_entries tune it).",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listFilesArgs) (*mcp.CallToolResult, listFilesOut, error) {
		opts := workspace.ListOptions{Recursive: in.Recursive, Depth: in.Depth, MaxEntries: in.MaxEntries}
		entries, truncated, err := ws.ListFilesWithOptions(in.Path, opts)
		if err != nil {
			return nil, listFilesOut{}, err
		}
		var b strings.Builder
		for _, e := range entries {
			if e.IsDir {
				b.WriteString("dir  " + e.Path + "/\n")
			} else {
				fmt.Fprintf(&b, "file %s (%d bytes)\n", e.Path, e.Size)
			}
		}
		if b.Len() == 0 {
			b.WriteString("(empty)\n")
		}
		if truncated {
			fmt.Fprintf(&b, "... listing truncated at %d entries; narrow with path/depth or raise max_entries\n", opts.EntryLimit())
		}
		return textResult(b.String()), listFilesOut{Path: in.Path, Entries: entries, Truncated: truncated}, nil
	})
}

// --- file_info -------------------------------------------------------------

type fileInfoArgs struct {
	Path string `json:"path" jsonschema:"path relative to workspace root"`
}

type fileInfoOut struct {
	workspace.Info
}

func addFileInfo(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "file_info",
		Description: "File or directory metadata: size, modified time, line count.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in fileInfoArgs) (*mcp.CallToolResult, fileInfoOut, error) {
		info, err := ws.Stat(in.Path)
		if err != nil {
			return nil, fileInfoOut{}, err
		}
		text := fmt.Sprintf("path: %s\nis_dir: %t\nsize: %d bytes\nmodified: %s\nlines: %d",
			info.Path, info.IsDir, info.Size, info.ModTime.Format("2006-01-02 15:04:05"), info.Lines)
		return textResult(text), fileInfoOut{Info: info}, nil
	})
}

// --- mkdir_path ------------------------------------------------------------

type mkdirPathArgs struct {
	Path string `json:"path" jsonschema:"directory relative to workspace root"`
}

type mkdirPathOut struct {
	Path string `json:"path"`
}

func addMkdirPath(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "mkdir_path",
		Description: "Create a directory and any missing parents.",
		Annotations: additive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mkdirPathArgs) (*mcp.CallToolResult, mkdirPathOut, error) {
		p, err := ws.Mkdir(in.Path)
		if err != nil {
			return nil, mkdirPathOut{}, err
		}
		return textResult(fmt.Sprintf("created directory %s", p)), mkdirPathOut{Path: p}, nil
	})
}

// --- move_path -------------------------------------------------------------

type movePathArgs struct {
	From string `json:"from" jsonschema:"source file or directory path, relative to workspace root"`
	To   string `json:"to" jsonschema:"destination path"`
}

type movePathOut struct {
	Path string `json:"path"`
}

func addMovePath(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "move_path",
		Description: "Move or rename a file or directory.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in movePathArgs) (*mcp.CallToolResult, movePathOut, error) {
		p, err := ws.Rename(in.From, in.To)
		if err != nil {
			return nil, movePathOut{}, err
		}
		return textResult(fmt.Sprintf("moved %s -> %s", in.From, p)), movePathOut{Path: p}, nil
	})
}

// --- delete_path -----------------------------------------------------------

type deletePathArgs struct {
	Path      string `json:"path" jsonschema:"path to delete"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"delete a whole directory tree"`
}

type deletePathOut struct {
	Path string `json:"path"`
}

func addDeletePath(s *mcp.Server, ws *workspace.Workspace) {
	addUntypedTool(s, &mcp.Tool{
		Name:        "delete_path",
		Description: "Delete a file or directory; recursive=true for a non-empty one. The workspace root is refused.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deletePathArgs) (*mcp.CallToolResult, deletePathOut, error) {
		if err := ws.Delete(in.Path, in.Recursive); err != nil {
			return nil, deletePathOut{}, err
		}
		return textResult(fmt.Sprintf("deleted %s", in.Path)), deletePathOut{Path: in.Path}, nil
	})
}
