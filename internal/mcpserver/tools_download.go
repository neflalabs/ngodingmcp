package mcpserver

// Download tool (opt-in, registered only when Options.Downloader is set):
// download_file fetches an https URL into the workspace. SSRF protection,
// size limits, redirects and the atomic write live in internal/fetch.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/fetch"
	"ngodingmcp/internal/workspace"
)

type downloadFileArgs struct {
	SourceURL string `json:"source_url" jsonschema:"https URL of the file to download. A signed, short-lived artifact URL (for example one supplied by the MCP client) works here. Only public https hosts are reached."`
	Path      string `json:"path" jsonschema:"destination file path, relative to the workspace root; parent directories are created"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace the destination if it already exists (default false)"`
	SHA256    string `json:"sha256,omitempty" jsonschema:"optional expected SHA-256 (64 hex characters); on mismatch nothing is written"`
}

type downloadFileOut struct {
	fetch.Result
}

func addDownloadFile(s *mcp.Server, ws *workspace.Workspace, dl *fetch.Client) {
	limit := fmt.Sprintf("%d bytes", dl.MaxBytes())
	if dl.MaxBytes()%(1<<20) == 0 {
		limit = fmt.Sprintf("%d MiB", dl.MaxBytes()>>20)
	}
	hosts := "any public host"
	if len(dl.AllowHosts()) > 0 {
		hosts = "only these hosts: " + strings.Join(dl.AllowHosts(), ", ")
	}
	annotations := destructive()
	annotations.OpenWorldHint = boolPtr(true)

	mcp.AddTool(s, &mcp.Tool{
		Name: "download_file",
		Description: "Download a file (text or binary, e.g. images) from an https URL and save it in the workspace. " +
			"Use this for binary files that write_file cannot create. " +
			"Limits: https only, " + hosts + ", max " + limit + ". Loopback and private network addresses are refused. " +
			"The write is atomic: on any error (size limit, sha256 mismatch, existing file without overwrite) nothing is written. " +
			"Returns the relative path, bytes written, sha256 and the reported content type.",
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadFileArgs) (*mcp.CallToolResult, downloadFileOut, error) {
		if strings.TrimSpace(in.SourceURL) == "" {
			return nil, downloadFileOut{}, fmt.Errorf("source_url is required")
		}
		if strings.TrimSpace(in.Path) == "" {
			return nil, downloadFileOut{}, fmt.Errorf("path is required")
		}
		res, err := dl.Download(ctx, ws, in.SourceURL, in.Path, in.Overwrite, in.SHA256)
		if err != nil {
			return nil, downloadFileOut{}, err
		}
		text := fmt.Sprintf("downloaded %d bytes from %s to %s\nsha256: %s", res.Bytes, res.Host, res.Path, res.SHA256)
		if res.ContentType != "" {
			text += "\ncontent-type (reported): " + res.ContentType
		}
		return textResult(text), downloadFileOut{Result: res}, nil
	})
}
