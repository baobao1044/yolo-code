// Tests for the cognitive→runtime adapter (Sprint 12 INT-004).

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// TestCognitiveAdapterThinkBridgesToolCalls verifies that a cognitive turn
// with tool calls is translated into the runtime's CognitiveTurn shape.
func TestCognitiveAdapterThinkBridgesToolCalls(t *testing.T) {
	provider := cognitive.NewMockProvider([]cognitive.Chunk{
		{Delta: "I'll use a tool.\n"},
		{ToolCall: &cognitive.ToolCall{Tool: "bash", Args: []byte(`{"command":"go version"}`), Reason: "check go"}},
	}, 128_000)

	adapter := &cognitiveAdapter{core: cognitive.New(provider, nil)}
	turn, err := adapter.Think(context.Background(), []prompt.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Think = %v, want nil", err)
	}
	if turn.Final {
		t.Fatal("turn.Final = true, want false")
	}
	if len(turn.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Tool != "bash" {
		t.Fatalf("Tool = %q, want bash", turn.ToolCalls[0].Tool)
	}
	if turn.ToolCalls[0].Reason != "check go" {
		t.Fatalf("Reason = %q, want check go", turn.ToolCalls[0].Reason)
	}
}

// TestCognitiveAdapterReflectBridgesPatch verifies a reflection decision
// proposing a patch is mapped to a runtime.ReflectionDecision containing the
// patch body.
func TestCognitiveAdapterReflectBridgesPatch(t *testing.T) {
	provider := cognitive.NewMockProvider([]cognitive.Chunk{
		{Delta: "DECISION: patch\nadd x"},
	}, 128_000)

	adapter := &cognitiveAdapter{core: cognitive.New(provider, nil)}
	task := &session.Task{ID: "t-1", RetryMax: 3}
	dec := adapter.Reflect(context.Background(), task, runtime.Verdict{Pass: false, Reason: "broken"}, runtime.Observation{Stdout: "fail"})
	if dec.Replan || dec.Abort {
		t.Fatalf("unexpected decision: replan=%v abort=%v", dec.Replan, dec.Abort)
	}
	if !strings.Contains(string(dec.Patch.Body), "DECISION: patch") {
		t.Fatalf("patch body = %q", dec.Patch.Body)
	}
}

// TestHeadlessWiresRealCognitiveCore verifies the full headless path through
// real Context Engine → Prompt Compiler → cognitive.Core adapters. The stub
// provider returns a direct answer, so the task reaches DONE and the
// transcript contains the context.built and assistant.message events.
func TestHeadlessWiresRealCognitiveCore(t *testing.T) {
	repo := corpusPath(t)
	bus := event.New()
	eng := econtext.New(econtext.Deps{
		Bus:  bus,
		Repo: repo,
		Open: []string{"auth/login.go"},
	})
	comp := prompt.New(nil, bus)
	cog := newRealCognitiveCore(cognitive.NewStubProvider(128_000), bus)

	out, err := runHeadlessDeps(context.Background(), bytes.NewBufferString("explain @auth/login.go\n"), 0,
		&headlessDeps{
			context: contextAdapter{eng: eng},
			prompt:  promptAdapter{comp: comp},
			cog:     cog,
			bus:     bus,
		})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}
	if !strings.Contains(out, "\"type\":\"context.built\"") {
		t.Fatalf("transcript missing context.built\n%s", out)
	}
	if !strings.Contains(out, "\"type\":\"assistant.message\"") {
		t.Fatalf("transcript missing assistant.message\n%s", out)
	}
}

// TestCorrectivePatchReachesThePatchEngine walks a reflection that decided to
// patch all the way to the last function before the bytes would hit disk, and
// asserts it gets there.
//
// It does not today, and the reason is that no layer on the route ever
// establishes which file the patch is for. reflection.go builds
// PatchOp{Body: []byte(note)} — the whole prose note, no path, because
// cognitive.PatchOp had no Path field to put one in. cognitive_adapter.go
// copies only Body across. patchToolCall then has nothing to encode,
// patchOpFromCall nothing to recover, and opTargetAndBody — which accepts a
// path from the JSON args or from PatchOp.Path and has neither — returns
// "missing target path". The runtime turns that into c.toError and the task
// ends.
//
// So reflection's patch arm has never been able to apply anything in the
// composed binary. Nothing caught it because every layer looks correct in
// isolation: the parser produces a decision, the adapter bridges it, the
// runtime routes it, the engine refuses it. The refusal is the only honest
// one in the chain, and it is at the far end.
func TestCorrectivePatchReachesThePatchEngine(t *testing.T) {
	// A reflection note shaped the way the prompt asks for one.
	provider := cognitive.NewMockProvider([]cognitive.Chunk{
		{Delta: "The guard rejects a valid input.\n" +
			"PATH: internal/exec/read.go\n" +
			"DECISION: patch\n" +
			"```\n<<<<<<< SEARCH\nif n < 1 {\n=======\nif n < 0 {\n>>>>>>> REPLACE\n```\n"},
	}, 128_000)

	adapter := &cognitiveAdapter{core: cognitive.New(provider, nil)}
	task := &session.Task{ID: "t-1", RetryMax: 3}
	dec := adapter.Reflect(context.Background(), task,
		runtime.Verdict{Pass: false, Reason: "test failed"},
		runtime.Observation{Stdout: "FAIL", Files: []string{"internal/exec/read.go"}})

	if dec.Replan || dec.Abort {
		t.Fatalf("unexpected decision: replan=%v abort=%v", dec.Replan, dec.Abort)
	}
	if dec.Patch.Path != "internal/exec/read.go" {
		t.Errorf("decision carries path %q, want the file the note named", dec.Patch.Path)
	}

	// adapter.Reflect already returns the runtime's shape, and the runtime's
	// own tests cover the tool-call round trip in between (patchToolCall
	// preserves Path, patchOpFromCall recovers it). What is left untested by
	// either side is the handover itself: what the composition root's patch
	// adapter can do with what the cognitive adapter handed it.
	path, body, err := opTargetAndBody(dec.Patch)
	if err != nil {
		t.Fatalf("opTargetAndBody: %v\n"+
			"a reflection that decided to patch cannot reach the patch engine", err)
	}
	if path != "internal/exec/read.go" {
		t.Errorf("target path = %q, want internal/exec/read.go", path)
	}
	if strings.Contains(body, "DECISION: patch") {
		t.Errorf("patch body carries the reflection prose, not the patch:\n%s\n"+
			"the validator is handed a note where it expects SEARCH/REPLACE blocks", body)
	}
	if !strings.Contains(body, "<<<<<<< SEARCH") {
		t.Errorf("patch body lost the SEARCH/REPLACE blocks:\n%s", body)
	}
}
