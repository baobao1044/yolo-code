// Structured logging (File 13 §13.5). One *slog.Logger for the whole agent,
// constructed here; layers receive it (or a child with added attrs) and never
// build their own. The root subscriber writes one DEBUG line per event with
// topic + task + the event's fields as structured attributes (§13.5.2) — the
// uniform machine-readable transcript. Higher-severity lines are emitted by
// the owning layer, not Infra (§13.5.3).
//
// Redaction (§13.5.4 second boundary): every log line passes through the
// redactor before write. L12-003 wires a logRedactor seam so the projection is
// redaction-aware; L12-005 (Secrets registry) supplies the real redactor. A
// nil redactor passes values through unchanged — the boundary exists, the
// registry fills it in. Two boundaries (exec output + log) because logs outlive
// the in-memory raw output: defense in depth.

package infra

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/baobao1044/yolo-code/internal/event"
)

// logRedactor redacts secret-bearing values in a slog attribute slice before
// the line is written (§13.5.4). The Secrets registry (L12-005) implements
// this; a nil redactor passes through. Kept an interface so the logger doesn't
// import the registry before L12-005 wires it.
type logRedactor interface {
	RedactAttrs(attrs []any) []any
}

// logProjector wraps the slog logger + the redactor and projects one event
// into one DEBUG line. L12-009 folds it into the Infra aggregate; L12-003
// exposes it directly so its tests don't need the whole aggregate.
type logProjector struct {
	log      *slog.Logger
	redactor logRedactor
}

// newLogProjector builds the projector writing to w (os.Stderr in prod, a
// buffer in tests). cfg.Log.Format selects text vs JSON; cfg.Log.Level is the
// threshold (the projector emits DEBUG, so a level above DEBUG silences it —
// the root subscriber is the only DEBUG emitter, §13.5.2).
func newLogProjector(cfg Config, w io.Writer) *logProjector {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{
		Level:     slog.Level(cfg.Log.Level),
		AddSource: true,
	}
	var handler slog.Handler
	switch cfg.Log.Format {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default: // "text" — human-readable, the TUI-friendly default
		handler = slog.NewTextHandler(w, opts)
	}
	log := slog.New(handler).With(
		slog.String("host.id", cfg.HostID),
		slog.String("version", cfg.Version),
	)
	return &logProjector{log: log}
}

// projectLog writes one DEBUG line for env (§13.5.2): topic + task + the
// event's fields as attrs, redacted via the redactor (§13.5.4). Called from the
// root subscriber goroutine.
func (lp *logProjector) projectLog(env event.Envelope) {
	attrs := make([]any, 0, 12)
	attrs = append(attrs, "topic", string(env.Evt.Type()))
	attrs = append(attrs, "task", string(env.Evt.CausalID()))
	attrs = append(attrs, "seq", env.Seq)
	attrs = appendAttrs(attrs, env.Evt)
	if lp.redactor != nil {
		attrs = lp.redactor.RedactAttrs(attrs)
	}
	lp.log.DebugContext(context.Background(), "event", attrs...)
}

// appendAttrs flattens an event's JSON fields into the slog attr slice
// (alternating key, value). The JSON tag is the attr key; the value is the
// decoded field (string/float64/bool/nil). task.id is skipped (projectLog adds
// "task" from CausalID instead, avoiding a duplicate).
func appendAttrs(attrs []any, e event.Event) []any {
	raw, err := json.Marshal(e)
	if err != nil {
		return attrs
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return attrs
	}
	for k, v := range m {
		attrs = append(attrs, k, v)
	}
	return attrs
}
