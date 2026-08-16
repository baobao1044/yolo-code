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
	"errors"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Deps wires a Store. Root is the on-disk root for the persistent sub-stores
// (conversations/, exec/, preference.json, AGENTS.md read path). Bus is nil in
// L10-001; L10-002 starts the listener on it (Subscribe + drain goroutine).
// Embedder is the optional embedding model for the LexicalStore (File 11
// §11.7.4) — the seam a real OpenAI/Ollama embedder plugs into; nil → the
// default hashing term-frequency vectorizer at dim 384, which gives lexical
// (shared-token) similarity, not semantic (the spec §11.6.2 range is 384–768;
// 384 is the air-gapped MVP floor). FS is the optional file reader the
// LexicalStore uses to reindex a path's content on patch.applied (§11.7.5);
// nil → Reindex can't read content (cold-start indexing still works via
// IndexRepo's own walk). Redactor masks secrets in the free text the listener
// writes to disk (see Redactor); nil → no redaction, which is what every unit
// test in this package wants and what the composition root must not ship.
type Deps struct {
	Root     string
	Bus      *event.Bus
	Embedder Embedder
	FS       FS
	Redactor Redactor
}

// Redactor masks secrets in a string. memory may import only event + stdlib
// (§15.15.2), so it cannot reach infra.Secrets — the composition root injects
// an implementation through Deps, exactly as it does for exec's normalizer and
// event's durability log.
//
// It exists because the listener is a *writer to disk*: an insight text lands
// in knowledge.json and an assistant message lands in conversations/<sid>.json,
// both of which outlive the session. The three boundaries infra already wired
// (exec output, the log line, the Sentry event) are all projections of the
// event stream; none of them is on the path from the bus to these files, so
// text that was raw at birth — verify runs its own commands and never passes
// through exec's normalizer — reached disk in the clear.
type Redactor interface {
	Redact(string) string
}

// Store owns the six sub-stores (§11.8) and, when a Bus is wired, the event
// listener goroutine that is the ONLY writer to them (besides the
// user-editable Preference store, §11.2). The listener is idempotent on
// Env.Seq (§5.6.1).
type Store struct {
	// root is Deps.Root, kept so Flush can refuse to write into the process's
	// working directory when the caller wired no root at all.
	root         string
	working      *WorkingMemory
	conversation *ConversationStore
	exec         *ExecHistoryStore
	repo         *ProjectStore
	knowledge    *LexicalStore   // code-chunk RAG (§11.6)
	insights     *KnowledgeStore // insight RAG (§11.5.1) — distinct from code chunks
	pref         *PreferenceStore

	// redactor is Deps.Redactor: the secret mask applied to every free-text
	// field the listener persists. nil means pass-through (see redact).
	redactor Redactor

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

	// warnings collects the non-fatal problems Open hit (today: one
	// *CorruptError per store file that existed but wouldn't decode). Written
	// once in Open, read-only afterwards.
	warnings []error
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
		root:         d.Root,
		working:      &WorkingMemory{},
		conversation: NewConversationStore(d.Root),
		exec:         NewExecHistoryStore(d.Root),
		repo:         NewProjectStore(d.Root),
		knowledge:    NewLexicalStoreWithFS(emb, d.FS),
		insights:     NewKnowledgeStore(d.Root, emb),
		pref:         NewPreferenceStore(d.Root),
		redactor:     d.Redactor,
	}
	// Eager-load the cross-session stores (§11.3.3 + §11.5.2). Preference +
	// Knowledge are per-user, cross-project (shared files), so Open re-reads
	// them so a lesson/pref set in session A is recalled in session B (the
	// L10-005 exit bar). Conversation/exec-history are per-session/task and
	// load on Resume (when the specific session/task is known); Open leaves
	// them empty.
	//
	// A corrupt/truncated file is NOT fatal (§11.3.3): a memory the agent can't
	// read must not stop it from running. quarantineOrFail moves the bad bytes
	// aside and records a warning; a genuine I/O failure still aborts Open.
	for _, load := range []func(context.Context) error{s.pref.Load, s.insights.Load} {
		if err := s.quarantineOrFail(load(context.Background())); err != nil {
			return nil, err
		}
	}
	if d.Bus != nil {
		s.listen(d.Bus) // start the listener goroutine (the only sub-store writer)
	}
	return s, nil
}

// quarantineOrFail classifies a load error. A *CorruptError is survivable: the
// file is renamed to <name>.corrupt so the bytes are still recoverable (an
// in-place overwrite by the next Persist would be a silent wipe), a warning is
// recorded, and nil is returned so Open continues with an empty store. Any
// other error — a permission problem, an unreadable root — is returned as-is
// and aborts Open, because that is a broken environment, not a broken file.
func (s *Store) quarantineOrFail(err error) error {
	if err == nil {
		return nil
	}
	var ce *CorruptError
	if !errors.As(err, &ce) {
		return err
	}
	s.warnings = append(s.warnings, err)
	// Through shareRenamer for the same reason writeJSON is: on Windows both
	// ends of this rename can be held open — the corrupt file by another
	// process, and <name>.corrupt by a reader looking at a previous
	// quarantine. Failing here only adds a warning, but a warning that says
	// "access denied" about a file nobody is denying is worse than none.
	if rerr := shareRenamer().do(ce.Path, ce.Path+".corrupt"); rerr != nil {
		s.warnings = append(s.warnings, rerr)
	}
	return nil
}

