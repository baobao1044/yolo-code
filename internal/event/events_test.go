package event

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
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
		"task.failed":          &TaskFailedEvent{Task: "t_01", Reason: "planner returned no plan"},
		"task.paused":          &TaskPausedEvent{Task: "t_01"},
		"task.checkpoint":      &CheckpointEvent{Task: "t_01", Name: "pre-edit", Snapshot: json.RawMessage(`{"sha":"abc"}`)},
		"task.restored":        &RestoredEvent{Task: "t_01", Name: "pre-edit"},
		"task.undone":          &UndoneEvent{Task: "t_01", Entry: json.RawMessage(`{"i":1}`)},
		"state.change":         &StateChangeEvent{Task: t, From: "PLAN", To: "EXECUTE", Why: "plan-ready"},
		"context.built":        &ContextBuiltEvent{Task: t},
		"approval.request":     &ApprovalRequestEvent{Task: t, ApprovalID: "a_1", Tool: "shell", Summary: "rm -rf x", Preview: "...", Risk: "high"},
		"observation.received": &ObservationEvent{Task: t, Tool: "read_file", Obs: json.RawMessage(`{"lines":12}`)},
		"verification.failed":  &VerificationFailedEvent{Task: t, Reason: "test failed"},
		"reflection.note":      &ReflectionEvent{Task: t, Note: "reconsider approach"},
		"patch.applied": &PatchAppliedEvent{
			Task:       t,
			Snapshot:   json.RawMessage(`{"sha":"def"}`),
			Files:      []PatchFile{{Path: "main.go", Insertions: 3, Deletions: 1, New: false}},
			Insertions: 3,
			Deletions:  1,
		},
		"verification.stage":   &VerificationStageEvent{Task: t, Stage: "lint", Status: "pass", Detail: "vet clean"},
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
		"coord.plan.done":      &PlanDoneEvent{PlanID: "p_1", Done: true, Merged: true, Summary: "all todos green"},
		"cost.incurred":        &CostIncurredEvent{Task: t, Tool: "shell", Dollars: 0.0125, Reason: "tool call"},
		"scope.enter":          &ScopeEnterEvent{Task: "t_01", Level: "L2", Reason: "goal is multi-file"},
		"scope.transition":     &ScopeTransitionEvent{Task: "t_01", FromLevel: "L2", ToLevel: "L3", Action: "expand", Reason: "tests failed"},
		"workflow.selected":    &WorkflowSelectedEvent{Task: "t_01", Goal: "fix the null deref", Workflow: "bugfix"},
		// Dropped is populated on purpose: it is the only map in the catalog, and
		// a nil one would round-trip trivially while the populated case — the one
		// that actually ships when a prompt is trimmed — went untested.
		"prompt.budget": &TokenBudgetEvent{
			Task: "t_01", Window: 8192, Used: 7104,
			Dropped: map[string]int{"retrieved files": 3, "conversation turns": 11},
		},
		"user.preference":  &UserPreferenceEvent{Task: "t_01", Key: "commits", Value: "conventional"},
		"user.command":     &UserCommandEvent{Command: "provider", Args: "groq"},
		"command.response": &CommandResponseEvent{Text: "provider: groq (llama-3.3-70b-versatile)"},
		"error":            &ErrorEvent{Task: t, Layer: "cognitive", Code: "timeout", Msg: "slow", Retry: true},
	}
}

// topicsDeclaredInEventsGo parses events.go and returns the topic string every
// Type() method returns. This is the source of truth the catalog must cover:
// deriving it from the code makes it impossible for a new event to be defined
// and then forgotten in the registry.
func topicsDeclaredInEventsGo(t *testing.T) map[Topic]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse events.go: %v", err)
	}
	out := map[Topic]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Type" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			lit, ok := ret.Results[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			topic, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", lit.Value, err)
			}
			out[Topic(topic)] = types.ExprString(fn.Recv.List[0].Type)
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no Type() methods out of events.go; the derivation is broken, not the catalog")
	}
	return out
}

// TestCatalogCoversEveryDeclaredEvent is the anti-drift check for the catalog.
// The registry used to be a hand-maintained Register() list, and it had drifted:
// five events (command.response, cost.incurred, plan.done, user.command,
// user.preference) were declared in events.go but never registered, so Replay
// could not reconstruct them and any durability log containing one was
// unreadable. Deriving the expected set from the source keeps that from
// happening again.
func TestCatalogCoversEveryDeclaredEvent(t *testing.T) {
	for topic, typeName := range topicsDeclaredInEventsGo(t) {
		if _, ok := factoryFor(topic); !ok {
			t.Errorf("%s declares topic %q but nothing registers it; a log containing it cannot be replayed", typeName, topic)
		}
	}
}

// TestCatalogHasAll16TopicGroups verifies the registry covers every topic group
// from File 05 §5.4.9. A missing group is a wire-contract regression. The set is
// read from the catalog table rather than hand-copied, so it cannot drift.
func TestCatalogHasAll16TopicGroups(t *testing.T) {
	if len(catalog) == 0 {
		t.Fatal("catalog table is empty")
	}
	for topic := range catalog {
		if _, ok := factoryFor(topic); !ok {
			t.Errorf("topic %q is in the catalog table but init did not register it", topic)
		}
	}
}

// TestRoundTripAllCatalogEvents is the L3-006 headline: every catalog event
// survives Envelope JSON round-trip with Type, CausalID, and payload intact.
func TestRoundTripAllCatalogEvents(t *testing.T) {
	samples := sampleEvents()
	// Coverage is derived from the catalog, not a magic count: adding a topic
	// without adding a sample must fail here rather than quietly go untested.
	for topic := range catalog {
		if _, ok := samples[topic]; !ok {
			t.Errorf("catalog topic %q has no sample event; its round-trip is untested", topic)
		}
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
