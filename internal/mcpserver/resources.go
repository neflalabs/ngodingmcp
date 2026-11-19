package mcpserver

// MCP resources. They are fetched on demand and therefore do not add to the
// tools/list budget. ngodingmcp://agents exposes AGENTS.md from the workspace
// root so a client can learn the project's structure, conventions and test
// commands without exploring the tree first.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

const agentsURI = "ngodingmcp://agents"

func addResources(s *mcp.Server, ws *workspace.Workspace) {
	s.AddResource(&mcp.Resource{
		URI:         agentsURI,
		Name:        "AGENTS.md",
		Title:       "Project guide (AGENTS.md)",
		Description: "Project structure, conventions and test commands from AGENTS.md at the workspace root.",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		res, err := ws.ReadFile("AGENTS.md", 0, 0)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "text/markdown",
			Text:     res.Content,
		}}}, nil
	})
}
