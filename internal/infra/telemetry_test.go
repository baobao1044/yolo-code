// Tests for L12-001 — Telemetry: span-per-event projector (File 13 §13.3).
// Telemetry is a pure observer: every event published to the bus becomes exactly
// one span, parented to the task root span for its task (identified by the
// event's CausalID). This is the stdlib-only stub (Sprint 7 zero-deps
// precedent): spans are recorded in an in-memory store, queryable by taskID,
// so the trace tree is testable without a real OTel collector. The real SDK
// swaps in behind the Telemetry type later.
//
// Allowed imports for this package's tests: event, infra, stdlib.

package infra

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestTelemetryStartRootRecordsTaskRoot pins StartRoot: it stores the task root
// span keyed by taskID so subsequent event spans can parent to it. A task with
// no root recorded parents to nothing (linkless span), per §13.3.2's "if ok"
// guard.
func TestTelemetryStartRootRecordsTaskRoot(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")

	roots := tel.Roots()
	if _, ok := roots["t_42"]; !ok {
		t.Fatal("StartRoot did not record a root span keyed by taskID t_42")
	}
}

// TestTelemetryEndRootClosesTaskRoot pins EndRoot: the root span is removed from
// the live map (LoadAndDelete semantics, §13.3.2) and marked ended with an
// error status when err is non-nil.
func TestTelemetryEndRootClosesTaskRoot(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")
	tel.EndRoot("t_42", nil)

	if _, ok := tel.Roots()["t_42"]; ok {
		t.Error("EndRoot left the root span in the live map; it should be removed")
	}
	ended := tel.EndedRoots()
	if _, ok := ended["t_42"]; !ok {
		t.Error("EndRoot did not record the ended root span for inspection")
	}
}

// TestTelemetryEndRootRecordsError pins that EndRoot with a non-nil error marks
// the span's status as Error and records the error message (§13.3.2).
func TestTelemetryEndRootRecordsError(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")
	tel.EndRoot("t_42", errBoom)

	sp := tel.EndedRoots()["t_42"]
	if sp == nil {
		t.Fatal("ended root span missing")
	}
	if sp.Status != SpanStatusError {
		t.Errorf("ended root status = %q, want %q", sp.Status, SpanStatusError)
	}
	if sp.ErrMsg == "" {
		t.Error("ended root span did not record the error message")
	}
}

// TestTelemetryProjectOneSpanPerEvent is the §13.3.1 invariant: every event
// published becomes exactly ONE span, named after its topic, parented to the
// task root for the event's CausalID. A task root must be started first so the
// child span links to it.
func TestTelemetryProjectOneSpanPerEvent(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")

	envs := []event.Envelope{
		mkEnv(1, &event.StateChangeEvent{Task: "t_42", From: "INIT", To: "PLAN", Why: "go"}),
		mkEnv(2, &event.ToolCallEvent{Task: "t_42", Tool: "ls"}),
		mkEnv(3, &event.TaskCompletedEvent{Task: "t_42"}),
	}
	for _, env := range envs {
		tel.Project(context.Background(), env)
	}

	spans := tel.Spans()
	if got, want := len(spans), 3; got != want {
		t.Fatalf("Project recorded %d spans, want %d (one per event)", got, want)
	}
	// Each span is named after its event's topic.
	wantTopics := []string{"state.change", "tool.call", "task.completed"}
	for i, want := range wantTopics {
		if spans[i].Name != want {
			t.Errorf("span[%d].Name = %q, want %q", i, spans[i].Name, want)
		}
	}
	// Each child span is parented (linked) to its task root.
	for i, sp := range spans {
		if sp.ParentTaskID != "t_42" {
			t.Errorf("span[%d].ParentTaskID = %q, want t_42 (linked to root)", i, sp.ParentTaskID)
		}
	}
}

// TestTelemetryProjectCarriesEventAttributes pins §13.3.3: the span's
// attributes carry the event's fields (task.id, state.from/to, tool.name,
// etc.). Unknown fields are attached as-is with their JSON key.
func TestTelemetryProjectCarriesEventAttributes(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")

	tel.Project(context.Background(),
		mkEnv(1, &event.StateChangeEvent{Task: "t_42", From: "INIT", To: "PLAN", Why: "go"}))

	spans := tel.Spans()
	if len(spans) != 1 {
		t.Fatalf("Project recorded %d spans, want 1", len(spans))
	}
	attrs := spans[0].Attrs
	// task.id and state.from/state.to must be present (§13.3.3 task.* + state.change rows).
	if attrs["task.id"] != "t_42" {
		t.Errorf("span attr task.id = %q, want t_42", attrs["task.id"])
	}
	if attrs["state.from"] != "INIT" {
		t.Errorf("span attr state.from = %q, want INIT", attrs["state.from"])
	}
	if attrs["state.to"] != "PLAN" {
		t.Errorf("span attr state.to = %q, want PLAN", attrs["state.to"])
	}
}

