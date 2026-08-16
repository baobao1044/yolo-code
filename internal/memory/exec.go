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
	"errors"
	"path/filepath"
	"sort"
	"sync"
)

// ExecHistoryStore persists per-task execution entries to JSON files.
type ExecHistoryStore struct {
	root  string
	mu    sync.Mutex
	tasks map[string][]ExecEntry
	// seqCounter tracks the monotonic per-task seq independent of the slice
	// length, so the rolling window cap (below) doesn't reset it.
	seqCounter map[string]int
}

// execWindow is the rolling-window cap (§11.4.1: "last 50 results"). Once a
// task exceeds it, the oldest entries are dropped so the audit trail stays
// bounded and the Context Engine's exec-memory input doesn't grow unbounded.
const execWindow = 50

// NewExecHistoryStore returns a JSON-file exec-history store rooted at dir.
func NewExecHistoryStore(dir string) *ExecHistoryStore {
	return &ExecHistoryStore{root: dir, tasks: make(map[string][]ExecEntry), seqCounter: make(map[string]int)}
}

func (s *ExecHistoryStore) path(tid string) string {
	return filepath.Join(s.root, "exec", tid+".json")
}

// Append records an entry for the task, assigning the next seq (append order).
// The listener calls this reacting to tool.result (L10-002). The slice is
// capped at execWindow entries (§11.4.1 rolling window); the oldest entries
// are dropped once the cap is exceeded. Seq is monotonic per task (tracked in
// seqCounter, NOT the slice length) so the rolling window doesn't reset it.
func (s *ExecHistoryStore) Append(_ context.Context, tid string, e ExecEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqCounter[tid]++
	e.Seq = s.seqCounter[tid]
	s.tasks[tid] = append(s.tasks[tid], e)
	if len(s.tasks[tid]) > execWindow {
		s.tasks[tid] = s.tasks[tid][len(s.tasks[tid])-execWindow:]
	}
}

// Persist writes the task's entries to its JSON file.
func (s *ExecHistoryStore) Persist(_ context.Context, tid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(s.path(tid), s.tasks[tid])
}

// persistAll writes every cached task's entries. Store.Flush calls it on
// shutdown/checkpoint so an interrupted run still leaves its audit trail; the
// per-task Persist stays the listener's path. Tasks are written in sorted
// order (S5) and one failure doesn't stop the rest.
func (s *ExecHistoryStore) persistAll(ctx context.Context) error {
	s.mu.Lock()
	tids := make([]string, 0, len(s.tasks))
	for tid := range s.tasks {
		tids = append(tids, tid)
	}
	s.mu.Unlock()
	sort.Strings(tids)
	var errs []error
	for _, tid := range tids {
		if err := s.Persist(ctx, tid); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Load re-reads the task's entries into the warm cache. seqCounter is rebuilt
// from the highest loaded Seq so subsequent Appends continue monotonically
// (not from 1, which would collide with the loaded entries).
func (s *ExecHistoryStore) Load(_ context.Context, tid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []ExecEntry
	if err := readJSON(s.path(tid), &entries); err != nil {
		return err
	}
	s.tasks[tid] = entries
	maxSeq := 0
	for _, e := range entries {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	s.seqCounter[tid] = maxSeq
	return nil
}

// Entries returns the task's entries in seq order.
func (s *ExecHistoryStore) Entries(tid string) []ExecEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecEntry(nil), s.tasks[tid]...)
}
