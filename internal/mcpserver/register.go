package mcpserver

// Registration helpers that decide which result payloads a tool advertises.
//
// mcp.AddTool with a typed output does two things: it publishes an
// outputSchema in tools/list (a fixed token cost on every session) and it
// sends the typed value as structuredContent next to the text content on
// every call. Clients do not agree on what reaches the model (see
// docs/tools.md, "Result payloads"), so the text content is the canonical
// output and these helpers trim the rest:
//
//   - addTextTool: content only. For tools whose result is mostly file
//     contents, listings, diffs or logs; a structured copy would only repeat
//     the text.
//   - addUntypedTool: content plus structuredContent, but no outputSchema.
//     For tools whose result is a short confirmation; programmatic clients
//     still get the JSON, the schema just stops costing tokens.
//
// Tools registered with plain mcp.AddTool keep both (small typed records
// such as file_info).

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolHandler is the typed handler shape every tool in this package uses.
type toolHandler[In, Out any] func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)

// addTextTool registers t so that its result carries only the text content:
// no outputSchema and no structuredContent. The handler's typed value is
// dropped.
func addTextTool[In, Out any](s *mcp.Server, t *mcp.Tool, h toolHandler[In, Out]) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		res, _, err := h(ctx, req, in)
		return res, nil, err
	})
}

// addUntypedTool registers t without an outputSchema while still sending the
// handler's value as structuredContent.
func addUntypedTool[In, Out any](s *mcp.Server, t *mcp.Tool, h toolHandler[In, Out]) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		res, out, err := h(ctx, req, in)
		if err != nil {
			return res, nil, err
		}
		return res, out, nil
	})
}
