// Tests for L12-004 — Sentry opt-in hub (File 13 §13.6). Opt-in via DSN: no
// DSN → nil hub → every method a no-op (nil-receiver safe). With a DSN the
// stdlib stub (Sprint 7 zero-deps precedent — no getsentry/sentry-go) records
// captured events in-memory; the real SDK swaps behind the Report/Flush surface.
// Only error-class events (error, cost.abort) are forwarded (§13.6.2).
//
// Redaction (§13.6.3 third boundary): captured event extras pass through the
// redactor (RedactMap) so a secret in an error's fields is masked before the
// (would-be) Sentry upload. L12-004 wires a sentryRedactor seam; L12-005
// (Secrets registry) supplies the real one.

package infra

import (
	"context"
	"strings"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestSentryNoDSNReturnsNilHub pins §13.6.1 opt-in: an empty DSN yields a nil
// hub, and every method on a nil hub is a no-op (no panic).
func TestSentryNoDSNReturnsNilHub(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = ""
	hub := newSentry(cfg)
	if hub != nil {
		t.Fatalf("empty DSN returned non-nil hub: %v", hub)
	}
	// Nil-hub methods must be safe.
	hub.Report(mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "boom"}))
	if err := hub.Flush(context.Background()); err != nil {
		t.Errorf("nil hub Flush returned err: %v", err)
	}
}

// TestSentryWithDSNCapturesErrorEvent pins §13.6.2: an error event forwarded to
// a DSN-wired hub produces one captured record (level error, message from the
// event, task tag). The stub records in-memory (no network); Captured() is the
// test accessor.
func TestSentryWithDSNCapturesErrorEvent(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	if hub == nil {
		t.Fatal("DSN set but newSentry returned nil hub")
	}
	hub.Report(mkEnv(1, &event.ErrorEvent{Task: "t_1", Layer: "runtime", Code: "panic", Msg: "boom"}))

	captured := hub.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured %d events, want 1", len(captured))
	}
	if captured[0].Level != "error" {
		t.Errorf("captured level = %q, want error", captured[0].Level)
	}
	if !strings.Contains(captured[0].Message, "boom") {
		t.Errorf("captured message = %q, want it to contain boom", captured[0].Message)
	}
	if captured[0].Tags["task"] != "t_1" {
		t.Errorf("captured task tag = %q, want t_1", captured[0].Tags["task"])
	}
}

// TestSentryWithDSNCapturesCostAbort pins §13.6.2: cost.abort (a budget
// incident) is also error-class and forwarded.
func TestSentryWithDSNCapturesCostAbort(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	hub.Report(mkEnv(1, &event.CostAbortEvent{Task: "t_1", Reason: "spend cap"}))

	captured := hub.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured %d events, want 1 (cost.abort is error-class)", len(captured))
	}
	if !strings.Contains(captured[0].Message, "spend cap") {
		t.Errorf("cost.abort message = %q, want spend cap reason", captured[0].Message)
	}
}

// TestSentryIgnoresNonErrorEvents pins §13.6.2: a normal tool result / token
// stream / reflection note is NOT forwarded to Sentry.
func TestSentryIgnoresNonErrorEvents(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	hub.Report(mkEnv(1, &event.ToolResultEvent{Task: "t_1", Tool: "ls"}))
	hub.Report(mkEnv(2, &event.AssistantMessageEvent{Task: "t_1", Text: "done", Final: true}))

	if got := len(hub.Captured()); got != 0 {
		t.Errorf("captured %d events, want 0 (non-error events must not be forwarded)", got)
	}
}

// TestSentryRedactsExtras pins §13.6.3 third boundary: a secret in an error
// event's fields is masked in the captured event's extras (the would-be Sentry
// context) before upload. The redactor is injected (real Secrets registry lands
// in L12-005).
func TestSentryRedactsExtras(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	hub.redactor = maskMapRedactor{"ghp_supertoken123"}

	hub.Report(mkEnv(1, &event.ErrorEvent{Task: "t_1", Msg: "leaked ghp_supertoken123 here"}))

	captured := hub.Captured()
	if len(captured) != 1 {
		t.Fatalf("captured %d, want 1", len(captured))
	}
	for _, v := range captured[0].Extras {
		if s, ok := v.(string); ok && strings.Contains(s, "ghp_supertoken123") {
			t.Errorf("captured extra leaked the raw token; §13.6.3 boundary failed: %q", s)
		}
	}
	if !strings.Contains(captured[0].Message, "REDACTED") {
		t.Errorf("captured message not redacted; got %q", captured[0].Message)
	}
}

// TestSentryFlushIsIdempotent pins §13.6.1: Flush is a no-op for the in-memory
// stub and idempotent.
func TestSentryFlushIsIdempotent(t *testing.T) {
	cfg := testConfig()
	cfg.Sentry.DSN = "https://fake@stub/s"
	hub := newSentry(cfg)
	if err := hub.Flush(context.Background()); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if err := hub.Flush(context.Background()); err != nil {
		t.Fatalf("second Flush: %v (idempotent)", err)
	}
}

// maskMapRedactor is a test redactor masking one token in string values of a
// map. Stands in for the real Secrets registry (L12-005) so the §13.6.3 boundary
// is testable before the registry exists.
type maskMapRedactor struct{ token string }

func (m maskMapRedactor) RedactMap(mp map[string]any) map[string]any {
	out := make(map[string]any, len(mp))
	for k, v := range mp {
		if s, ok := v.(string); ok {
			out[k] = strings.ReplaceAll(s, m.token, "REDACTED")
		} else {
			out[k] = v
		}
	}
	return out
}
