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

// holdWorkspaceMiddleware holds the workspace for the whole tool call, path
// scrubbing included, so a workspace switch from the Web UI can never change
// the root in the middle of a call. A switch waits until running calls end.
func holdWorkspaceMiddleware(ws *workspace.Workspace) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			release := ws.Hold()
			defer release()
			return next(ctx, method, req)
		}
	}
}
