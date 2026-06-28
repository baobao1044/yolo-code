// Tests for L10-001 — the six memory sub-stores (File 11 §11.1). Each sub-
// store reads/writes its own type behind the Store aggregate. These tests
// exercise the surfaces the Context Engine (File 06) and Session Manager
// (File 03) consume — Working().History, Conversation().Append+Persist,
// ExecHistory().Append+Entries, Project().Invalidate, Preferences().Get/Set/All
// — against in-memory and JSON-file backings. The event-driven write rule
// (L10-002) and the vector store (L10-003) are separate tickets; L10-001 ships
// the store shapes + a direct (package-private) mutator per sub-store so the
// surfaces are exercisable now.

package memory

import (
	"context"
	"testing"
)

func TestWorkingMemoryForksAndHistory(t *testing.T) {
	// Working memory is in-process, per-turn RAM (§11.3.1). A turn forks the
	// conversation so a canceled turn can't corrupt the shared list; the fork
	// carries the new user text and the live history reads back through History().
	w := &WorkingMemory{}
	w.Append(Message{Role: "user", Text: "first"})
	if len(w.History()) != 1 {
		t.Fatalf("History() = %d parts, want 1 after one append", len(w.History()))
	}

	// Fork carries the new turn text; the parent history is preserved.
	conv := w.Fork("second")
	if conv == nil {
		t.Fatal("Fork returned nil")
	}
	// The forked conversation holds the prior turn + the new one.
	if len(conv.History()) != 2 {
		t.Errorf("forked History() = %d, want 2 (prior + new turn)", len(conv.History()))
	}
}

func TestConversationStoreAppendAndPersist(t *testing.T) {
	// Conversation memory is per-session, resumable (§11.3.2). Append adds a
	// message; Persist writes the session's messages to JSON so a re-open can
	// resume. L10-001 uses a JSON-file backing (stdlib only — modernc.org/sqlite
	// is a documented future upgrade; the session layer set this precedent).
	dir := t.TempDir()
	c := NewConversationStore(dir)
	c.AppendAssistant(context.Background(), "s_1", Message{Role: "assistant", Text: "hi"})
	if err := c.Persist(context.Background(), "s_1"); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if got := c.Messages("s_1"); len(got) != 1 || got[0].Text != "hi" {
		t.Errorf("Messages(s_1) = %+v, want one 'hi'", got)
	}

	// A fresh store over the same dir resumes the conversation (load-on-Open).
	c2 := NewConversationStore(dir)
	if err := c2.Load(context.Background(), "s_1"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c2.Messages("s_1"); len(got) != 1 || got[0].Text != "hi" {
		t.Errorf("resumed Messages(s_1) = %+v, want one 'hi'", got)
	}
}

func TestExecHistoryAppendAndEntries(t *testing.T) {
	// Execution history is the per-task audit trail (§11.4.1): tool calls,
	// patches, reflections, verify verdicts. Append records an entry; Entries
	// returns them in seq order so Reflection can read "what went wrong".
	dir := t.TempDir()
	e := NewExecHistoryStore(dir)
	e.Append(context.Background(), "t_1", ExecEntry{Kind: "tool", Summary: "read a.go"})
	e.Append(context.Background(), "t_1", ExecEntry{Kind: "patch", Summary: "fixed brace"})
	if got := e.Entries("t_1"); len(got) != 2 {
		t.Errorf("Entries(t_1) = %d, want 2", len(got))
	} else if got[0].Summary != "read a.go" || got[1].Summary != "fixed brace" {
		t.Errorf("Entries order = %+v, want seq order", got)
	} else if got[0].Seq != 1 || got[1].Seq != 2 {
		// The seq IS the append-order audit trail (§11.4.1) — Reflection reads
		// "what went wrong last time" by seq. A flat or unordered seq breaks that.
		t.Errorf("Entries seq = %d/%d, want 1/2 (append order)", got[0].Seq, got[1].Seq)
	}
}

func TestProjectStoreInvalidates(t *testing.T) {
	// Repository memory survives across sessions (§11.4.2): AGENTS.md + a tree
	// cache invalidated on file writes. Invalidate marks the given paths stale so
	// the next read re-walks them.
	dir := t.TempDir()
	p := NewProjectStore(dir)
	if p == nil {
		t.Fatal("NewProjectStore returned nil")
	}
	p.Invalidate([]string{"a.go", "b.md"})
	if got := p.Stale(); len(got) != 2 {
		t.Errorf("Stale() = %d, want 2 after invalidate", len(got))
	}
}

func TestPreferenceStoreGetSetAll(t *testing.T) {
	// Preference memory is per-user, cross-project, the ONE store the user can
	// edit directly (§11.5.2). Get/Set/All over a key/value map persisted to JSON
	// so a preference set in session A is recalled in session B (L10-005 exit).
	dir := t.TempDir()
	p := NewPreferenceStore(dir)
	if err := p.Set(context.Background(), "test-style", "table-driven"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := p.Get(context.Background(), "test-style")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "table-driven" {
		t.Errorf("Get = %q, want 'table-driven'", got)
	}
	all, err := p.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if all["test-style"] != "table-driven" {
		t.Errorf("All[test-style] = %q, want 'table-driven'", all["test-style"])
	}
}

func TestStoreOpensWithAllSubStores(t *testing.T) {
	// The Store aggregate (§11.8) owns all six sub-stores. Open wires them; each
	// accessor returns a non-nil store so a consumer (the composition root)
	// never nil-panics. SemanticStore is a stub in L10-001 (populated in L10-003).
	s, err := Open(Deps{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if s.Working() == nil {
		t.Error("Working() = nil")
	}
	if s.Conversation() == nil {
		t.Error("Conversation() = nil")
	}
	if s.ExecHistory() == nil {
		t.Error("ExecHistory() = nil")
	}
	if s.Project() == nil {
		t.Error("Project() = nil")
	}
	if s.Preferences() == nil {
		t.Error("Preferences() = nil")
	}
	if s.Semantic() == nil {
		t.Error("Semantic() = nil (stub should still exist in L10-001)")
	}
}
