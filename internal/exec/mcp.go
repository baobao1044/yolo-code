// MCP runtime tools (File 08 §8.1.4): external tool servers speaking the
// Model Context Protocol. There is no MCP SDK in the tree yet (stdlib-only
// build), so the client/server seam is defined as exec-owned interfaces
// matching the spec's shape. A concrete HTTP/SSE transport is a later
// decision; for now a server is anything implementing ServerConn, and tests
// drive an in-process fake.
//
// An MCP tool is wrapped to satisfy the same Tool interface as the static
// built-ins, so the dispatcher, sandbox, and HITL gate treat it identically
// (File 08 §8.1.4 last paragraph). A down server is skipped on Discover, not
// fatal — one missing tool server must not break the agent.

package exec

import (
	"context"

	"github.com/baobao1044/yolo-code/internal/event"
)

// ToolSpec is the descriptor an MCP server advertises for one of its tools
// (File 08 §8.1.4 `mcp.ToolSpec`): the local tool name (dotted with the
// server name by MCPTool), the input schema, and the server-declared risk.
type ToolSpec struct {
	Name   string
	Schema Schema
	Risk   event.Risk
}

// CallResult is what ServerConn.CallTool returns (File 08 §8.1.4 `out`):
// the tool's textual content, exit code, and an optional summary.
type CallResult struct {
	Content  string
	ExitCode int
	Summary  string
}

// ServerConn is the per-server transport seam. A real implementation dials
// the MCP server's endpoint; the fake (mcpfake.go) runs in-process. Both
// satisfy this interface so Discover treats them uniformly.
type ServerConn interface {
	ListTools(ctx context.Context) ([]ToolSpec, error)
	CallTool(ctx context.Context, name string, args []byte) (CallResult, error)
}

// Client discovers tools across a set of MCP servers. Discover returns the
// tools of every reachable server; a server whose ListTools errors is skipped
// (File 08 §8.1.4). The map key is the server name used in the dotted tool
// name (server.tool).
type Client struct {
	servers map[string]ServerConn
}

// NewMCPClient builds a client over the named servers. The map is taken
// as-is (a nil map means no servers → Discover returns nil, nil).
func NewMCPClient(servers map[string]ServerConn) *Client {
	return &Client{servers: servers}
}

// Discover returns one MCPTool per tool each reachable server advertises
// (File 08 §8.1.4). A down server (ListTools errors) is skipped, not fatal.
func (c *Client) Discover(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	for name, srv := range c.servers {
		list, err := srv.ListTools(ctx)
		if err != nil {
			continue // a down server doesn't break the agent
		}
		for _, spec := range list {
			tools = append(tools, &MCPTool{server: name, spec: spec, client: srv})
		}
	}
	return tools, nil
}

// MCPTool wraps an MCP server's ToolSpec so the dispatcher/sandbox/HITL treat
// it like a static built-in (File 08 §8.1.4). Name is dotted server.spec so
// two servers can each expose a "search" without colliding.
type MCPTool struct {
	server string
	spec   ToolSpec
	client ServerConn
}

func (t *MCPTool) Name() string               { return t.server + "." + t.spec.Name }
func (t *MCPTool) Schema() Schema             { return t.spec.Schema }
func (t *MCPTool) Risk(_ ToolCall) event.Risk { return t.spec.Risk }
func (t *MCPTool) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSNone, Net: true}, // MCP servers are remote by default
		Category:    "mcp",
		Description: "MCP tool " + t.server + "." + t.spec.Name,
	}
}

func (t *MCPTool) Run(ctx context.Context, in ToolInput) (ToolOutput, error) {
	out, err := t.client.CallTool(ctx, t.spec.Name, in.Args)
	if err != nil {
		return ToolOutput{}, err
	}
	return ToolOutput{Stdout: out.Content, ExitCode: out.ExitCode, Summary: out.Summary}, nil
}
