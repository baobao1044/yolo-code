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
