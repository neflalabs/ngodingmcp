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
	addWriteFile(s, ws)
	addListFiles(s, ws)
	addFileInfo(s, ws)
	addMkdirPath(s, ws)
	addMovePath(s, ws)
	addDeletePath(s, ws)
}

// --- read_file -------------------------------------------------------------

type readFileArgs struct {
	Path   string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	Offset int    `json:"offset,omitempty" jsonschema:"1-based line to start reading from (default 1)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of lines to return (default all)"`
}

type readFileOut struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Size       int64  `json:"size"`
}

func addReadFile(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a text file. Returns numbered lines plus the raw content of the requested slice.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readFileArgs) (*mcp.CallToolResult, readFileOut, error) {
		res, err := ws.ReadFile(in.Path, in.Offset, in.Limit)
		if err != nil {
			return nil, readFileOut{}, err
		}
		out := readFileOut(res)
		header := fmt.Sprintf("%s (lines %d-%d of %d, %d bytes)", res.Path, res.StartLine, res.EndLine, res.TotalLines, res.Size)
		return textResult(header + "\n" + workspace.NumberLines(res.Content, res.StartLine)), out, nil
	})
}

// --- write_file ------------------------------------------------------------

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"path to the file, relative to the workspace root"`
	Content string `json:"content" jsonschema:"full file content to write"`
}

type writeFileOut struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

func addWriteFile(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file with the given content. Parent directories are created automatically. Writes are atomic.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in writeFileArgs) (*mcp.CallToolResult, writeFileOut, error) {
		p, n, err := ws.WriteFile(in.Path, in.Content)
		if err != nil {
			return nil, writeFileOut{}, err
		}
		out := writeFileOut{Path: p, Bytes: n}
		return textResult(fmt.Sprintf("wrote %d bytes to %s", n, p)), out, nil
	})
}

// --- list_files ------------------------------------------------------------

type listFilesArgs struct {
	Path      string `json:"path,omitempty" jsonschema:"directory to list; omit or use . for the workspace root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"walk the whole subtree instead of one level"`
}

type listFilesOut struct {
	Path    string            `json:"path"`
	Entries []workspace.Entry `json:"entries"`
}

func addListFiles(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_files",
		Description: "List files and directories inside the workspace, one level or recursively.",
		Annotations: readOnly(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listFilesArgs) (*mcp.CallToolResult, listFilesOut, error) {
		entries, err := ws.ListFiles(in.Path, in.Recursive)
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
		return textResult(b.String()), listFilesOut{Path: in.Path, Entries: entries}, nil
	})
}

// --- file_info -------------------------------------------------------------

type fileInfoArgs struct {
	Path string `json:"path" jsonschema:"path to inspect, relative to the workspace root"`
}

type fileInfoOut struct {
	workspace.Info
}

func addFileInfo(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "file_info",
		Description: "Return metadata about a file or directory: size, modification time, and line count.",
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
	Path string `json:"path" jsonschema:"directory path to create, relative to the workspace root"`
}

type mkdirPathOut struct {
	Path string `json:"path"`
}

func addMkdirPath(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "mkdir_path",
		Description: "Create a directory (and any necessary parent directories) inside the workspace.",
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
	To   string `json:"to" jsonschema:"destination file or directory path, relative to workspace root"`
}

type movePathOut struct {
	Path string `json:"path"`
}

func addMovePath(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "move_path",
		Description: "Move or rename a file or directory within the workspace.",
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
	Path      string `json:"path" jsonschema:"file or directory path to delete, relative to workspace root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"if true and target is a directory, deletes entire directory tree"`
}

type deletePathOut struct {
	Path string `json:"path"`
}

func addDeletePath(s *mcp.Server, ws *workspace.Workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_path",
		Description: "Delete a file or directory inside the workspace. Use recursive=true to remove non-empty directories. Refuses to delete workspace root.",
		Annotations: destructive(),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deletePathArgs) (*mcp.CallToolResult, deletePathOut, error) {
		if err := ws.Delete(in.Path, in.Recursive); err != nil {
			return nil, deletePathOut{}, err
		}
		return textResult(fmt.Sprintf("deleted %s", in.Path)), deletePathOut{Path: in.Path}, nil
	})
}
