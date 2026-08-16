package cognitive

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// reflTask builds a session.Task for reflection tests with a configurable
// RetryMax (the cap reflection enforces, sourced from the Cost Controller).
func reflTask(retry, max int) *session.Task {
	return &session.Task{ID: "t_r", Goal: "fix the bug", Retry: retry, RetryMax: max}
}

// TestReflectEmitsReflectionNoteEvent is the L6-003 exit criterion: reflection
// publishes a reflection.note event carrying the root-cause analysis. The mock
// provider streams a scripted note; Reflect must publish it verbatim.
func TestReflectEmitsReflectionNoteEvent(t *testing.T) {
	note := "The patch used the wrong signature. DECISION: replan"
	chunks := []Chunk{{Delta: note}}
	core, bus := newTestCore(t, chunks)
	noteCh := bus.Subscribe("reflection.note")

	dec := core.Reflect(ctxWithTask("t_r"), reflTask(0, 3),
		Verdict{Pass: false, Reason: "build failed"}, Observation{Text: "error: undefined Login"})

	env := drain(t, noteCh)
	re, ok := env.Evt.(*event.ReflectionEvent)
	if !ok {
		t.Fatalf("event type = %T, want *ReflectionEvent", env.Evt)
	}
	if re.Note != note {
		t.Errorf("reflection.note = %q, want %q (verbatim root-cause)", re.Note, note)
	}
	if re.Task != event.TaskID("t_r") {
		t.Errorf("reflection.note.Task = %q, want %q", re.Task, "t_r")
	}
	if dec.Note != note {
		t.Errorf("decision.Note = %q, want %q", dec.Note, note)
	}
	// replan was chosen → the note feeds the next PLAN iteration.
	if !dec.Replan {
		t.Error("dec.Replan = false, want true (the chosen decision)")
	}
}

// TestReflectMutatesNothingButRetry pins the cardinal rule (§7.3.1): reflection
// calls no tools and mutates only the task's retry counter. The task's goal,
// status, and history are unchanged; only Retry advances by one.
func TestReflectMutatesNothingButRetry(t *testing.T) {
	chunks := []Chunk{{Delta: "root cause here. DECISION: replan"}}
	core, _ := newTestCore(t, chunks)
	task := reflTask(1, 5)
	before := *task // snapshot

	_ = core.Reflect(stdctx.Background(), task, Verdict{Pass: false}, Observation{Text: "x"})

	if task.Retry != before.Retry+1 {
		t.Errorf("task.Retry = %d, want %d (only the retry counter advances)", task.Retry, before.Retry+1)
	}
	if task.Goal != before.Goal {
		t.Errorf("task.Goal changed: %q → %q", before.Goal, task.Goal)
	}
	if task.Status != before.Status {
		t.Errorf("task.Status changed: %q → %q", before.Status, task.Status)
	}
	if len(task.History) != len(before.History) {
		t.Errorf("task.History changed: %d → %d", len(before.History), len(task.History))
	}
}

