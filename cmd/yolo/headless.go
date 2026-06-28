// The headless runner (File 14 §14.10) is the Sprint 1 demo path: pipe a prompt
// to `yolo --headless` and it prints one JSON line per event to stdout. This is
// the cheapest demo (no TUI) and the one golden-transcript tests (File 15
// §15.15.3) assert against — so it must be deterministic across runs (S5).
//
// Determinism: each line carries the deterministic projection of an envelope
// (seq, type, task, and the event's payload), omitting the non-deterministic
// timestamp. Two runs with the same input produce byte-identical output.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/infra"
	"github.com/yolo-code/yolo/internal/memory"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

// runHeadless runs one headless turn against a fresh in-memory store, reading
// the prompt from stdin and writing one JSON line per event to a buffer whose
// bytes it returns. seed makes timestamps deterministic when nonzero (zero
// uses real time, but the projection omits time anyway).
func runHeadless(stdin io.Reader, seed int64) (string, error) {
	return runHeadlessCtx(context.Background(), stdin, seed)
}

// headlessDeps lets a caller override the runtime's port wiring (File 04 §4.6).
// Sprint 1's runHeadless leaves these nil → the runtime fills noop stubs (the
// single-turn demo path). L5-003 injects the real Context Engine + Prompt
// Compiler + an asserting cognitive core so the headless transcript reflects a
// real, compiled prompt carrying real file contents. Repo is the working-tree
// root the Context Engine reads from; Open is the set of open files to gather.
// L10-006 adds Memory + Bus: when both are set, the composition root wires a
// memoryStoreAdapter behind runtime.MemoryStore (publishing a learning event on
// the direct-answer path, §11.2). The caller owns the Store + Bus (the test
// builds them so the memory listener is the event subscriber); runHeadlessDeps
// uses deps.bus when provided and falls back to its own bus otherwise.
//
// L12-009 adds Infra: when set, the caller already called infra.Start on the
// shared bus (so it owns the aggregate for post-run assertions). runHeadlessDeps
// then owns the Stop — it runs after the bus closes in the close chain, so the
// root subscriber range ends → done closes → Stop's wait returns promptly (no
// goroutine leak). Infra is a pure observer (§13.1.2): it adds no events, so the
// transcript stays byte-identical to the unwired run.
type headlessDeps struct {
	context runtime.ContextBuilder
	prompt  runtime.PromptCompiler
	cog     runtime.CognitiveCore
	repo    string
	open    []string
	window  int
	memory  *memory.Store
	bus     *event.Bus
	infra   *infra.Infra // L12-009: caller Start'd it on deps.bus; runHeadlessDeps owns the Stop.
}

