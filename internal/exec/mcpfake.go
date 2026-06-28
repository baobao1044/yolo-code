// An in-process fake MCP server for tests (no MCP SDK in the tree). It
// advertises a fixed list of ToolSpecs and records CallTool invocations so
// tests can assert a wrapper actually invoked the client. Lives in the exec
// package so the test files can use it without a _test.go-only helper
// package (the rest of exec's tests reuse it as MCP lands in later tickets).

package exec

import (
	"context"
	"fmt"
	"sync"
)

// fakeMCPServer implements ServerConn with a canned tool list and a CallTool
// that echoes the call into its result content (so a test can assert the
// wrapper surfaced the server's content as stdout).
type fakeMCPServer struct {
	mu      sync.Mutex
	tools   []ToolSpec
	calls   int
	listErr error // non-nil → ListTools fails (the "down server" case)
}

func newFakeMCPServer(_ string, tools []ToolSpec) *fakeMCPServer {
	return &fakeMCPServer{tools: tools}
}

func (s *fakeMCPServer) ListTools(_ context.Context) ([]ToolSpec, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.tools, nil
}

func (s *fakeMCPServer) CallTool(_ context.Context, name string, args []byte) (CallResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return CallResult{
		Content:  fmt.Sprintf("%s(%s)", name, string(args)),
		Summary:  "mcp call " + name,
		ExitCode: 0,
	}, nil
}
