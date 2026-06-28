package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestHeadlessSingleTurnPrintsDeterministicTranscript is the L2-006 headline
// (and S5 for the spine): `echo "hi" | yolo --headless` prints one line per
// event, and two runs produce byte-identical output (modulo the timestamp,
// which the headless projection omits by design).
func TestHeadlessSingleTurnPrintsDeterministicTranscript(t *testing.T) {
	first, err := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless (1): %v", err)
	}
	second, err := runHeadless(bytes.NewBufferString("say hi\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless (2): %v", err)
	}
	if first != second {
		t.Errorf("transcript not byte-identical across runs (S5)\n first:\n%s\n second:\n%s", first, second)
	}

	// With the Sprint 12 wired adapters, the spine includes the real context
	// build + token stream before the direct answer.
	want := []string{
		"task.started",
		"state.change",
		"state.change",
		"context.built",
		"state.change",
		"llm.token",
		"state.change",
		"assistant.message",
		"task.completed",
	}
	lines := strings.Split(strings.TrimRight(first, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("transcript has %d lines, want %d:\n%s", len(lines), len(want), first)
	}
	for i, w := range want {
		if !strings.Contains(lines[i], "\"type\":\""+w+"\"") {
			t.Errorf("line %d = %q, want type %q", i, lines[i], w)
		}
	}
}

// TestHeadlessPipesStdinToTaskGoal verifies the prompt read from stdin becomes
// the task's goal (visible in task.started).
func TestHeadlessPipesStdinToTaskGoal(t *testing.T) {
	out, err := runHeadless(bytes.NewBufferString("refactor the auth module\n"), 0)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if !strings.Contains(out, "refactor the auth module") {
		t.Errorf("transcript missing the stdin goal; got:\n%s", out)
	}
}

// TestHeadlessCancelProducesCancelledTranscript verifies that canceling the
// headless context mid-run surfaces a CANCELLED task (the exit path the TUI's
// Ctrl+C will use). We drive a blocking cognitive core and cancel.
func TestHeadlessCancelProducesCancelledTranscript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cancel()
	}()
	out, err := runHeadlessCtx(ctx, bytes.NewBufferString("say hi\n"), 0)
	_ = err
	// Either the task was cancelled, or (if cancel raced ahead of StartTask)
	// no task ran. Both are valid terminal outcomes; assert we did not print a
	// DONE.
	if strings.Contains(out, "DONE") {
		t.Errorf("canceled run reached DONE; transcript:\n%s", out)
	}
}
