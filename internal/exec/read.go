// The Read built-in (File 08 §8.1.3): reads a file through the sandbox so
// every access is confined to the repo root. It is the smallest real built-in
// that exercises Resolve end-to-end (L7-008's Bash uses it for the shell
// gate). Write/Patch delegate to the Patch Engine (File 10) in a later sprint.

package exec

import (
	"context"
	"encoding/json"
	"os"

	"github.com/yolo-code/yolo/internal/event"
)

// NewRead returns a Read tool confined to s. The sandbox is captured at
// construction so the dispatcher can register one Read per engine.
func NewRead(s *Sandbox) *Read {
	return &Read{sandbox: s}
}

// Read reads a file (with optional line range, deferred) inside the sandbox.
type Read struct {
	sandbox *Sandbox
}

func (r *Read) Name() string { return "read" }

func (r *Read) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSRead},
		Cost:        CostCheap,
		Category:    "fs",
		Description: "read a file inside the sandbox",
	}
}

func (r *Read) Schema() Schema {
	return Schema{Type: "object", Required: []string{"path"}}
}

func (r *Read) Risk(_ ToolCall) event.Risk { return RiskLow }

func (r *Read) Run(_ context.Context, in ToolInput) (ToolOutput, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return ToolOutput{}, err
	}
	full, err := r.sandbox.Resolve(args.Path)
	if err != nil {
		return ToolOutput{}, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ToolOutput{}, err
	}
	return ToolOutput{Stdout: string(data), Files: []string{full}}, nil
}
