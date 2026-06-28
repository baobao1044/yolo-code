// Tests for L12-002 — Metrics: counters/histograms from §13.4.1 (unsampled).
// Metrics are the unsampled truth: where traces answer "what happened on this
// task," metrics answer "how is the system doing over time." Every event
// increments events.total{topic}; topic-specific events increment their
// counters (tool.calls.total{tool}, verify.verdicts.total{stage,status},
// patch.files.total). The stdlib-only stub records in-memory (no OTel metrics
// SDK), queryable via Snapshot, so counters are testable without a Prometheus
// scrape.
//
// Spec gaps documented in code (File 13 §13.4.1 table vs the actual event
// catalog): llm.tokens.total{model,direction} and cost.dollars.total/cost.loops.total
// can't be projected today — TokenEvent carries only a streamed Delta string
// (no tokens_in/out/model), and the catalog has cost.degraded/cost.abort but no
// cost.spend/cost.loop. Those counters land when the owning layers emit the
// fields; the projection switch is ready for them (no-op cases now).

package infra

import (
	"encoding/json"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// TestMetricsEventsTotalCountedByTopic pins §13.4.1 row 1: every event
// increments events.total, labeled by topic. After N events across M topics,
// the per-topic counters sum to N.
func TestMetricsEventsTotalCountedByTopic(t *testing.T) {
	m := newMetrics(testConfig())
	m.Record(mkEnv(1, &event.TaskStartedEvent{Task: "t_1"}))
	m.Record(mkEnv(2, &event.StateChangeEvent{Task: "t_1", From: "INIT", To: "PLAN", Why: "go"}))
	m.Record(mkEnv(3, &event.TaskCompletedEvent{Task: "t_1"}))
	m.Record(mkEnv(4, &event.StateChangeEvent{Task: "t_1", From: "PLAN", To: "DONE", Why: "ok"}))

	if got, want := m.Counter("events.total", labels{"topic": "state.change"}), int64(2); got != want {
		t.Errorf("events.total{topic=state.change} = %d, want %d", got, want)
	}
	if got, want := m.Counter("events.total", labels{"topic": "task.started"}), int64(1); got != want {
		t.Errorf("events.total{topic=task.started} = %d, want %d", got, want)
	}
	if got, want := m.Counter("events.total", labels{"topic": "task.completed"}), int64(1); got != want {
		t.Errorf("events.total{topic=task.completed} = %d, want %d", got, want)
	}
}

// TestMetricsToolCallsTotalCountedByTool pins §13.4.1 row: tool.result increments
// tool.calls.total{tool}. Two results for "ls" + one for "cat" → 2 and 1.
func TestMetricsToolCallsTotalCountedByTool(t *testing.T) {
	m := newMetrics(testConfig())
	m.Record(mkEnv(1, &event.ToolResultEvent{Task: "t_1", Tool: "ls", Obs: json.RawMessage(`{}`)}))
	m.Record(mkEnv(2, &event.ToolResultEvent{Task: "t_1", Tool: "ls", Obs: json.RawMessage(`{}`)}))
	m.Record(mkEnv(3, &event.ToolResultEvent{Task: "t_1", Tool: "cat", Obs: json.RawMessage(`{}`)}))

	if got, want := m.Counter("tool.calls.total", labels{"tool": "ls"}), int64(2); got != want {
		t.Errorf("tool.calls.total{tool=ls} = %d, want %d", got, want)
	}
	if got, want := m.Counter("tool.calls.total", labels{"tool": "cat"}), int64(1); got != want {
		t.Errorf("tool.calls.total{tool=cat} = %d, want %d", got, want)
	}
}

// TestMetricsVerifyVerdictsCountedByStageStatus pins §13.4.1 row: verification.*
// increments verify.verdicts.total{stage,status}. A verification.stage event
// with Stage="fmt" Status="pass" and another with Stage="test" Status="fail".
func TestMetricsVerifyVerdictsCountedByStageStatus(t *testing.T) {
	m := newMetrics(testConfig())
	m.Record(mkEnv(1, &event.VerificationStageEvent{Task: "t_1", Stage: "fmt", Status: "pass"}))
	m.Record(mkEnv(2, &event.VerificationStageEvent{Task: "t_1", Stage: "test", Status: "fail"}))
	m.Record(mkEnv(3, &event.VerificationStageEvent{Task: "t_1", Stage: "fmt", Status: "fail"}))

	if got, want := m.Counter("verify.verdicts.total", labels{"stage": "fmt", "status": "pass"}), int64(1); got != want {
		t.Errorf("verify.verdicts.total{stage=fmt,status=pass} = %d, want %d", got, want)
	}
	if got, want := m.Counter("verify.verdicts.total", labels{"stage": "fmt", "status": "fail"}), int64(1); got != want {
		t.Errorf("verify.verdicts.total{stage=fmt,status=fail} = %d, want %d", got, want)
	}
	if got, want := m.Counter("verify.verdicts.total", labels{"stage": "test", "status": "fail"}), int64(1); got != want {
		t.Errorf("verify.verdicts.total{stage=test,status=fail} = %d, want %d", got, want)
	}
}

// TestMetricsPatchFilesTotalCountsFiles pins §13.4.1 row: patch.applied
// increments patch.files.total by the number of files in the patch (NOT by 1
// per event — the metric counts files, the unit the patch touched).
func TestMetricsPatchFilesTotalCountsFiles(t *testing.T) {
	m := newMetrics(testConfig())
	m.Record(mkEnv(1, &event.PatchAppliedEvent{
		Task:  "t_1",
		Files: []event.PatchFile{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}},
	}))
	m.Record(mkEnv(2, &event.PatchAppliedEvent{
		Task:  "t_1",
		Files: []event.PatchFile{{Path: "d.go"}},
	}))

	if got, want := m.Counter("patch.files.total", labels{}), int64(4); got != want {
		t.Errorf("patch.files.total = %d, want %d (3 files + 1 file across two patches)", got, want)
	}
}

