// Tests for the MCP client (File 08 §8.1.4): an external MCP server's tools
// are discovered at startup, wrapped into the same Tool interface as the
// static built-ins, and called through the client. A down server is skipped,
// not fatal. These tests use an in-process fake server (no MCP SDK) — the
// real HTTP/SSE transport is a later decision.

package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

func TestMCPDiscoverReturnsTools(t *testing.T) {
	up := newFakeMCPServer("alpha", []ToolSpec{
		{Name: "search", Schema: Schema{Type: "object", Required: []string{"q"}}, Risk: RiskLow},
	})
	c := NewMCPClient(map[string]ServerConn{"alpha": up})

	tools, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover = %v, want nil", err)
	}
	if len(tools) != 1 {
		t.Fatalf("Discover returned %d tools, want 1", len(tools))
	}
	got := tools[0]
	if got.Name() != "alpha.search" {
		t.Fatalf("MCP tool name = %q, want %q (server.spec dotted)", got.Name(), "alpha.search")
	}
	if _, ok := got.(*MCPTool); !ok {
		t.Fatalf("Discover returned %T, want *MCPTool", got)
	}
	if got.Schema().Type != "object" {
		t.Fatalf("MCP schema type = %q, want object", got.Schema().Type)
	}
}

func TestMCPToolCallable(t *testing.T) {
	up := newFakeMCPServer("alpha", []ToolSpec{{Name: "search", Risk: RiskLow}})
	c := NewMCPClient(map[string]ServerConn{"alpha": up})
	tools, _ := c.Discover(context.Background())

	out, err := tools[0].Run(context.Background(), ToolInput{Args: []byte(`{"q":"hi"}`)})
	if err != nil {
		t.Fatalf("MCPTool.Run = %v, want nil", err)
	}
	// The fake server echoes the call into its result; the wrapper must surface
	// it as stdout (File 08 §8.1.4: Stdout = out.Content).
	if !strings.Contains(out.Stdout, "search") {
		t.Fatalf("MCPTool.Run stdout = %q, want it to reflect the server's content", out.Stdout)
	}
	if up.calls != 1 {
		t.Fatalf("fake server CallTool count = %d, want 1 (Run must invoke the client)", up.calls)
	}
}

func TestMCPDownServerSkipped(t *testing.T) {
	down := newFakeMCPServer("down", nil)
	down.listErr = errDown("connection refused")
	up := newFakeMCPServer("up", []ToolSpec{{Name: "ping", Risk: RiskLow}})
	c := NewMCPClient(map[string]ServerConn{
		"down": down,
		"up":   up,
	})

	tools, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover = %v, want nil (a down server must not be fatal)", err)
	}
	if len(tools) != 1 {
		t.Fatalf("Discover returned %d tools, want 1 (down server skipped)", len(tools))
	}
	if tools[0].Name() != "up.ping" {
		t.Fatalf("returned tool = %q, want up.ping (down must be skipped, not block the agent)", tools[0].Name())
	}
}

// errDown is a tiny sentinel error for the fake's listErr, so the test can
// assert the failure path without importing errors.
type errDown string

func (e errDown) Error() string { return string(e) }

func TestMCPToolRiskDeclaredByServer(t *testing.T) {
	up := newFakeMCPServer("alpha", []ToolSpec{{Name: "rm", Risk: RiskHigh}})
	c := NewMCPClient(map[string]ServerConn{"alpha": up})
	tools, _ := c.Discover(context.Background())

	if got := tools[0].Risk(ToolCall{}); got != RiskHigh {
		t.Fatalf("MCPTool.Risk = %q, want %q (server-declared)", got, RiskHigh)
	}
}

// ensure event import is kept across tickets that accrete here.
var _ = event.TaskID("")
