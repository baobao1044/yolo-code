// The memory Store aggregate (File 11 §11.8): owns the six sub-stores and
// exposes them via accessors the Context Engine (File 06) and Session Manager
// (File 03) consume. Open wires them over a JSON-file backing (stdlib only;
// SQLite is a documented future upgrade — the session layer set the precedent).
//
// Open does NOT start the event listener yet — L10-002 wires listen(bus). L10-
// 001 ships the store shapes + a direct (package-private) mutator per sub-store
// so the surfaces are exercisable now. The cardinal rule (§11.2 — updates only
// via events) lands with the listener; until then the package's mutators are
// callable only from within memory (the listener) and the composition root.

package memory

import (
	"sync"

	"github.com/yolo-code/yolo/internal/event"
)

// Deps wires a Store. Root is the on-disk root for the persistent sub-stores
// (conversations/, exec/, preference.json, AGENTS.md read path). Bus is nil in
// L10-001; L10-002 starts the listener on it (Subscribe + drain goroutine).
type Deps struct {
	Root string
	Bus  *event.Bus
}

// Store owns the six sub-stores (§11.8) and, when a Bus is wired, the event
// listener goroutine that is the ONLY writer to them (besides the
// user-editable Preference store, §11.2). The listener is idempotent on
// Env.Seq (§5.6.1).
type Store struct {
	working      *WorkingMemory
	conversation *ConversationStore
	exec         *ExecHistoryStore
	repo         *ProjectStore
	knowledge    *SemanticStore
	pref         *PreferenceStore

	// Listener state (L10-002). bus/ch/done/listening are set by listen; seen
	// + seenMu track applied seqs for idempotency.
	bus       *event.Bus
	ch        <-chan event.Envelope
	done      chan struct{}
	listening bool
	seenMu    sync.Mutex
	seen      map[uint64]bool
}

// Open wires a Store from Deps. The persistent stores load lazily (a Get/Load
// re-reads the file); L10-005 adds eager cross-session load on Open. Returns a
// store whose accessors are all non-nil (the L10-001 exit bar).
func Open(d Deps) (*Store, error) {
	s := &Store{
		working:      &WorkingMemory{},
		conversation: NewConversationStore(d.Root),
		exec:         NewExecHistoryStore(d.Root),
		repo:         NewProjectStore(d.Root),
		knowledge:    NewSemanticStore(),
		pref:         NewPreferenceStore(d.Root),
	}
	if d.Bus != nil {
		s.listen(d.Bus) // start the listener goroutine (the only sub-store writer)
	}
	return s, nil
}

// Working returns the in-process working memory (§11.3.1).
func (s *Store) Working() *WorkingMemory { return s.working }

// Conversation returns the per-session conversation store (§11.3.2).
func (s *Store) Conversation() *ConversationStore { return s.conversation }

// ExecHistory returns the per-task execution audit trail (§11.4.1).
func (s *Store) ExecHistory() *ExecHistoryStore { return s.exec }

// Project returns the repository memory — AGENTS.md + tree cache (§11.4.2).
func (s *Store) Project() *ProjectStore { return s.repo }

// Semantic returns the vector RAG store (§11.6). L10-001 stub; L10-003 fills it.
func (s *Store) Semantic() *SemanticStore { return s.knowledge }

// Preferences returns the per-user preference store (§11.5.2).
func (s *Store) Preferences() *PreferenceStore { return s.pref }

// Close releases the store's resources. If the listener is running, this waits
// for the drain goroutine to exit (the bus's Close closes the subscriber
// channel → the range ends → done is closed). Idempotent.
func (s *Store) Close() error {
	if s != nil && s.listening && s.done != nil {
		<-s.done
	}
	return nil
}
