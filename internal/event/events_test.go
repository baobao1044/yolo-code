package event

import (
	"encoding/json"
	"testing"
	"time"
)

// sampleEvents is one concrete instance of every catalog event, keyed by its
// topic. TestRoundTripAllCatalogEvents iterates this to assert that every
// registered type survives an Envelope marshal/unmarshal cycle with its Type,
// CausalID, and payload intact.
func sampleEvents() map[Topic]Event {
	const t = TaskID("t_42")
	return map[Topic]Event{
		"task.started":         &TaskStartedEvent{Task: "t_01", Session: "s_1", Goal: "fix bug"},
		"task.completed":       &TaskCompletedEvent{Task: "t_01"},
		"task.cancelled":       &TaskCancelledEvent{Task: "t_01", Reason: "user", Partial: "half-done"},
		"task.paused":          &TaskPausedEvent{Task: "t_01"},
		"task.checkpoint":      &CheckpointEvent{Task: "t_01", Name: "pre-edit", Snapshot: json.RawMessage(`{"sha":"abc"}`)},
		"task.restored":        &RestoredEvent{Task: "t_01", Name: "pre-edit"},
		"task.undone":          &UndoneEvent{Task: "t_01", Entry: json.RawMessage(`{"i":1}`)},
		"state.change":         &StateChangeEvent{Task: t, From: "PLAN", To: "EXECUTE", Why: "plan-ready"},
		"context.built":        &ContextBuiltEvent{Task: t},
		"approval.request":     &ApprovalRequestEvent{Task: t, Tool: "shell", Summary: "rm -rf x", Preview: "...", Risk: "high"},
		"observation.received": &ObservationEvent{Task: t, Tool: "read_file", Obs: json.RawMessage(`{"lines":12}`)},
		"verification.failed":  &VerificationFailedEvent{Task: t, Reason: "test failed"},
		"reflection.note":      &ReflectionEvent{Task: t, Note: "reconsider approach"},
		"patch.applied":        &PatchAppliedEvent{Task: t, Snapshot: json.RawMessage(`{"sha":"def"}`)},
		"llm.token":            &TokenEvent{Task: t, Delta: "Hel"},
		"llm.thinking":         &ThinkingEvent{Task: t, Delta: "thinking..."},
		"assistant.message":    &AssistantMessageEvent{Task: t, Text: "Hello", Final: true},
		"tool.call":            &ToolCallEvent{Task: t, Tool: "read_file", Args: json.RawMessage(`{"path":"a.go"}`), Reason: "need to see it"},
		"cost.degraded":        &CostDegradedEvent{Task: t, Stage: "reflection_disabled"},
		"cost.abort":           &CostAbortEvent{Task: t, Reason: "spend cap"},
		"tool.result":          &ToolResultEvent{Task: t, Tool: "read_file", Obs: json.RawMessage(`{"ok":true}`)},
		"memory.update":        &MemoryUpdateEvent{Task: t, Store: "preference", Items: 3},
		"user.submit":          &UserSubmitEvent{Text: "do X", Attachments: []json.RawMessage{json.RawMessage(`{"ref":"f1"}`)}},
		"user.cancel":          &UserCancelEvent{Task: t},
		"user.approve":         &UserApproveEvent{Task: "t_01", ApprovalID: "a_1"},
		"user.reject":          &UserRejectEvent{Task: "t_01", ApprovalID: "a_1", Reason: "nope"},
		"user.pause":           &UserPauseEvent{Task: t},
		"user.resume":          &UserResumeEvent{Task: t},
		"user.quit":            &UserQuitEvent{},
		"coord.task.assign":    &TaskAssignEvent{PlanID: "p_1", TodoID: "td_1", Agent: "coder", Brief: "write X", Context: []string{"ctx"}},
		"coord.plan.ready":     &PlanReadyEvent{PlanID: "p_1", Plan: json.RawMessage(`{"todos":3}`)},
		"coord.code.ready":     &CodeReadyEvent{PlanID: "p_1", TodoID: "td_1", Diff: "@@", SelfReport: "done"},
		"coord.review.verdict": &ReviewVerdictEvent{PlanID: "p_1", TodoID: "td_1", Approved: true, Comments: []string{"good"}},
		"coord.test.report":    &TestReportEvent{PlanID: "p_1", TodoID: "td_1", Passed: true, Output: "ok"},
		"error":                &ErrorEvent{Task: t, Layer: "cognitive", Code: "timeout", Msg: "slow", Retry: true},
	}
}

