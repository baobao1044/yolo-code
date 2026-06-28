// Echo is the L7-001 demonstrator built-in: the smallest Tool that exercises
// the registry and (later) the dispatcher end-to-end. It echoes the "msg"
// arg to stdout, so tests can assert a tool actually ran and produced output
// without a filesystem or sandbox. Real built-ins (Read/Bash/Grep/…) land in
// later tickets.

package exec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yolo-code/yolo/internal/event"
)

// NewEcho returns the echo built-in. A constructor (not a zero-value) so the
// registry registers a fresh, isolated instance and the type reads like the
// other built-in constructors (NewRead, NewBash, …) that follow it.
func NewEcho() *Echo {
	return &Echo{}
}

// Echo echoes its "msg" argument to stdout.
type Echo struct{}

func (e *Echo) Name() string { return "echo" }

func (e *Echo) Metadata() Metadata {
	return Metadata{
		Permission:  Permission{FS: FSNone},
		Cost:        CostCheap,
		Category:    "demo",
		Description: "echo the msg argument to stdout (test built-in)",
	}
}

func (e *Echo) Schema() Schema {
	return Schema{Type: "object", Required: []string{"msg"}}
}

func (e *Echo) Risk(_ ToolCall) event.Risk { return RiskLow }

func (e *Echo) Run(_ context.Context, in ToolInput) (ToolOutput, error) {
	var args struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return ToolOutput{}, fmt.Errorf("echo: invalid args: %w", err)
	}
	return ToolOutput{Stdout: args.Msg, Summary: "echoed " + args.Msg}, nil
}