// TestReflectAbortsAtRetryCap pins the cost-controlled abort (§7.3.2/§7.6.4):
// when Retry >= RetryMax, reflection aborts without calling the provider and
// emits no note. The decision's Abort is true and the note explains the cap.
func TestReflectAbortsAtRetryCap(t *testing.T) {
	// A provider that, if called, would publish a note — but it must NOT be
	// called because the cap is already hit.
	called := false
	prov := &trackingProvider{onStream: func() { called = true }}
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	core := New(prov, bus)
	noteCh := bus.Subscribe("reflection.note")

	dec := core.Reflect(stdctx.Background(), reflTask(3, 3), Verdict{Pass: false}, Observation{})

	if !dec.Abort {
		t.Error("dec.Abort = false, want true (retry cap reached → cost-controlled abort)")
	}
	if !strings.Contains(dec.Note, "retry cap") {
		t.Errorf("dec.Note = %q, want it to mention the retry cap", dec.Note)
	}
	if called {
		t.Error("reflection called the provider despite the retry cap; it must abort without streaming")
	}
	// No note published on abort-at-cap.
	select {
	case env := <-noteCh:
		t.Errorf("reflection published a note on abort-at-cap: %+v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestReflectParsesReplanDecision pins the decision parser: a note ending
// "DECISION: replan" selects replan (the note feeds the next PLAN iteration).
func TestReflectParsesReplanDecision(t *testing.T) {
	chunks := []Chunk{{Delta: "wrong function signature. DECISION: replan"}}
	core, _ := newTestCore(t, chunks)
	dec := core.Reflect(stdctx.Background(), reflTask(0, 3), Verdict{Pass: false}, Observation{})
	if !dec.Replan || dec.Abort {
		t.Errorf("dec = %+v, want Replan=true Abort=false", dec)
	}
}

// TestReflectParsesAbortDecision pins the decision parser: "DECISION: abort"
// selects abort (surface to the user, stop).
func TestReflectParsesAbortDecision(t *testing.T) {
	chunks := []Chunk{{Delta: "unrecoverable. DECISION: abort"}}
	core, _ := newTestCore(t, chunks)
	dec := core.Reflect(stdctx.Background(), reflTask(0, 3), Verdict{Pass: false}, Observation{})
	if !dec.Abort || dec.Replan {
		t.Errorf("dec = %+v, want Abort=true Replan=false", dec)
	}
}

// TestReflectParsesPatchDecision pins the decision parser: "DECISION: patch"
// selects a corrective patch proposal (carried as the PatchOp body). The Patch
// Engine applies it later; reflection only proposes.
func TestReflectParsesPatchDecision(t *testing.T) {
	note := "missing error check. DECISION: patch"
	chunks := []Chunk{{Delta: note}}
	core, _ := newTestCore(t, chunks)
	dec := core.Reflect(stdctx.Background(), reflTask(0, 3), Verdict{Pass: false}, Observation{})
	if dec.Abort || dec.Replan {
		t.Errorf("dec = %+v, want Abort=false Replan=false (patch selected)", dec)
	}
	if len(dec.Patch.Body) == 0 {
		t.Error("dec.Patch.Body empty, want the note as the proposed patch body")
	}
}

// TestReflectDefaultsToReplanWhenNoMarker pins that a note with no explicit
// DECISION marker defaults to replan — the root-cause analysis feeds the next
// PLAN iteration (§7.3.3), the common case.
func TestReflectDefaultsToReplanWhenNoMarker(t *testing.T) {
	chunks := []Chunk{{Delta: "the build failed because the signature was wrong"}}
	core, _ := newTestCore(t, chunks)
	dec := core.Reflect(stdctx.Background(), reflTask(0, 3), Verdict{Pass: false}, Observation{})
	if !dec.Replan {
		t.Error("dec.Replan = false, want true (no marker → default replan)")
	}
	if dec.Abort {
		t.Error("dec.Abort = true, want false")
	}
}

// TestReflectPropagatesStreamError pins that a stream error during reflection
// aborts with the error in the note (no partial decision).
func TestReflectPropagatesStreamError(t *testing.T) {
	chunks := []Chunk{{Err: errStream("reflect: connection lost")}}
	core, _ := newTestCore(t, chunks)
	dec := core.Reflect(stdctx.Background(), reflTask(0, 3), Verdict{Pass: false}, Observation{})
	if !dec.Abort {
		t.Error("dec.Abort = false, want true (stream error → abort)")
	}
	if !strings.Contains(dec.Note, "connection lost") {
		t.Errorf("dec.Note = %q, want it to carry the stream error", dec.Note)
	}
}

// trackingProvider is a mock that records whether Stream was called.
type trackingProvider struct {
	onStream func()
}

func (p *trackingProvider) Stream(ctx stdctx.Context, _ Request) (<-chan Chunk, error) {
	if p.onStream != nil {
		p.onStream()
	}
	out := make(chan Chunk, 1)
	go func() { defer close(out); _ = ctx }()
	return out, nil
}

func (p *trackingProvider) Window() int { return 128_000 }

// ensure session import is used (reflTask references session.Task via the
// helper above; keep the alias here to satisfy go vet's import order).
var _ = session.Task{}

// TestParseReflectionToleratesMarkdownAndPunctuation pins the minor defect: a
// real model writes `DECISION: **abort**` or "DECISION: abort." — the exact
// match rejected both and fell through to the default replan, so a
// cost-controlled abort silently became another retry.
func TestParseReflectionToleratesMarkdownAndPunctuation(t *testing.T) {
	cases := []struct {
		note  string
		abort bool
		patch bool
	}{
		{note: "root cause\nDECISION: **abort**", abort: true},
		{note: "root cause\nDECISION: abort.", abort: true},
		{note: "root cause\nDECISION: _Abort_", abort: true},
		{note: "root cause\n**DECISION: abort**", abort: true},
		{note: "root cause\nDECISION: `patch`", patch: true},
		{note: "root cause\nDECISION: **patch** — retry the edit", patch: true},
	}
	for _, tc := range cases {
		dec := parseReflection(tc.note)
		if dec.Abort != tc.abort {
			t.Errorf("parseReflection(%q).Abort = %v, want %v", tc.note, dec.Abort, tc.abort)
		}
		if got := len(dec.Patch.Body) > 0; got != tc.patch {
			t.Errorf("parseReflection(%q) patch proposed = %v, want %v", tc.note, got, tc.patch)
		}
	}
}

// TestReflectionPatchCarriesPathAndBody pins the two things a patch decision
// has to produce for the Patch Engine to have anything to do: which file, and
// the blocks to apply. Before this the decision produced neither — Path did not
// exist as a field and Body was the whole reflection, prose and DECISION line
// included — so the composition root refused every corrective patch with
// "missing target path" and never reached the validator.
//
// The tolerant matching mirrors the decision marker's, and for the same reason:
// a real model writes `PATH: **x.go**` or "PATH: `x.go`", and an exact match
// rejecting those would put the path back at "".
func TestReflectionPatchCarriesPathAndBody(t *testing.T) {
	const blocks = "<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"
	cases := []struct {
		name string
		note string
		path string
		body string
	}{
		{
			name: "plain",
			note: "root cause\nPATH: internal/exec/read.go\nDECISION: patch\n```\n" + blocks + "\n```",
			path: "internal/exec/read.go",
			body: blocks,
		},
		{
			name: "emphasised path and a fence info string",
			note: "root cause\nPATH: **internal/exec/read.go**\nDECISION: patch\n```diff\n" + blocks + "\n```",
			path: "internal/exec/read.go",
			body: blocks,
		},
		{
			name: "trailing prose after the path",
			note: "PATH: read.go (the caller)\nDECISION: patch\n```\n" + blocks + "\n```",
			path: "read.go",
			body: blocks,
		},
		{
			// The bare-note shape the mock provider and the scripted
			// reflections emit. No path is a valid answer — the runtime reads
			// it as "the files the failing verdict covered" — and the body
			// falls back to the note rather than to nothing.
			name: "no path, no fence",
			note: "missing error check. DECISION: patch",
			path: "",
			body: "missing error check. DECISION: patch",
		},
		{
			name: "unterminated fence falls back rather than guessing",
			note: "PATH: a.go\nDECISION: patch\n```\n" + blocks,
			path: "a.go",
			body: "PATH: a.go\nDECISION: patch\n```\n" + blocks,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec := parseReflection(c.note)
			if dec.Replan || dec.Abort {
				t.Fatalf("decision was not patch: replan=%v abort=%v", dec.Replan, dec.Abort)
			}
			if dec.Patch.Path != c.path {
				t.Errorf("Path = %q, want %q", dec.Patch.Path, c.path)
			}
			if got := string(dec.Patch.Body); got != c.body {
				t.Errorf("Body = %q, want %q", got, c.body)
			}
		})
	}
}
