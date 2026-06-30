// Tests for the exec→runtime adapter (Sprint 12 INT-001).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

func TestExecAdapterDispatchesBash(t *testing.T) {
	root := t.TempDir()
	sandbox := exec.NewSandbox(root, root)
	reg := new(exec.Registry)
	reg.Register(exec.NewBash(sandbox))

	engine := exec.New(exec.Deps{Registry: reg})
	adapter := &execAdapter{engine: engine}

	obs, err := adapter.Dispatch(context.Background(), runtime.ToolCall{
		Tool: "bash",
		Args: []byte(`{"command":"go version"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch(bash) = %v, want nil", err)
	}
	if !strings.Contains(obs.Stdout, "go version") {
		t.Fatalf("stdout = %q, want go version", obs.Stdout)
	}
}
