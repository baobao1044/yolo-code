// The dispatcher (File 08 §8.3): one ToolCall → one Dispatch → one ToolResult.
// It looks the tool up, validates args, runs the admitter gate, classifies
// risk, runs the tool under a per-call timeout in a worker goroutine,
// normalizes the output, and publishes a ToolResultEvent. HITL approval
// (§8.5) is wired in L7-006; L7-003 covers everything up to and including Run.
//
// Single-call discipline: tools run sequentially per turn (the runtime drives
// one at a time, File 08 §8.3.3). Parallel read-only tools are a later
// opt-in. Each Dispatch runs its tool in a goroutine so a per-call timeout
// can cancel it; the goroutine is joined before Dispatch returns, so the
// engine never leaks workers (the timeout test asserts ctx cancellation and
// bounded elapsed time).

package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// ToolAdmitter is the tool-policy seam (File 08 §8.3.2 `toolPolicy.Allow`).
// exec may not import cognitive (import matrix), so the gate is an interface
// the composition root wires to a cognitive.ToolPolicy adapter later. nil
// means no gate (the default for unit tests; the real runtime always wires
// one, File 02 default-deny).
type ToolAdmitter interface {
	Admit(call ToolCall) error
}

// Normalizer turns a raw ToolOutput into a structured Observation (File 08
// §8.6). An interface so L7-003 can use a passthrough while L7-007 swaps in
// the real redact/truncate/summarize normalizer without touching engine.go.
type Normalizer interface {
	Normalize(out ToolOutput, meta Metadata) Observation
}

// Deps wires the engine at construction. Registry and bus are required;
// admitter/normalizer default to no gate / passthrough. The engine's config
// (per-call timeout, HITL auto-approve) is set on Engine directly by tests.
type Deps struct {
	Registry   *Registry
	Bus        *event.Bus
	Admitter   ToolAdmitter
	Normalizer Normalizer
}

// Engine dispatches tool calls (File 08 §8.7). Fields are read-only after New
// except the HITL pending map (L7-006), so Dispatch is safe for the
// single-goroutine drive loop (invariant I1).
type Engine struct {
	registry   *Registry
	bus        *event.Bus
	admitter   ToolAdmitter
	normalizer Normalizer
	config     Config
}

// Config holds the dispatcher's tunables. ToolTimeout caps every per-call
// worker; a tool's Metadata.Timeout overrides it downward (shorter wins).
// AutoApprove is the HITL gate's allowlist of risk classes that run without a
// prompt (wired in L7-006).
type Config struct {
	ToolTimeout time.Duration
	AutoApprove map[event.Risk]bool
}

// New builds an Engine from Deps. A nil admitter means no gate; a nil
// normalizer falls back to passthrough; a nil bus makes event publishing a
// no-op (unit tests can run a tool without a bus).
func New(d Deps) *Engine {
	e := &Engine{
		registry:   d.Registry,
		bus:        d.Bus,
		admitter:   d.Admitter,
		normalizer: d.Normalizer,
	}
	if e.normalizer == nil {
		e.normalizer = passthroughNormalizer{}
	}
	return e
}

// Dispatch runs one tool call (File 08 §8.3.2). A non-nil error means the call
// did not run to completion (unknown tool, bad args, denied, timeout, run
// error); a nil error means the tool ran and the Observation carries its
// result. The published ToolResultEvent carries the call's Task as its causal
// id so the event trace links to the originating session task.
func (e *Engine) Dispatch(ctx context.Context, call ToolCall) (Observation, error) {
	tool, ok := e.registry.Get(call.Tool)
	if !ok {
		return obsErr(call), fmt.Errorf("unknown tool %q", call.Tool)
	}
	if err := validateArgs(tool.Schema(), call.Args); err != nil {
		return obsErr(call), err
	}
	if e.admitter != nil {
		if err := e.admitter.Admit(call); err != nil {
			return obsErr(call), err
		}
	}
	// L7-006 inserts the HITL approval gate here (risk >= medium && !autoApprove).

	out, err := e.runWithTimeout(ctx, tool, call)
	if err != nil {
		return obsErr(call), err
	}

	obs := e.normalizer.Normalize(out, tool.Metadata())
	obs.Tool = call.Tool
	e.publishResult(ctx, call, obs)
	return obs, nil
}

// NeedsApproval reports whether a call would block on the HITL gate (File 08
// §8.3.2). Wired in L7-006; L7-003 returns false (no approval yet).
func (e *Engine) NeedsApproval(_ ToolCall) bool { return false }

// runWithTimeout runs the tool in a goroutine under a per-call timeout. If
// Config.ToolTimeout is set, a child context is cancelled when it elapses;
// the tool sees ctx.Done() and must return. The goroutine is always joined
// before return, so no worker leaks. A timeout surfaces as ctx.Err() so the
// caller can distinguish it from a tool's own error.
func (e *Engine) runWithTimeout(ctx context.Context, tool Tool, call ToolCall) (ToolOutput, error) {
	timeout := e.config.ToolTimeout
	if t := tool.Metadata().Timeout; t > 0 && (timeout == 0 || t < timeout) {
		timeout = t
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if timeout > 0 {
		var tc context.CancelFunc
		runCtx, tc = context.WithTimeout(ctx, timeout)
		defer tc()
	}

	type result struct {
		out ToolOutput
		err error
	}
	res := make(chan result, 1)
	go func() {
		out, err := tool.Run(runCtx, ToolInput{Args: call.Args})
		res <- result{out, err}
	}()

	select {
	case r := <-res:
		return r.out, r.err
	case <-runCtx.Done():
		// Let the tool observe the cancellation and exit, then drain to avoid
		// a leak. Bounded by the tool honoring ctx (the timeout test asserts).
		<-res
		return ToolOutput{}, runCtx.Err()
	}
}

// publishResult emits the ToolResultEvent for the call (File 08 §8.3.2). A nil
// bus makes this a no-op so a tool can run without an event trace.
func (e *Engine) publishResult(ctx context.Context, call ToolCall, obs Observation) {
	if e.bus == nil {
		return
	}
	_ = e.bus.Publish(ctx, &event.ToolResultEvent{
		Task: call.Task,
		Tool: call.Tool,
		Obs:  mustMarshalObs(obs),
	})
}

// obsErr is the Observation returned when a call fails before Run (unknown
// tool, bad args, denied). It carries no stdout so the model gets a clean
// error, not a half-empty observation.
func obsErr(call ToolCall) Observation {
	return Observation{Tool: call.Tool, ExitCode: -1}
}

// passthroughNormalizer copies a raw ToolOutput into an Observation unchanged
// (the L7-003 default; L7-007 swaps in the redact/truncate/summarize one).
type passthroughNormalizer struct{}

func (passthroughNormalizer) Normalize(out ToolOutput, _ Metadata) Observation {
	return Observation{
		Stdout:   out.Stdout,
		Stderr:   out.Stderr,
		ExitCode: out.ExitCode,
		Summary:  out.Summary,
		Bytes:    len(out.Stdout) + len(out.Stderr),
		Files:    out.Files,
	}
}

// mustMarshalObs serializes obs for the ToolResultEvent's opaque Obs field.
// The event wire carries it as json.RawMessage; a marshal failure is a
// programmer error (Observation is plain structs), so it panics rather than
// silently dropping the result.
func mustMarshalObs(obs Observation) []byte {
	b, err := json.Marshal(obs)
	if err != nil {
		panic("exec: marshal observation: " + err.Error())
	}
	return b
}
