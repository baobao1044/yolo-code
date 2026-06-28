// Tests for L12-003 — Structured logging (File 13 §13.5). One *slog.Logger
// for the whole agent; the root subscriber writes one DEBUG line per event
// with topic + task + the event's fields as structured attributes. The
// projection is the uniform machine-readable transcript; higher-severity lines
// are emitted by the owning layer, not Infra (§13.5.3).
//
// Redaction (§13.5.4 second boundary): every log line passes through the
// redactor before write. L12-003 wires a logRedactor seam so the projection is
// redaction-aware; L12-005 (Secrets registry) supplies the real redactor. A
// nil redactor passes through (no redaction) — the boundary exists, the
// registry fills it in.

package infra

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// captureLogger builds a logger writing to a buffer (instead of os.Stderr) so
// the test can inspect the emitted lines. Returns the Infra-shaped projector
// wrapper + the buffer.
func captureLogger(t *testing.T, cfg Config) (*logProjector, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	lp := newLogProjector(cfg, &buf)
	return lp, &buf
}

// TestLoggerOneDebugLinePerEvent pins §13.5.2: the root subscriber writes one
// DEBUG line per event, with topic + task as attrs. After N events, the buffer
// holds exactly N lines.
func TestLoggerOneDebugLinePerEvent(t *testing.T) {
	lp, buf := captureLogger(t, testConfig())

	lp.projectLog(mkEnv(1, &event.TaskStartedEvent{Task: "t_1", Session: "s_1", Goal: "demo"}))
	lp.projectLog(mkEnv(2, &event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"}))
	lp.projectLog(mkEnv(3, &event.TaskCompletedEvent{Task: "t_1"}))

	out := buf.String()
	if got, want := strings.Count(out, "\n"), 3; got != want {
		t.Fatalf("emitted %d lines, want %d (one DEBUG per event)\nout=%q", got, want, out)
	}
	// Each line carries topic + task.
	for _, wantTopic := range []string{"task.started", "state.change", "task.completed"} {
		if !strings.Contains(out, wantTopic) {
			t.Errorf("log line missing topic %q; out=%q", wantTopic, out)
		}
	}
	if !strings.Contains(out, "task") || !strings.Contains(out, "t_1") {
		t.Errorf("log line missing task attr; out=%q", out)
	}
}

// TestLoggerCarriesEventFieldsAsAttrs pins §13.5.2: the event's fields are
// structured attributes on the line (e.g. from=INIT, to=PLAN for state.change).
func TestLoggerCarriesEventFieldsAsAttrs(t *testing.T) {
	lp, buf := captureLogger(t, testConfig())

	lp.projectLog(mkEnv(1, &event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"}))

	out := buf.String()
	if !strings.Contains(out, "INIT") {
		t.Errorf("log missing from=INIT field; out=%q", out)
	}
	if !strings.Contains(out, "PLAN") {
		t.Errorf("log missing to=PLAN field; out=%q", out)
	}
}

// TestLoggerRedactsSecretFields pins §13.5.4 second boundary: a secret-bearing
// field value is masked before it hits the log. The redactor is injected (the
// real Secrets registry lands in L12-005); here we inject a redactor that
// masks a known token, and assert the raw token never reaches the buffer.
func TestLoggerRedactsSecretFields(t *testing.T) {
	cfg := testConfig()
	var buf bytes.Buffer
	lp := newLogProjector(cfg, &buf)
	lp.redactor = maskTokenRedactor{"ghp_supertoken123"} // a secret in an event field

	lp.projectLog(mkEnv(1, &event.AssistantMessageEvent{
		Task: "t_1", Text: "leaked ghp_supertoken123 in my reply", Final: true,
	}))

	out := buf.String()
	if strings.Contains(out, "ghp_supertoken123") {
		t.Errorf("log leaked the raw token; redaction boundary (§13.5.4) failed\nout=%q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Errorf("log missing redaction marker; out=%q", out)
	}
}

// TestLoggerNilRedactorPassesThrough pins the nil-safe seam: a nil redactor
// (no Secrets registry wired) passes values through unchanged. The boundary
// exists; L12-005 fills it.
func TestLoggerNilRedactorPassesThrough(t *testing.T) {
	cfg := testConfig()
	var buf bytes.Buffer
	lp := newLogProjector(cfg, &buf)
	lp.redactor = nil // no Secrets wired

	lp.projectLog(mkEnv(1, &event.AssistantMessageEvent{Task: "t_1", Text: "plain text", Final: true}))

	out := buf.String()
	if !strings.Contains(out, "plain text") {
		t.Errorf("nil redactor dropped the text; out=%q", out)
	}
}

// TestLoggerFormatHonored pins §13.5.1: "json" → JSON handler, "text" → text
// handler. A JSON-format line must parse as JSON (starts with '{'); a text
// line must not.
func TestLoggerFormatHonored(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		cfg := testConfig()
		cfg.Log.Format = "json"
		var buf bytes.Buffer
		lp := newLogProjector(cfg, &buf)
		lp.projectLog(mkEnv(1, &event.TaskCompletedEvent{Task: "t_1"}))
		out := strings.TrimSpace(buf.String())
		if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}") {
			t.Errorf("json format did not emit a JSON object; got %q", out)
		}
	})
	t.Run("text", func(t *testing.T) {
		cfg := testConfig()
		cfg.Log.Format = "text"
		var buf bytes.Buffer
		lp := newLogProjector(cfg, &buf)
		lp.projectLog(mkEnv(1, &event.TaskCompletedEvent{Task: "t_1"}))
		out := strings.TrimSpace(buf.String())
		if strings.HasPrefix(out, "{") {
			t.Errorf("text format emitted JSON; got %q", out)
		}
	})
}

// maskTokenRedactor is a test redactor that masks one known token string. It
// stands in for the real Secrets registry (L12-005) so the §13.5.4 boundary is
// testable before the registry exists.
type maskTokenRedactor struct{ token string }

func (m maskTokenRedactor) RedactAttrs(attrs []any) []any {
	out := make([]any, len(attrs))
	for i, a := range attrs {
		if s, ok := a.(string); ok {
			out[i] = strings.ReplaceAll(s, m.token, "REDACTED")
		} else {
			out[i] = a
		}
	}
	return out
}
