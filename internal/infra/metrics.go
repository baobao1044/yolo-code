// Metrics — counters & histograms (File 13 §13.4), stdlib-only stub (Sprint 7
// zero-deps precedent). The real OTel metrics SDK would construct a
// MeterProvider + PeriodicReader + OTLP exporter; instead Metrics records
// counters and histograms in-memory, queryable via Counter/Histogram, so the
// §13.4.1 table is testable without a Prometheus scrape. Metrics is the swap
// point — a later hardening sprint replaces the recorder behind the same
// Record/shutdown surface.
//
// Metrics are the UNSAMPLED truth (§13.4.1): budgets and counters must be
// exact, so Record runs on every event regardless of the trace sample rate.
// Cardinality discipline (§13.4.3): task IDs, file paths, and tool argument
// strings are NEVER metric labels — they'd explode the backend's cardinality.
// task_id appears on cost.loops.total only (bounded by the concurrency cap);
// that counter lands when cost.loop events exist (spec gap — see Record).

package infra

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// labels is the metric label set (string→string). Kept a local type so the
// cardinality discipline (§13.4.3) is enforced at the call site: only bounded,
// low-cardinality keys belong here. The real SDK's attribute set swaps in
// behind this type.
type labels map[string]string

// counterKey is the composite identity of a counter reading: the metric name
// + the sorted label set. Two Records with the same name + labels accumulate.
type counterKey struct {
	name   string
	labels string // deterministic encoding of the label map
}

// histogramKey is the same for a histogram's recorded sample.
type histogramKey struct {
	name   string
	labels string
}

// Metrics records counters and histograms in-memory. Counters are int64; the
// §13.4.1 table mixes int64 (counts) and float64 (cost.dollars) — float
// counters store as int64 scaled to micro-dollars when cost.spend lands (spec
// gap). Histograms store the raw int64 samples for percentile computation.
type Metrics struct {
	mu         sync.Mutex
	counters   map[counterKey]int64
	histograms map[histogramKey][]int64
}

// newMetrics constructs the in-memory stub. cfg is accepted for the swap point
// (the real SDK reads cfg.OTel.MetricInterval); the stub ignores it.
func newMetrics(cfg Config) *Metrics {
	_ = cfg
	return &Metrics{
		counters:   make(map[counterKey]int64),
		histograms: make(map[histogramKey][]int64),
	}
}

// Record projects one event into the relevant counters/histograms (§13.4.1).
// Called from the root subscriber goroutine alongside Telemetry.Project. Every
// event increments events.total{topic}; topic-specific cases add the rest.
func (m *Metrics) Record(env event.Envelope) {
	topic := string(env.Evt.Type())
	m.addCounter("events.total", labels{"topic": topic}, 1)

	switch env.Evt.Type() {
	case "tool.result":
		if tool, ok := strField(env.Evt, "tool"); ok {
			m.addCounter("tool.calls.total", labels{"tool": tool}, 1)
		}
	case "verification.stage":
		stage, _ := strField(env.Evt, "stage")
		status, _ := strField(env.Evt, "status")
		m.addCounter("verify.verdicts.total", labels{"stage": stage, "status": status}, 1)
	case "patch.applied":
		if n := fileCount(env.Evt); n > 0 {
			m.addCounter("patch.files.total", labels{}, int64(n))
		}
	case "cost.abort", "cost.degraded":
		// cost.* counters: cost.dollars.total{task_kind} and cost.loops.total
		// {task_id} need cost.spend/cost.loop events which the catalog doesn't
		// carry yet (spec gap). cost.abort/cost.degraded are incident signals,
		// already covered by events.total{topic}. No extra counter now.
	}
}

// addCounter increments a named counter under a label set by n. Thread-safe.
func (m *Metrics) addCounter(name string, lbls labels, n int64) {
	k := counterKey{name: name, labels: encodeLabels(lbls)}
	m.mu.Lock()
	m.counters[k] += n
	m.mu.Unlock()
}

// Counter returns the current value of a named counter under a label set. A
// never-recorded counter returns 0 (no panic) — tests rely on this for absent
// metrics.
func (m *Metrics) Counter(name string, lbls labels) int64 {
	k := counterKey{name: name, labels: encodeLabels(lbls)}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[k]
}

// Histogram returns the samples recorded for a named histogram under a label
// set. A never-recorded histogram returns nil.
func (m *Metrics) Histogram(name string, lbls labels) []int64 {
	k := histogramKey{name: name, labels: encodeLabels(lbls)}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, len(m.histograms[k]))
	copy(out, m.histograms[k])
	return out
}

// shutdown is the §13.4 drain at close. For the in-memory stub it's a no-op
// (nothing to flush); the real SDK would shut the periodic reader down.
// Idempotent.
func (m *Metrics) shutdown(ctx context.Context) error {
	_ = ctx
	return nil
}

// encodeLabels turns a label map into a deterministic string key (sorted
// "k=v|k=v") so two Records with the same labels accumulate regardless of map
// iteration order.
func encodeLabels(lbls labels) string {
	if len(lbls) == 0 {
		return ""
	}
	keys := make([]string, 0, len(lbls))
	for k := range lbls {
		keys = append(keys, k)
	}
	// Simple stable sort (small label sets — usually 1-2 keys); avoids pulling
	// sort just for this.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += "|"
		}
		out += k + "=" + lbls[k]
	}
	return out
}

// strField extracts a string field from an event by JSON key (the event's
// struct tag). Returns ("", false) if the field is absent or non-string.
func strField(e event.Event, key string) (string, bool) {
	raw, err := json.Marshal(e)
	if err != nil {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// fileCount returns the number of files in a patch.applied event's Files field.
// Returns 0 for non-patch events or a malformed payload.
func fileCount(e event.Event) int {
	raw, err := json.Marshal(e)
	if err != nil {
		return 0
	}
	var p struct {
		Files []json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return 0
	}
	return len(p.Files)
}
