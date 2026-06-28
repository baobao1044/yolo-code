// Execution history — per-task audit trail (§11.4.1): what the agent did. Each
// entry is a tool call, a patch, a reflection note, or a verify verdict, with a
// one-line summary and the git checkpoint it produced. The Session Manager's
// history/undo (File 03 §3.3) and Reflection (File 07 §7.3 — "what went wrong
// last time") read this. JSON-file backed, one file per task under
// root/exec/<tid>.json (stdlib-only; SQLite is a documented future upgrade).
//
// Mutator discipline (§11.2): Append is called by the event listener reacting
// to tool.result/patch.applied/etc. The slice order IS the seq (append order).

package memory

import (
	"context"
	"path/filepath"
	"sync"
)

// ExecHistoryStore persists per-task execution entries to JSON files.
type ExecHistoryStore struct {
	root  string
	mu    sync.Mutex
	tasks map[string][]ExecEntry
}

// NewExecHistoryStore returns a JSON-file exec-history store rooted at dir.
func NewExecHistoryStore(dir string) *ExecHistoryStore {
	return &ExecHistoryStore{root: dir, tasks: make(map[string][]ExecEntry)}
}

func (s *ExecHistoryStore) path(tid string) string {
	return filepath.Join(s.root, "exec", tid+".json")
}

// Append records an entry for the task, assigning the next seq (append order).
// The listener calls this reacting to tool.result (L10-002).
func (s *ExecHistoryStore) Append(_ context.Context, tid string, e ExecEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Seq = len(s.tasks[tid]) + 1
	s.tasks[tid] = append(s.tasks[tid], e)
}

// Persist writes the task's entries to its JSON file.
func (s *ExecHistoryStore) Persist(_ context.Context, tid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.path(tid), s.tasks[tid])
}

// Load re-reads the task's entries into the warm cache.
func (s *ExecHistoryStore) Load(_ context.Context, tid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ExecEntry
	if err := readJSON(s.path(tid), &entries); err != nil {
		return err
	}
	s.tasks[tid] = entries
	return nil
}

// Entries returns the task's entries in seq order.
func (s *ExecHistoryStore) Entries(tid string) []ExecEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecEntry(nil), s.tasks[tid]...)
}
