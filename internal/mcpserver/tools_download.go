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
	SourceURL string `json:"source_url" jsonschema:"https URL; signed short-lived artifact URLs work"`
	Path      string `json:"path" jsonschema:"destination path; parents are created"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace an existing file"`
	SHA256    string `json:"sha256,omitempty" jsonschema:"expected SHA-256 (hex)"`
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

	addUntypedTool(s, &mcp.Tool{
		Name: "download_file",
		Description: "Download an https URL (text or binary, e.g. images) into the workspace. " +
			"Limits: " + hosts + ", max " + limit + "; loopback and private addresses are refused. " +
			"Atomic: nothing is written on any error.",
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
