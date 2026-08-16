// Tests for L10-005 — persistence + cross-session recall (File 11 §11.3.3 +
// §11.5.2). Persistent stores load-on-Open so a fact stored in session A is
// recalled in session B (the L10-005 exit bar). Preference memory is per-user,
// cross-project (shared file); conversation/exec-history are per-session/task
// (resume). The store shares one root dir across sessions so the next Open
// re-reads what the last Open wrote.

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCrossSessionPreferenceRecall (L10-005 exit bar): store a preference in
// session A, open a fresh Store for session B over the same root, and the
// preference is recalled. A preference set in one session survives into the
// next — the agent remembers across sessions.
func TestCrossSessionPreferenceRecall(t *testing.T) {
	dir := t.TempDir()

	// Session A: store the preference + close.
	a, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	if err := a.Preferences().Set(context.Background(), "test-style", "table-driven"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_ = a.Close()

	// Session B: a fresh Store over the same root. Open eager-loads the
	// preference file so Preferences() recalls the value set in A.
	b, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer b.Close()

	got, err := b.Preferences().Get(context.Background(), "test-style")
	if err != nil {
		t.Fatalf("Get across sessions: %v (preference didn't persist + recall)", err)
	}
	if got != "table-driven" {
		t.Errorf("cross-session recall = %q, want \"table-driven\"", got)
	}
	// All() returns it too.
	all, _ := b.Preferences().All(context.Background())
	if all["test-style"] != "table-driven" {
		t.Errorf("All[test-style] = %q, want \"table-driven\"", all["test-style"])
	}
}

// TestCrossSessionConversationResume: a conversation persisted in session A is
// re-loaded by a fresh ConversationStore in session B (§11.3.3 resume). The
// Context Engine resumes by re-reading the messages.
func TestCrossSessionConversationResume(t *testing.T) {
	dir := t.TempDir()

	a, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	a.Conversation().AppendAssistant(context.Background(), "s_1", Message{Role: RoleAssistant, Text: "first reply"})
	if err := a.Conversation().Persist(context.Background(), "s_1"); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	_ = a.Close()

	// Session B over the same root: Open eager-loads conversations so a Resume
	// re-reads the messages (the test asserts Messages is populated).
	b, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer b.Close()
	if err := b.Conversation().Load(context.Background(), "s_1"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Conversation().Messages("s_1"); len(got) != 1 || got[0].Text != "first reply" {
		t.Errorf("resumed Messages = %+v, want one \"first reply\"", got)
	}
}

// TestExecHistoryPersistsAcrossSessions: the per-task exec history persists to
// JSON and a fresh store re-loads it (the audit trail survives a restart).
func TestExecHistoryPersistsAcrossSessions(t *testing.T) {
	dir := t.TempDir()

	a, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	a.ExecHistory().Append(context.Background(), "t_1", ExecEntry{Kind: "tool", Summary: "read"})
	if err := a.ExecHistory().Persist(context.Background(), "t_1"); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	_ = a.Close()

	b, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer b.Close()
	if err := b.ExecHistory().Load(context.Background(), "t_1"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.ExecHistory().Entries("t_1"); len(got) != 1 || got[0].Summary != "read" {
		t.Errorf("resumed Entries = %+v, want one \"read\"", got)
	}
}

// TestOpenWithNoPriorDataStartsEmpty: a fresh root (no prior data) Open-s and
// the stores are empty — no error from loading absent files.
func TestOpenWithNoPriorDataStartsEmpty(t *testing.T) {
	s, err := Open(Deps{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if got, err := s.Preferences().All(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("fresh prefs = %v (err %v), want empty", got, err)
	}
}

// TestCloseFlushesUnfinishedWork (§11.3.3): the listener only persists on
// task.completed, so a session that ends any other way — Ctrl-C, a crash, a
// task still in flight — used to write nothing at all. Close must flush the
// durable sub-stores so what the agent learned survives into the next launch.
func TestCloseFlushesUnfinishedWork(t *testing.T) {
	dir := t.TempDir()

	a, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	ctx := context.Background()
	a.Conversation().AppendAssistant(ctx, "s_1", Message{Role: RoleAssistant, Text: "unfinished reply"})
	a.ExecHistory().Append(ctx, "s_1", ExecEntry{Kind: "tool", Summary: "grep"})
	a.Insights().Record(ctx, "prefer table-driven tests", "verify.pass")
	// No task.completed — the session just ends.
	if err := a.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	b, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer b.Close()
	if got := b.Insights().All(); len(got) != 1 || got[0].Text != "prefer table-driven tests" {
		t.Errorf("insights after restart = %+v, want the one recorded before Close", got)
	}
	if err := b.Conversation().Load(ctx, "s_1"); err != nil {
		t.Fatalf("Conversation Load: %v", err)
	}
	if got := b.Conversation().Messages("s_1"); len(got) != 1 || got[0].Text != "unfinished reply" {
		t.Errorf("conversation after restart = %+v, want the unflushed reply", got)
	}
	if err := b.ExecHistory().Load(ctx, "s_1"); err != nil {
		t.Fatalf("ExecHistory Load: %v", err)
	}
	if got := b.ExecHistory().Entries("s_1"); len(got) != 1 || got[0].Summary != "grep" {
		t.Errorf("exec history after restart = %+v, want the unflushed entry", got)
	}
}

// TestFlushIsCallableMidSession: Flush is the explicit checkpoint the
// composition root can call without closing the store (a long task shouldn't
// have to end for its memory to become durable).
func TestFlushIsCallableMidSession(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	s.Insights().Record(ctx, "the build is quadratic", "verify.fail")
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// The file is on disk NOW — a second store reading the same root sees it
	// while the first is still open.
	other, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open other: %v", err)
	}
	defer other.Close()
	if got := other.Insights().All(); len(got) != 1 {
		t.Errorf("insights after Flush = %+v, want 1 (Flush didn't reach disk)", got)
	}
}

// TestWriteJSONIsAtomic: a save must never expose a half-written file. The old
// writeJSON went through os.WriteFile, which truncates the target and then
// streams the bytes, so a crash — or, as here, a concurrent reader — could see
// a truncated file where a valid one used to be. A temp-file + rename leaves
// the reader with the old contents or the new ones, never a mix.
func TestWriteJSONIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")

	// A payload large enough that a single non-atomic write is observably
	// non-instant (a few MiB of JSON).
	payload := make([]string, 20000)
	for i := range payload {
		payload[i] = strings.Repeat("x", 200)
	}
	if err := writeJSON(path, payload); err != nil {
		t.Fatalf("seed writeJSON: %v", err)
	}

	const rounds = 20
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			if err := writeJSON(path, payload); err != nil {
				t.Errorf("writeJSON: %v", err)
				return
			}
		}
	}()

	torn := 0
	for {
		select {
		case <-done:
			if torn > 0 {
				t.Errorf("%d concurrent reads saw a torn file — the save is not atomic", torn)
			}
			return
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue // a missing file is fine; a torn one is not
		}
		var out []string
		if json.Unmarshal(data, &out) != nil || len(out) != len(payload) {
			torn++
		}
	}
}

// TestOpenWithCorruptStoreFileIsNonFatal: a truncated or hand-mangled store
// file must not kill the agent and must not be silently overwritten. Open
// starts empty, reports the problem through Warnings, and moves the bytes
// aside as <name>.corrupt so they can still be recovered.
func TestOpenWithCorruptStoreFileIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	// A truncated write: valid JSON prefix, no closing bracket.
	const truncated = `[{"text":"half a lesso`
	if err := os.WriteFile(filepath.Join(dir, "knowledge.json"), []byte(truncated), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preference.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open with corrupt files returned an error (want non-fatal): %v", err)
	}
	defer s.Close()

	warns := s.Warnings()
	if len(warns) < 2 {
		t.Errorf("Warnings() = %v, want one per corrupt file", warns)
	}
	for _, w := range warns {
		var ce *CorruptError
		if !errors.As(w, &ce) {
			t.Errorf("warning %v is not a *CorruptError", w)
		}
	}
	// The stores start empty rather than half-loaded.
	if got := s.Insights().All(); len(got) != 0 {
		t.Errorf("insights after corrupt load = %+v, want empty", got)
	}
	if got, _ := s.Preferences().All(context.Background()); len(got) != 0 {
		t.Errorf("prefs after corrupt load = %v, want empty", got)
	}
	// No silent wipe: the original bytes are still on disk, moved aside.
	kept, err := os.ReadFile(filepath.Join(dir, "knowledge.json.corrupt"))
	if err != nil {
		t.Fatalf("corrupt knowledge.json was not preserved: %v", err)
	}
	if string(kept) != truncated {
		t.Errorf("quarantined bytes = %q, want the original %q", kept, truncated)
	}
}

// TestOpenThenFlushOnAFreshRootWritesNothingSurprising: first run — no files
// exist, Open must not error, and Flush must create only the stores that have
// content (a missing file on first launch is "nothing remembered yet").
func TestFirstRunWithNoFilesLoadsCleanly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Deps{Root: dir})
	if err != nil {
		t.Fatalf("Open on a fresh root: %v", err)
	}
	defer s.Close()
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("Warnings on a fresh root = %v, want none (a missing file is not corruption)", w)
	}
	if got := s.Insights().All(); len(got) != 0 {
		t.Errorf("insights on a fresh root = %+v, want empty", got)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Errorf("Flush on a fresh root: %v", err)
	}
}
