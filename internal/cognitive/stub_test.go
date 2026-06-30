package cognitive

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/baobao1044/yolo-code/internal/prompt"
)

// TestStubDeterministicSameInputSameTokenSequence is the L6-007 / S5 exit
// criterion: the stub produces the same chunk sequence for the same input,
// every run. Two streams over identical messages must yield byte-identical
// token deltas, thinking, and tool calls. Run across every response path
// (list/read/edit/default) so nondeterminism in any branch is caught.
func TestStubDeterministicSameInputSameTokenSequence(t *testing.T) {
	stub := NewStubProvider(0)
	inputs := []string{
		"list the files in this repo",
		"read @auth/login.go",
		"fix the bug in main.go",
		"explain what this does",
	}
	for _, in := range inputs {
		msgs := []prompt.Message{{Role: "user", Content: in}}
		first, err := drainStream(stub, msgs)
		if err != nil {
			t.Fatalf("first stream (%q): %v", in, err)
		}
		second, err := drainStream(stub, msgs)
		if err != nil {
			t.Fatalf("second stream (%q): %v", in, err)
		}
		if len(first) != len(second) {
			t.Fatalf("%q: nondeterministic chunk count: first %d, second %d", in, len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				t.Fatalf("%q: nondeterministic at chunk %d:\n first:  %+v\n second: %+v", in, i, first[i], second[i])
			}
		}
	}
}

// TestStubListPromptEmitsListFilesTool pins the input→response rule: a "list
// files" prompt yields a list_files tool block, not a direct answer.
func TestStubListPromptEmitsListFilesTool(t *testing.T) {
	chunks := mustRespond(t, "list the files in this repo")
	turn := turnFrom(chunks)
	if turn.Final {
		t.Fatal("turn.Final = true, want false (list prompt → list_files tool call)")
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Tool != "list_files" {
		t.Errorf("tool calls = %+v, want one list_files", turn.ToolCalls)
	}
}

// TestStubReadPromptEmitsReadFileWithTarget pins the input→response rule: a
// "read" prompt with an @file yields a read_file tool call targeting that file.
func TestStubReadPromptEmitsReadFileWithTarget(t *testing.T) {
	chunks := mustRespond(t, "read @auth/login.go")
	turn := turnFrom(chunks)
	if turn.Final {
		t.Fatal("turn.Final = true, want false (read prompt → read_file tool call)")
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Tool != "read_file" {
		t.Fatalf("tool calls = %+v, want one read_file", turn.ToolCalls)
	}
	var args struct {
		File string `json:"file"`
	}
	if len(turn.ToolCalls[0].Args) > 0 {
		_ = json.Unmarshal(turn.ToolCalls[0].Args, &args)
	}
	if args.File != "auth/login.go" {
		t.Errorf("read_file target = %q, want auth/login.go (extracted from @ref)", args.File)
	}
}

// TestStubEditPromptEmitsEditFile pins the input→response rule for fix/edit/
// refactor keywords.
func TestStubEditPromptEmitsEditFile(t *testing.T) {
	for _, p := range []string{"fix the bug", "edit the file", "refactor main.go"} {
		chunks := mustRespond(t, p)
		turn := turnFrom(chunks)
		if turn.Final {
			t.Errorf("%q: turn.Final = true, want false (edit keyword → edit_file)", p)
		}
		if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Tool != "edit_file" {
			t.Errorf("%q: tool calls = %+v, want one edit_file", p, turn.ToolCalls)
		}
	}
}

// TestStubDefaultPromptDirectAnswer pins the input→response rule: a prompt
// with no tool keyword yields a direct answer (Final, no tool calls).
func TestStubDefaultPromptDirectAnswer(t *testing.T) {
	chunks := mustRespond(t, "explain what this does")
	turn := turnFrom(chunks)
	if !turn.Final {
		t.Error("turn.Final = false, want true (no tool keyword → direct answer)")
	}
	if len(turn.ToolCalls) != 0 {
		t.Errorf("tool calls = %+v, want none", turn.ToolCalls)
	}
	if turn.Text == "" {
		t.Error("turn.Text empty, want the direct answer")
	}
}

// TestStubWindowPinsContextBudget pins Window() returns the configured value,
// which the Prompt Compiler's budget reads (File 06 §6.6.1).
func TestStubWindowPinsContextBudget(t *testing.T) {
	if w := NewStubProvider(8000).Window(); w != 8000 {
		t.Errorf("Window = %d, want 8000", w)
	}
	if w := NewStubProvider(0).Window(); w != 128_000 {
		t.Errorf("Window(0) = %d, want default 128000", w)
	}
}

// TestStubStreamEndToEndThroughThink pins the full S5 path: the stub streams
// through the real Core's Think loop, producing a parsed Turn the runtime
// would drive. Same input twice → same Turn.
func TestStubStreamEndToEndThroughThink(t *testing.T) {
	core := New(NewStubProvider(0), nil)
	msgs := []prompt.Message{{Role: "user", Content: "read @x.go"}}

	t1, err := core.Think(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Think (1): %v", err)
	}
	t2, err := core.Think(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Think (2): %v", err)
	}
	if t1.Final != t2.Final || t1.Text != t2.Text || len(t1.ToolCalls) != len(t2.ToolCalls) {
		t.Errorf("nondeterministic Think turn:\n t1=%+v\n t2=%+v", t1, t2)
	}
	if t1.Final {
		t.Error("turn.Final = true, want false (read prompt → tool call)")
	}
}

// drainStream runs a StubProvider.Stream to completion and returns the chunk
// sequence, so two calls can be compared for determinism.
func drainStream(s *StubProvider, msgs []prompt.Message) ([]Chunk, error) {
	ch, err := s.Stream(context.Background(), Request{Messages: msgs})
	if err != nil {
		return nil, err
	}
	var out []Chunk
	for chunk := range ch {
		out = append(out, chunk)
	}
	return out, nil
}

// mustRespond builds a stub and drains its response for the last-user-message
// derived from msg.
func mustRespond(t *testing.T, lastUserContent string) []Chunk {
	t.Helper()
	stub := NewStubProvider(0)
	ch, err := stub.Stream(context.Background(), Request{
		Messages: []prompt.Message{{Role: "user", Content: lastUserContent}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out []Chunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// turnFrom accumulates a chunk sequence's deltas and parses them, mirroring
// what Core.Think does.
func turnFrom(chunks []Chunk) Turn {
	var buf []byte
	for _, c := range chunks {
		buf = append(buf, []byte(c.Delta)...)
	}
	return parseTurn(string(buf))
}