// TestCatalogHasAll16TopicGroups verifies the registry covers every topic group
// from File 05 §5.4.9. A missing group is a wire-contract regression.
func TestCatalogHasAll16TopicGroups(t *testing.T) {
	want := []Topic{
		"task.started", "task.completed", "task.cancelled", "task.paused",
		"task.checkpoint", "task.restored", "task.undone",
		"state.change", "context.built", "approval.request",
		"observation.received", "verification.failed", "reflection.note",
		"patch.applied",
		"llm.token", "llm.thinking", "assistant.message", "tool.call",
		"cost.degraded", "cost.abort",
		"tool.result", "memory.update",
		"user.submit", "user.cancel", "user.approve", "user.reject",
		"user.pause", "user.resume", "user.quit",
		"coord.task.assign", "coord.plan.ready", "coord.code.ready",
		"coord.review.verdict", "coord.test.report",
		"error",
	}
	for _, topic := range want {
		if _, ok := factoryFor(topic); !ok {
			t.Errorf("topic %q has no registered factory; catalog is incomplete", topic)
		}
	}
}

// TestRoundTripAllCatalogEvents is the L3-006 headline: every catalog event
// survives Envelope JSON round-trip with Type, CausalID, and payload intact.
func TestRoundTripAllCatalogEvents(t *testing.T) {
	samples := sampleEvents()
	if len(samples) < 33 {
		t.Errorf("sample table has %d events, want at least 33 (full catalog)", len(samples))
	}
	for topic, want := range samples {
		want := want
		t.Run(string(topic), func(t *testing.T) {
			// Type + CausalID must match the topic/task before marshaling.
			if want.Type() != topic {
				t.Fatalf("Type() = %q, want %q (struct wired to wrong topic)", want.Type(), topic)
			}
			wantTask := want.CausalID()

			env := Envelope{Seq: 7, At: time.Now().UTC(), Evt: want}
			data, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			// The on-disk form carries the schema version.
			var probe struct {
				V int `json:"v"`
			}
			if err := json.Unmarshal(data, &probe); err != nil {
				t.Fatalf("unmarshal version probe: %v", err)
			}
			if probe.V != Version {
				t.Errorf("v = %d, want %d", probe.V, Version)
			}

			var got Envelope
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Seq != env.Seq {
				t.Errorf("Seq = %d, want %d", got.Seq, env.Seq)
			}
			if got.Evt == nil {
				t.Fatal("unmarshaled Envelope.Evt is nil; factory did not reconstruct the event")
			}
			if got.Evt.Type() != topic {
				t.Errorf("round-trip Type() = %q, want %q", got.Evt.Type(), topic)
			}
			if got.Evt.CausalID() != wantTask {
				t.Errorf("round-trip CausalID() = %q, want %q", got.Evt.CausalID(), wantTask)
			}
			// The concrete event must round-trip byte-for-byte: re-marshal and
			// compare the payload bytes, which proves no field was dropped.
			wantPayload, _ := json.Marshal(env.Evt)
			gotPayload, _ := json.Marshal(got.Evt)
			if string(wantPayload) != string(gotPayload) {
				t.Errorf("payload drifted on round-trip\n want %s\n got  %s", wantPayload, gotPayload)
			}
		})
	}
}

// TestEnvelopeVersionIsOne pins the wire schema version to 1 (File 05 §5.4.10).
// A bump is a breaking change requiring a log-reader migration.
func TestEnvelopeVersionIsOne(t *testing.T) {
	if Version != 1 {
		t.Errorf("Version = %d, want 1 (a bump is a breaking log-reader change)", Version)
	}
}
