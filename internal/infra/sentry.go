// Sentry integration (File 13 §13.6), stdlib-only stub (Sprint 7 zero-deps
// precedent — no getsentry/sentry-go). Opt-in via Config.Sentry.DSN: an empty
// DSN yields a nil hub (§13.6.1); every method on a nil hub is a no-op
// (nil-receiver safe). With a DSN the stub records captured events in-memory
// (no network); the real Sentry SDK swaps behind the Report/Flush surface.
//
// Only error-class events are forwarded (§13.6.2): error (recoverable tagged
// info, fatal error) and cost.abort (a budget incident). Normal tool calls,
// token streams, and reflection notes are NOT sent. Redaction (§13.6.3 third
// boundary): captured event extras pass through the redactor (RedactMap) so a
// secret in an error's fields is masked before the (would-be) upload. L12-004
// wires a sentryRedactor seam; L12-005 (Secrets registry) supplies the real one.

package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yolo-code/yolo/internal/event"
)

// sentryRedactor redacts secret-bearing values in a map before a captured event
// is stored/uploaded (§13.6.3). The Secrets registry (L12-005) implements this;
// a nil redactor passes through. Kept an interface so the hub doesn't import
// the registry before L12-005 wires it.
type sentryRedactor interface {
	RedactMap(map[string]any) map[string]any
}

// CapturedEvent is a recorded Sentry event (in-memory; the real SDK's
// sentry.Event is the swap point). Level is "error"/"warning"/"info"; Message
// is the human-readable summary; Tags are low-cardinality labels (task);
// Extras are the redacted event fields.
type CapturedEvent struct {
	Level   string
	Message string
	Tags    map[string]string
	Extras  map[string]any
}

// SentryHub is the opt-in Sentry reporter. nil when opt-out (no DSN); all
// methods are nil-receiver safe. captured holds the in-memory record for tests.
type SentryHub struct {
	redactor sentryRedactor
	captured []CapturedEvent
}

// newSentry builds the hub. Returns nil (opt-out) when cfg.Sentry.DSN is empty
// (§13.6.1). With a DSN the stub returns a hub that records in-memory — the
// real SDK's init (with BeforeSend redaction) swaps behind this; a failed init
// is fail-silent (nil hub).
func newSentry(cfg Config) *SentryHub {
	if cfg.Sentry.DSN == "" {
		return nil
	}
	// The real sentry.Init would run here; the stub succeeds unconditionally
	// (no network). Fail-silent if a real init ever errors → return nil.
	return &SentryHub{}
}

// Report forwards an error-class event to the hub (§13.6.2). Non-error events
// are ignored. Non-blocking — the stub appends to a slice; the real SDK's
// CaptureEvent enqueues for async flush. Nil-receiver safe.
func (h *SentryHub) Report(env event.Envelope) {
	if h == nil {
		return
	}
	topic := env.Evt.Type()
	if !isErrorEvent(topic) {
		return // §13.6.2: only error + cost.abort are forwarded
	}
	ce := CapturedEvent{
		Level:   "error",
		Message: sentryMessage(env.Evt),
		Tags:    map[string]string{"task": string(env.Evt.CausalID()), "topic": string(topic)},
		Extras:  sentryExtras(env.Evt),
	}
	if h.redactor != nil {
		ce.Extras = h.redactor.RedactMap(ce.Extras)
		// The message may carry a secret too (e.g. an error msg quoting a
		// token); redact it via the extras path by treating it as a one-field map.
		redacted := h.redactor.RedactMap(map[string]any{"msg": ce.Message})
		if s, ok := redacted["msg"].(string); ok {
			ce.Message = s
		}
	}
	h.captured = append(h.captured, ce)
}

// Captured returns a snapshot of the recorded events (test accessor; the real
// SDK would have no equivalent — it uploads).
func (h *SentryHub) Captured() []CapturedEvent {
	if h == nil {
		return nil
	}
	out := make([]CapturedEvent, len(h.captured))
	copy(out, h.captured)
	return out
}

// Flush blocks up to the deadline draining the queue (§13.6.1). For the
// in-memory stub it's a no-op (nothing to flush); the real SDK's
// sentry.Flush(time.Until(d)) runs here. Nil-receiver safe; idempotent.
func (h *SentryHub) Flush(ctx context.Context) error {
	if h == nil {
		return nil
	}
	d, ok := ctx.Deadline()
	if !ok {
		d = time.Now().Add(2 * time.Second)
	}
	_ = time.Until(d) // real SDK: sentry.Flush(this); stub: no-op
	return nil
}

// sentryMessage extracts the human-readable message for a captured event. For
// error events it's the Msg field; for cost.abort the Reason field; otherwise
// the topic (defensive — Report only forwards error-class).
func sentryMessage(e event.Event) string {
	if m, ok := strField(e, "msg"); ok {
		return m
	}
	if r, ok := strField(e, "reason"); ok {
		return r
	}
	return string(e.Type())
}

// sentryExtras flattens an event's JSON fields into the extras map (the
// would-be Sentry context). The real SDK's BeforeSend redacts this; the stub
// redacts via the injected sentryRedactor.
func sentryExtras(e event.Event) map[string]any {
	raw, err := json.Marshal(e)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}