// Warnings returns the non-fatal problems Open encountered — one *CorruptError
// per store file that existed but wouldn't decode (its bytes were moved to
// <name>.corrupt and the sub-store started empty). Nil means a clean load. The
// composition root surfaces these to the user; nothing in memory acts on them.
func (s *Store) Warnings() []error {
	if s == nil {
		return nil
	}
	return append([]error(nil), s.warnings...)
}

// Flush writes every durable sub-store to disk (§11.3.3). The listener only
// persists on task.completed, so a session that ends any other way — Ctrl-C, a
// crash, a task still running — used to leave nothing behind. Close calls this;
// the composition root can also call it at any checkpoint. Store errors are
// joined so one unwritable file doesn't hide the others. A Store opened with no
// Root is skipped rather than writing into the process's working directory.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil || s.root == "" {
		return nil
	}
	return errors.Join(
		s.pref.Persist(ctx),
		s.insights.Persist(ctx),
		s.conversation.persistAll(ctx),
		s.exec.persistAll(ctx),
	)
}

// Working returns the in-process working memory (§11.3.1).
func (s *Store) Working() *WorkingMemory { return s.working }

// Conversation returns the per-session conversation store (§11.3.2).
//
// DEAD SEAM (read side) — the write side is live: the listener appends on
// assistant.message and persists on task.completed, and Flush writes
// conversations/ on every Close. But nothing outside package memory calls this
// accessor, so the transcript is written to disk and never read back into a
// prompt. The context.Memory port (internal/context/ports.go:16) has no history
// method to read it through — it is Preferences/Project/Retrieve only — so
// animating this seam is a wider change than Insights below, not a one-liner.
// Wiring or deleting is cmd/yolo's call; memory just stops implying it's read.
func (s *Store) Conversation() *ConversationStore { return s.conversation }

// ExecHistory returns the per-task execution audit trail (§11.4.1).
//
// DEAD SEAM (read side) — same shape as Conversation above: the listener
// appends on every tool.result and Flush persists exec/, but no caller outside
// package memory ever reads it. The audit trail accumulates on disk unread.
func (s *Store) ExecHistory() *ExecHistoryStore { return s.exec }

// Project returns the repository memory — AGENTS.md + tree cache (§11.4.2).
func (s *Store) Project() *ProjectStore { return s.repo }

// Semantic returns the code-chunk retrieval index (§11.6). The method keeps
// the spec's name; the store it returns is a *LexicalStore because that is what
// it is with the shipped embedder — hashed term-frequency similarity, not
// semantic search (see semantic.go's header). Inject a real Deps.Embedder and
// the name becomes accurate.
func (s *Store) Semantic() *LexicalStore { return s.knowledge }

// Insights returns the knowledge insight store (§11.5.1) — distinct from the
// code-chunk RAG. Records short lessons learned from verification events.
//
// This was a DEAD SEAM and is no longer one; the note stays because what it
// records is easy to reintroduce. The write half always ran for real —
// verification.failed and verification.stage record insights every run, Open
// loads and re-embeds knowledge.json, Flush and Close write it back — while the
// read half never happened. The single path from memory into a prompt is
// contextMemoryAdapter.Retrieve in cmd/yolo/adapters.go, and it read only the
// OTHER store:
//
//	return toContextParts(a.store.Semantic().Retrieve(ctx, query, topK))
//
// So the advertised cross-session learning recorded lessons nothing ever
// recalled, and charged for them twice over: one knowledge.json per root
// growing with every distinct verification message (bounded at maxInsights, see
// knowledge.go — before that cap it grew forever), plus a full re-embed of
// every stored insight on every Open (KnowledgeStore.Load), which is the one
// piece of startup work that scales with how long the project has been used.
//
// The adapter now queries both stores and splits topK between them — insights
// take a quarter, at least one slot, and the code index takes the rest; see
// that method for why a plain merge-and-sort would be wrong. If a future change
// makes Retrieve read only one store again, the write cost above comes straight
// back. cmd/yolo/insights_recall_test.go fails when it does.
func (s *Store) Insights() *KnowledgeStore { return s.insights }

// Preferences returns the per-user preference store (§11.5.2).
func (s *Store) Preferences() *PreferenceStore { return s.pref }

// Close releases the store's resources. If the listener is running, this waits
// for the drain goroutine to exit (the bus's Close closes the subscriber
// channel → the range ends → done is closed). It then waits for any background
// work (Persist/Reindex offloaded from dispatch) to finish so a shutdown
// doesn't leave file I/O racing a temp-dir cleanup. It then Flushes, so what
// the session learned is on disk even when no task.completed ever fired — a
// store that only saved on task.completed lost everything from an interrupted
// run. Idempotent.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.listening && s.done != nil {
		<-s.done
	}
	s.bg.Wait()
	return s.Flush(context.Background())
}