// TestTelemetryProjectErrorEventSetsErrorStatus pins that an error-class event
// (the "error" topic) projects to a span with Error status (§13.3.2).
func TestTelemetryProjectErrorEventSetsErrorStatus(t *testing.T) {
	tel := newTelemetryForTest()
	tel.StartRoot(context.Background(), "t_42")

	tel.Project(context.Background(),
		mkEnv(1, &event.ErrorEvent{Task: "t_42", Layer: "runtime", Code: "panic", Msg: "boom", Retry: false}))

	spans := tel.Spans()
	if len(spans) != 1 {
		t.Fatalf("Project recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status != SpanStatusError {
		t.Errorf("error event span status = %q, want %q", spans[0].Status, SpanStatusError)
	}
}

// TestTelemetryProjectWithoutRootIsLinkless pins the §13.3.2 "if ok" guard: an
// event whose task has no started root projects to a linkless span (no parent),
// not a panic or a dropped span.
func TestTelemetryProjectWithoutRootIsLinkless(t *testing.T) {
	tel := newTelemetryForTest()
	// No StartRoot for t_42.
	tel.Project(context.Background(),
		mkEnv(1, &event.TaskCompletedEvent{Task: "t_42"}))

	spans := tel.Spans()
	if len(spans) != 1 {
		t.Fatalf("linkless Project recorded %d spans, want 1", len(spans))
	}
	if spans[0].ParentTaskID != "" {
		t.Errorf("linkless span ParentTaskID = %q, want empty (no root)", spans[0].ParentTaskID)
	}
}

// TestTelemetryShutdownIsIdempotent pins §13.3.5: shutdown is the drain at
// close (no-op for in-memory), and is idempotent — calling it twice returns
// nil without error.
func TestTelemetryShutdownIsIdempotent(t *testing.T) {
	tel := newTelemetryForTest()
	if err := tel.shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := tel.shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v (should be idempotent)", err)
	}
}

// --- test helpers ---

var errBoom = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }

// mkEnv builds an Envelope with the given seq + event. The timestamp is
// omitted (deterministic; the projector doesn't read it).
func mkEnv(seq uint64, evt event.Event) event.Envelope {
	return event.Envelope{Seq: seq, Evt: evt}
}

// newTelemetryForTest builds a Telemetry with the in-memory stub tracer for
// these package-internal tests (no Config wiring needed yet — L12-009 wires
// Config into Start).
func newTelemetryForTest() *Telemetry {
	return newTelemetry(testConfig())
}

// TestTelemetrySpansAreRedacted pins the fourth boundary.
//
// §13.7.3 names three redaction boundaries — exec output, the log line, the
// Sentry event — and Infra.project fans every envelope out to four observers.
// Three of them were covered: logProjector holds a redactor, SentryHub holds a
// redactor, Metrics only ever reads names and counts. Telemetry was the fourth,
// and it took eventAttrs(env.Evt) verbatim: the whole event, JSON-decoded, every
// field. An assistant message that quotes a key, a tool result carrying an
// env dump, an error whose Msg embeds a token — all of it landed in Span.Attrs
// and, for error events, in Span.ErrMsg.
//
// The stub keeps spans in memory, which is why this was easy to miss: nothing
// leaves the process today. But Telemetry is documented as the swap point for
// the real OTel SDK, and the swap does not change Project — it changes where
// the spans go. A boundary that is only safe because its exporter is a slice is
// not a boundary.
func TestTelemetrySpansAreRedacted(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	tel := newTelemetryForTest()

	tel.Project(context.Background(), mkEnv(1, &event.AssistantMessageEvent{
		Task: "t_1", Text: "the key is " + secret, Final: true,
	}))
	tel.Project(context.Background(), mkEnv(2, &event.ErrorEvent{
		Task: "t_1", Msg: "auth failed for " + secret,
	}))

	for _, sp := range tel.Spans() {
		if s := NewSecrets(); s.WouldLeak(fmt.Sprint(sp.Attrs)) {
			t.Errorf("span %q attrs carry an unredacted secret: %v", sp.Name, sp.Attrs)
		}
		if strings.Contains(sp.ErrMsg, secret) {
			t.Errorf("span %q ErrMsg carries an unredacted secret: %q", sp.Name, sp.ErrMsg)
		}
	}
}

// TestTelemetryRootErrorIsRedacted covers the other way a string reaches a span.
// Project's input is an event, so redacting eventAttrs covers it; EndRoot's is
// an error the runtime hands in directly, and err.Error() goes straight to
// ErrMsg. A provider auth failure is exactly the error whose text tends to
// quote the credential it failed with, so the two paths need the same
// treatment.
func TestTelemetryRootErrorIsRedacted(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	tel := newTelemetryForTest()

	tel.StartRoot(context.Background(), "t_1")
	tel.EndRoot("t_1", errString("provider rejected "+secret))

	sp := tel.EndedRoots()["t_1"]
	if sp == nil {
		t.Fatal("EndRoot recorded no ended root")
	}
	if strings.Contains(sp.ErrMsg, secret) {
		t.Errorf("root span ErrMsg carries an unredacted secret: %q", sp.ErrMsg)
	}
}

// TestErrorSpanCarriesItsMessage pins a defect the redaction work above
// surfaced: an error span's ErrMsg was always empty.
//
// Project sets the status from isErrorEvent, then reaches for the text:
//
//	if m, ok := sp.Attrs["msg"]; ok { ... }
//
// but eventAttrs has already run every JSON key through attrKey, and attrKey
// maps "msg" → "error.msg" (§13.3.3 stable trace attribute). So the lookup
// misses on every error event ever projected, and the span goes out marked
// Error with nothing saying what the error was. The attribute itself was
// always there under the renamed key, which is why nothing noticed: a
// collector querying error.msg sees the text, and only the span's own status
// message is blank.
func TestErrorSpanCarriesItsMessage(t *testing.T) {
	tel := newTelemetryForTest()
	tel.Project(context.Background(), mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "disk full"}))

	spans := tel.Spans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Status != SpanStatusError {
		t.Errorf("status = %q, want %q", spans[0].Status, SpanStatusError)
	}
	if spans[0].ErrMsg != "disk full" {
		t.Errorf("ErrMsg = %q, want %q — the lookup key is the pre-rename JSON name, "+
			"so an error span reports a failure with no message", spans[0].ErrMsg, "disk full")
	}
}
