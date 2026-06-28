// Telemetry — OpenTelemetry integration (File 13 §13.3), stdlib-only stub
// (Sprint 7 zero-deps precedent). The real OTel SDK would construct a
// TracerProvider + BatchSpanProcessor + OTLP HTTP exporter here; instead this
// records spans in an in-memory store so the trace tree is testable without a
// collector. The Telemetry type is the swap point: a later hardening sprint
// replaces the recorder with the real SDK behind the same StartRoot/EndRoot/
// Project/shutdown surface, and no caller changes.
//
// The span-per-event model (§13.3.1): every event published to the bus becomes
// exactly one span, named after the event's topic, parented to the task root
// span for the event's CausalID, with attributes from the event's fields.
// StartRoot/EndRoot are called by L2 (the runtime) to bracket each task; Infra
// only stores the spans so Project can link children to roots.

package infra

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/yolo-code/yolo/internal/event"
)

// SpanStatus is the outcome of a span: unset, OK, or Error (mirrors OTel codes).
type SpanStatus string

const (
	SpanStatusUnset SpanStatus = ""
	SpanStatusOK    SpanStatus = "OK"
	SpanStatusError SpanStatus = "Error"
)

// Span is a recorded span (in-memory; the real SDK's trace.Span is the swap
// point). Attrs carries the event's fields as string→any (§13.3.3). ParentTaskID
// is the task root this span links to (empty when the event's task had no root
// started — §13.3.2's "if ok" guard).
type Span struct {
	Name         string
	ParentTaskID string
	Status       SpanStatus
	ErrMsg       string
	Attrs        map[string]any
}

// Telemetry projects events into spans. roots holds live task root spans keyed
// by taskID; endedRoots + spans are the in-memory record available to tests.
type Telemetry struct {
	mu         sync.Mutex
	roots      map[string]*Span
	endedRoots map[string]*Span
	spans      []Span
}

// newTelemetry constructs the in-memory stub tracer. cfg is accepted for the
// swap point (the real SDK reads cfg.OTel.Endpoint/SampleRate); the stub ignores
// it. log is nil here — L12-009 passes the Infra logger in.
func newTelemetry(cfg Config) *Telemetry {
	_ = cfg // stub ignores OTel config; the real SDK reads it
	return &Telemetry{
		roots:      make(map[string]*Span),
		endedRoots: make(map[string]*Span),
	}
}

// StartRoot opens the task root span (§13.3.2). Called once per task by L2 when
// it transitions into PLAN; Infra only stores the span so subsequent event
// spans can link to it. Returns a context + span to mirror the OTel API; the
// stub's span is the stored *Span.
func (t *Telemetry) StartRoot(ctx context.Context, taskID string) (context.Context, *Span) {
	sp := &Span{Name: "task", Attrs: map[string]any{"task.id": taskID}}
	t.mu.Lock()
	t.roots[taskID] = sp
	t.mu.Unlock()
	return ctx, sp
}

// EndRoot closes the task root span (§13.3.2): removes it from the live map
// (LoadAndDelete semantics) and, when err is non-nil, marks its status Error
// and records the message. Called by L2 when the task leaves the EXECUTE/VERIFY/
// PATCH cycle.
func (t *Telemetry) EndRoot(taskID string, err error) {
	t.mu.Lock()
	sp, ok := t.roots[taskID]
	if ok {
		delete(t.roots, taskID)
	}
	if sp != nil {
		if err != nil {
			sp.Status = SpanStatusError
			sp.ErrMsg = err.Error()
		} else if sp.Status == SpanStatusUnset {
			sp.Status = SpanStatusOK
		}
		t.endedRoots[taskID] = sp
	}
	t.mu.Unlock()
}

// Project turns one event into one span, parented to its task's root (§13.3.1).
// Called from the root subscriber goroutine. Never blocks on export — the stub
// appends to an in-memory slice; the real SDK's span.End queues for batch
// export on its own goroutine. An event whose task has no started root projects
// to a linkless span (empty ParentTaskID), not a panic.
func (t *Telemetry) Project(ctx context.Context, env event.Envelope) {
	_ = ctx
	taskID := string(env.Evt.CausalID())
	t.mu.Lock()
	_, hasRoot := t.roots[taskID]
	t.mu.Unlock()

	sp := Span{
		Name:         string(env.Evt.Type()),
		ParentTaskID: "", // linkless unless a root is live
		Attrs:        eventAttrs(env.Evt),
	}
	if hasRoot {
		sp.ParentTaskID = taskID
	}
	if isErrorEvent(env.Evt.Type()) {
		sp.Status = SpanStatusError
		if m, ok := sp.Attrs["msg"]; ok {
			if ms, ok := m.(string); ok {
				sp.ErrMsg = ms
			}
		}
	}
	t.mu.Lock()
	t.spans = append(t.spans, sp)
	t.mu.Unlock()
}

// Roots returns a snapshot of the live task root spans (taskID → *Span). Tests
// use it to assert StartRoot recorded a root and EndRoot removed it.
func (t *Telemetry) Roots() map[string]*Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]*Span, len(t.roots))
	for k, v := range t.roots {
		out[k] = v
	}
	return out
}

// EndedRoots returns a snapshot of the ended task root spans. Tests assert
// EndRoot recorded the root with its final status/err.
func (t *Telemetry) EndedRoots() map[string]*Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]*Span, len(t.endedRoots))
	for k, v := range t.endedRoots {
		out[k] = v
	}
	return out
}

// Spans returns a snapshot of the recorded event spans (one per Project call,
// in projection order). Tests assert the §13.3.1 invariant: count == events,
// names == topics, attrs carry event fields.
func (t *Telemetry) Spans() []Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Span, len(t.spans))
	copy(out, t.spans)
	return out
}

// shutdown is the §13.3.5 drain at close. For the in-memory stub it's a no-op
// (nothing to flush — spans are already in the store); the real SDK would
// ForceFlush the batch processor then shut the exporter down. Idempotent.
func (t *Telemetry) shutdown(ctx context.Context) error {
	_ = ctx
	return nil
}

// eventAttrs marshals an event to a map of string→any for span attributes
// (§13.3.3). The JSON tag is the attribute key; the value is the JSON-decoded
// field. task.id is always added from CausalID so traces are queryable even
// when an event omits it from its JSON (some use a non-json TaskID field).
func eventAttrs(e event.Event) map[string]any {
	attrs := map[string]any{"task.id": string(e.CausalID())}
	raw, err := json.Marshal(e)
	if err != nil {
		return attrs
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return attrs
	}
	// Map JSON keys to the §13.3.3 attribute keys where a rename is in force;
	// otherwise attach as-is with the JSON key.
	for k, v := range m {
		attrs[attrKey(k)] = v
	}
	return attrs
}

// attrKey maps a JSON field name to its §13.3.3 span attribute key where the
// stable trace attribute differs from the wire field (e.g. "from" → "state.from"
// for state.change events). Unknown keys pass through unchanged.
func attrKey(jsonKey string) string {
	switch jsonKey {
	case "from":
		return "state.from"
	case "to":
		return "state.to"
	case "tool":
		return "tool.name"
	case "msg":
		return "error.msg"
	}
	return jsonKey
}

// isErrorEvent reports whether a topic is error-class — Project marks these
// spans with Error status (§13.3.2 env.Err != "" path). cost.abort is also
// error-class (a budget incident, §13.6.2).
func isErrorEvent(topic event.Topic) bool {
	return topic == "error" || topic == "cost.abort"
}