// TestMetricsCounterUnknownReturnsZero pins the zero-value: a counter/label
// combo never recorded returns 0, not a panic. Tests rely on this for absent
// metrics.
func TestMetricsCounterUnknownReturnsZero(t *testing.T) {
	m := newMetrics(testConfig())
	if got := m.Counter("never.recorded", labels{"topic": "x"}); got != 0 {
		t.Errorf("unrecorded counter = %d, want 0", got)
	}
}

// TestMetricsCardinalityDiscipline pins §13.4.3: task IDs, file paths, and tool
// argument strings are NEVER metric labels (they'd explode the backend's
// cardinality). Record a tool.result and assert the only label is "tool" —
// the task ID is NOT a label.
func TestMetricsCardinalityDiscipline(t *testing.T) {
	m := newMetrics(testConfig())
	m.Record(mkEnv(1, &event.ToolResultEvent{Task: "t_99", Tool: "ls", Obs: json.RawMessage(`{}`)}))

	// tool.calls.total{tool=ls} == 1; tool.calls.total{tool=ls,task=t_99} == 0
	// (the task ID is not a label).
	if got := m.Counter("tool.calls.total", labels{"tool": "ls"}); got != 1 {
		t.Errorf("tool.calls.total{tool=ls} = %d, want 1", got)
	}
	if got := m.Counter("tool.calls.total", labels{"tool": "ls", "task": "t_99"}); got != 0 {
		t.Errorf("tool.calls.total{tool=ls,task=t_99} = %d, want 0 (task ID must NOT be a label, §13.4.3)", got)
	}
}

// TestMetricsShutdownIsIdempotent pins the §13.3.5-equivalent drain for metrics:
// shutdown is a no-op for in-memory and idempotent.
func TestMetricsShutdownIsIdempotent(t *testing.T) {
	m := newMetrics(testConfig())
	if err := m.shutdown(nil); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := m.shutdown(nil); err != nil {
		t.Fatalf("second shutdown: %v (should be idempotent)", err)
	}
}