// runHeadlessDeps is the injectable form: real L4/L5 ports when deps is
// non-nil; the Sprint 1 stub path otherwise. The shared core drives the task
// and prints the transcript. runHeadless/runHeadlessCtx delegate here so
// there's one drive/print path, not two.
func runHeadlessDeps(ctx context.Context, stdin io.Reader, seed int64, deps *headlessDeps) (string, error) {
	prompt := readPrompt(stdin)

	// Fresh per-run store keeps the transcript reproducible: session and task
	// ids start at s_1/t_1 every time (S5 byte-identical).
	dir, err := os.MkdirTemp("", "yolo-headless")
	if err != nil {
		return "", err
	}
	store := session.NewFileStore(dir)
	// L10-006: when the caller injects a bus (the memory-wiring test does, so
	// the memory listener it owns is the event subscriber), reuse it instead of
	// making a fresh one — the runtime, the memory listener, and the headless
	// transcript subscriber must all share one bus. The caller closes it.
	// Otherwise make our own and close it on exit (Sprint 1 path).
	bus := event.New()
	busOwned := true
	if deps != nil && deps.bus != nil {
		bus = deps.bus
		busOwned = false
	}
	if busOwned {
		defer func() { _ = bus.Close() }()
	}

	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(ctx, "headless", "demo")
	if err != nil {
		return "", err
	}

	d := runtime.Deps{Bus: bus, Session: smgr}
	if deps != nil {
		d.Context = deps.context
		d.Prompt = deps.prompt
		d.Cognitive = deps.cog
		// L10-006: bridge memory into the runtime's MemoryStore port. The
		// adapter publishes a learning event the memory listener reacts to (it
		// does NOT mutate a sub-store directly — §11.2). Only wire it when the
		// caller injected a Store; the Sprint 1/2 stub path leaves Memory nil
		// and the runtime's nil-guard skips the Update call.
		if deps.memory != nil {
			d.Memory = memoryStoreAdapter{store: deps.memory, bus: bus}
		}
	} else {
		// Sprint 1 stub path: a canned-answer stub core, noop context+prompt.
		d.Cognitive = runtime.StubCognitive{Answer: cannedAnswer(prompt)}
	}
	// Sprint 6 wiring gap: the exec/verify/patch/restorer ports are NOT yet
	// wired here (they default to runtime no-op stubs). The real exec.Engine
	// (L7), verify.Engine (L8), patch.Engine (L9) and the session Manager's
	// checkpoint Restore all sit behind the runtime's port seams, but the
	// adapters that bridge them live in the composition root — and those are
	// deferred to the integration sprint (after L10 Memory). The L8-003 FSM
	// wiring PATCH→VERIFY→(fail)→rollback is proven by internal/runtime's
	// loop_test.go against stub ports, which is the Sprint 6 exit bar
	// (§15.9.2: a breaking agent edit is detected, the FSM transitions and
	// rolls back, the task is not marked done). Wiring the real engines here
	// needs its own TDD cycle (adapter tests) and is out of scope for L8-003.
	core := runtime.New(d)

	// Subscribe to the root wildcard BEFORE driving so no event is missed.
	ch := bus.Subscribe(event.Topic(">"))

	var out strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		enc := json.NewEncoder(&out)
		for env := range ch {
			_ = enc.Encode(projectEnvelope(env))
		}
	}()

	// Drive the task; the drive loop runs on this goroutine (MVP inline).
	submitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-submitCtx.Done():
		}
	}()
	_, _ = core.Submit(submitCtx, sid, prompt)

	// Close the bus so the subscriber drain ends and the transcript is flushed.
	_ = bus.Close()
	wg.Wait()
	// L12-009: with Infra wired, the root subscriber's range ended (the bus just
	// closed) so done is closed and Stop's wait returns promptly — flush the
	// observers. Bound by the run's ctx so a misbehaving flush can't hang the
	// headless exit; the stubs are no-ops so this is effectively free. Skipped
	// when no Infra is wired (the Sprint 1 path).
	if deps != nil && deps.infra != nil {
		_ = deps.infra.Stop(ctx)
	}
	return out.String(), nil
}

// runHeadlessCtx is the context-aware form: canceling ctx cancels the task
// mid-run (Ctrl+C path). It delegates to runHeadlessDeps with no injected
// ports (the Sprint 1 stub path).
func runHeadlessCtx(ctx context.Context, stdin io.Reader, seed int64) (string, error) {
	return runHeadlessDeps(ctx, stdin, seed, nil)
}

// readPrompt trims whitespace/newlines from the first line of stdin; that line
// becomes the task goal.
func readPrompt(stdin io.Reader) string {
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// cannedAnswer is the stubbed cognitive core's reply. In Sprint 1 it echoes the
// goal so the transcript visibly carries the user's input end-to-end.
func cannedAnswer(goal string) string {
	if goal == "" {
		return "hello"
	}
	return goal
}

// projectEnvelope renders the deterministic projection of an envelope: the
// bus-assigned seq, the event type, the causal task id, and the event payload
// as JSON. The timestamp is deliberately omitted so two runs of the same input
// produce byte-identical output (S5).
type projection struct {
	Seq  uint64          `json:"seq"`
	Type string          `json:"type"`
	Task string          `json:"task,omitempty"`
	Evt  json.RawMessage `json:"evt"`
}

func projectEnvelope(env event.Envelope) projection {
	payload, _ := json.Marshal(env.Evt)
	return projection{
		Seq:  env.Seq,
		Type: string(env.Evt.Type()),
		Task: string(env.Evt.CausalID()),
		Evt:  payload,
	}
}
