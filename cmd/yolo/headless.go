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

// runHeadlessCtx is the context-aware form: canceling ctx cancels the task
// mid-run (Ctrl+C path). It returns the printed transcript and any fatal error.
func runHeadlessCtx(ctx context.Context, stdin io.Reader, seed int64) (string, error) {
	prompt := readPrompt(stdin)

	// Fresh per-run store keeps the transcript reproducible: session and task
	// ids start at s_1/t_1 every time (S5 byte-identical).
	dir, err := os.MkdirTemp("", "yolo-headless")
	if err != nil {
		return "", err
	}
	store := session.NewFileStore(dir)
	bus := event.New()
	defer func() { _ = bus.Close() }()

	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(ctx, "headless", "demo")
	if err != nil {
		return "", err
	}

	core := runtime.New(runtime.Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: runtime.StubCognitive{Answer: cannedAnswer(prompt)},
	})

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
	return out.String(), nil
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
