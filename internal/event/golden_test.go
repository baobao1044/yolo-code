//go:build golden

package event

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// This is the Sprint 0 golden-transcript harness (File 15 §15.15.3). Later
// sprints extend it to the full agent with a deterministic stub provider; here
// it proves the bus's replay determinism: a fixed publish sequence reproduces
// the same Seq/Type/CausalID/payload on replay.
//
// The `golden` build tag keeps these tests out of the default `go test` run
// and groups them as the S5 determinism gate (§15.15.1).

// deterministic is the replayable projection of an Envelope: everything
// except the non-deterministic timestamp.
type deterministic struct {
	Seq      uint64
	Type     Topic
	CausalID TaskID
	Payload  []byte
}

func project(envs []Envelope) []deterministic {
	out := make([]deterministic, len(envs))
	for i, env := range envs {
		payload, _ := json.Marshal(env.Evt)
		out[i] = deterministic{
			Seq:      env.Seq,
			Type:     env.Evt.Type(),
			CausalID: env.Evt.CausalID(),
			Payload:  payload,
		}
	}
	return out
}

// TestGoldenReplayDeterminism is the S5 gate for the bus (Sprint 0): recording
// a transcript and replaying it yields byte-identical deterministic
// projections. A drift is a determinism regression.
func TestGoldenReplayDeterminism(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golden.log")
	bus, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// A fixed sequence spanning several topics — the "transcript" under test.
	publishes := []Event{
		&TaskStartedEvent{Task: "t_01", Session: "s_1", Goal: "demo"},
		&StateChangeEvent{Task: "t_01", From: "INIT", To: "PLAN", Why: "go"},
		&ToolCallEvent{Task: "t_01", Tool: "list_files", Args: json.RawMessage(`{}`), Reason: "look"},
		&ToolResultEvent{Task: "t_01", Tool: "list_files", Obs: json.RawMessage(`{"files":["a.go"]}`)},
		&AssistantMessageEvent{Task: "t_01", Text: "done", Final: true},
		&TaskCompletedEvent{Task: "t_01"},
	}

	// Subscribe before publishing so collect sees every event in order.
	liveCh := bus.Subscribe(">")
	for _, e := range publishes {
		if err := bus.Publish(context.Background(), e); err != nil {
			t.Fatalf("publish %s: %v", e.Type(), err)
		}
	}

	// Drain the live subscription.
	live := make([]Envelope, 0, len(publishes))
	for len(live) < len(publishes) {
		select {
		case env := <-liveCh:
			live = append(live, env)
		case <-time.After(time.Second):
			t.Fatalf("timed out collecting live event %d/%d", len(live), len(publishes))
		}
	}

	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Replay from disk and project again. The two projections must be identical
	// — this is replay determinism (S5) at the bus layer.
	replayed, err := Replay(path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	liveProj := project(live)
	replayedProj := project(replayed)

	if len(liveProj) != len(replayedProj) {
		t.Fatalf("length drift: live=%d replayed=%d", len(liveProj), len(replayedProj))
	}
	for i := range liveProj {
		if !sameDeterministic(liveProj[i], replayedProj[i]) {
			t.Errorf("event %d drift:\n live  %+v\n replay %+v", i, liveProj[i], replayedProj[i])
		}
	}
}

// TestGoldenReplayIsStableAcrossRuns is the second S5 property: two identical
// runs (same publishes, fresh logs) produce identical replay projections. This
// catches nondeterminism in Seq assignment or payload encoding that a single
// replay would miss.
func TestGoldenReplayIsStableAcrossRuns(t *testing.T) {
	run := func() []deterministic {
		path := filepath.Join(t.TempDir(), "golden.log")
		bus, err := Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		for _, e := range []Event{
			&TaskStartedEvent{Task: "t_01", Session: "s_1", Goal: "demo"},
			&StateChangeEvent{Task: "t_01", From: "INIT", To: "PLAN", Why: "go"},
			&TaskCompletedEvent{Task: "t_01"},
		} {
			if err := bus.Publish(context.Background(), e); err != nil {
				t.Fatalf("publish %s: %v", e.Type(), err)
			}
		}
		_ = bus.Close()
		envs, err := Replay(path)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		return project(envs)
	}

	first := run()
	second := run()
	if len(first) != len(second) {
		t.Fatalf("run length drift: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if !sameDeterministic(first[i], second[i]) {
			t.Errorf("run drift at event %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// sameDeterministic compares two projections field-by-field (a struct holding
// a []byte slice is not directly comparable with ==).
func sameDeterministic(a, b deterministic) bool {
	return a.Seq == b.Seq &&
		a.Type == b.Type &&
		a.CausalID == b.CausalID &&
		string(a.Payload) == string(b.Payload)
}
