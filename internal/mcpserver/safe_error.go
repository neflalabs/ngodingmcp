package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ngodingmcp/internal/workspace"
)

// safeErrorMiddleware rewrites host absolute paths in every failed tool result
// into workspace-relative ones, so no handler can leak the host directory
// layout through an error message.
func safeErrorMiddleware(ws *workspace.Workspace) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if r, ok := res.(*mcp.CallToolResult); ok && r != nil && r.IsError {
				for _, c := range r.Content {
					if tc, ok := c.(*mcp.TextContent); ok {
						tc.Text = ws.ScrubPaths(tc.Text)
					}
				}
			}
			return res, err
		}
	}
}
