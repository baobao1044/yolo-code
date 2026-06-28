// Tests for L10-005 — persistence + cross-session recall (File 11 §11.3.3 +
// §11.5.2). Persistent stores load-on-Open so a fact stored in session A is
// recalled in session B (the L10-005 exit bar). Preference memory is per-user,
// cross-project (shared file); conversation/exec-history are per-session/task
// (resume). The store shares one root dir across sessions so the next Open
// re-reads what the last Open wrote.

package memory

import (
	"context"
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
