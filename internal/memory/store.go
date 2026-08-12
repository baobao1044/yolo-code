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
	"context"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Deps wires a Store. Root is the on-disk root for the persistent sub-stores
// (conversations/, exec/, preference.json, AGENTS.md read path). Bus is nil in
// L10-001; L10-002 starts the listener on it (Subscribe + drain goroutine).
// Embedder is the optional vector embedder for the SemanticStore (File 11
// §11.7.4); nil → the default deterministic hash embedder at dim 384 (the spec
// §11.6.2 range 384–768; 384 is the air-gapped MVP floor). FS is the optional
// file reader the SemanticStore uses to reindex a path's content on
// patch.applied (§11.7.5); nil → Reindex can't read content (cold-start
// indexing still works via IndexRepo's own walk).
type Deps struct {
	Root     string
	Bus      *event.Bus
	Embedder Embedder
	FS       FS
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
	knowledge    *SemanticStore // code-chunk RAG (§11.6)
	insights     *KnowledgeStore // insight RAG (§11.5.1) — distinct from code chunks
	pref         *PreferenceStore

	// Listener state (L10-002). bus/ch/done/listening are set by listen; seen
	// + seenMu track applied seqs for idempotency.
	bus       *event.Bus
	ch        <-chan event.Envelope
	done      chan struct{}
	listening bool
	seenMu    sync.Mutex
	seen      map[uint64]bool
	// bg tracks background work (Persist/Reindex offloaded from dispatch so
	// the drain stays fast). Close waits on it after the drain ends so a
	// Store shutdown doesn't leave file I/O racing a temp-dir cleanup.
	bg sync.WaitGroup
}

// Open wires a Store from Deps. The persistent stores load lazily (a Get/Load
// re-reads the file); L10-005 adds eager cross-session load on Open. Returns a
// store whose accessors are all non-nil (the L10-001 exit bar).
func Open(d Deps) (*Store, error) {
	emb := d.Embedder
	if emb == nil {
		emb = NewHashEmbedder(384)
	}
	s := &Store{
		working:      &WorkingMemory{},
		conversation: NewConversationStore(d.Root),
		exec:         NewExecHistoryStore(d.Root),
		repo:         NewProjectStore(d.Root),
		knowledge:    NewSemanticStoreWithFS(emb, d.FS),
		insights:     NewKnowledgeStore(d.Root, emb),
		pref:         NewPreferenceStore(d.Root),
	}
	// Eager-load the cross-session stores (§11.3.3 + §11.5.2). Preference +
	// Knowledge are per-user, cross-project (shared files), so Open re-reads
	// them so a lesson/pref set in session A is recalled in session B (the
	// L10-005 exit bar). Conversation/exec-history are per-session/task and
	// load on Resume (when the specific session/task is known); Open leaves
	// them empty.
	if err := s.pref.Load(context.Background()); err != nil {
		return nil, err
	}
	if err := s.insights.Load(context.Background()); err != nil {
		return nil, err
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

// Insights returns the knowledge insight store (§11.5.1) — distinct from the
// code-chunk RAG. Records short lessons learned from task/verify events.
func (s *Store) Insights() *KnowledgeStore { return s.insights }

// Preferences returns the per-user preference store (§11.5.2).
func (s *Store) Preferences() *PreferenceStore { return s.pref }

// Close releases the store's resources. If the listener is running, this waits
// for the drain goroutine to exit (the bus's Close closes the subscriber
// channel → the range ends → done is closed). It then waits for any background
// work (Persist/Reindex offloaded from dispatch) to finish so a shutdown
// doesn't leave file I/O racing a temp-dir cleanup. Idempotent.
func (s *Store) Close() error {
	if s != nil && s.listening && s.done != nil {
		<-s.done
	}
	if s != nil {
		s.bg.Wait()
	}
	return nil
}
