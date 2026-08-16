// The Manager ties sessions, tasks, the store, the bus, the checkpointer, and
// the cost view together (File 03 §3.7). Sprint 1 implements the lifecycle
// (open/start/complete) here; checkpoints, undo, resume, pause, and cancel are
// added in L1-002/L1-003.

package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Deps are the Manager's collaborators, injected at construction so tests can
// substitute fakes (File 03 §3.7).
type Deps struct {
	Store    Store
	Bus      *event.Bus
	Git      Checkpointer
	CostCtrl CostView // nil → DefaultCostView
}

// Manager owns the session/task lifecycle and the per-task undo stack.
type Manager struct {
	store    Store
	bus      *event.Bus
	git      Checkpointer
	costCtrl CostView

	mu       sync.Mutex
	sessions map[ID]*Session
	tasks    map[TaskID]*Task
	// cancels holds the cancel function the runtime (File 04) attaches so the
	// Session Manager can cascade cancellation to the task's context (§3.6.1).
	cancels map[TaskID]context.CancelFunc

	// Monotonic counters for IDs, seeded from the store on first allocation
	// (see seedCounters) and claimed through the store's Create*, which
	// refuses an ID that is already on disk.
	//
	// They used to be plain per-process counters starting at zero, which made
	// every ID s_1 / t_1. On the TUI's durable root
	// (~/.config/yolo-code/sessions) that meant each launch's os.WriteFile
	// truncated the previous launch's session and task, so yesterday's title,
	// goal, task status and undo history were destroyed at startup and resume
	// was structurally impossible.
	//
	// SCOPE OF THE GUARANTEE — read this before adding another Manager.
	// IDs are unique per STORE ROOT, across processes and restarts, and that
	// is all. Two Managers sharing a root cannot collide, because each claims
	// its ID atomically against that directory. Two Managers on DIFFERENT
	// roots both start at s_1 / t_1 and will happily mint the same ID.
	//
	// That second case is live today: cmd/yolo builds one Manager on
	// sessionStateDir() (tui_runner.go) and another on its "coord"
	// subdirectory (coord_runner.go), both publishing to one bus, so two
	// different tasks both call themselves t_1 and every bus consumer sees one
	// task. The remedy is one shared root, which this change is what makes
	// safe — see TestTwoManagersOnOneRootMintDisjointIDs. Uniqueness is
	// deliberately not process-global: a process-wide counter would make the
	// headless demo's IDs depend on how many Managers happened to be built
	// first, and its transcript is required to be byte-identical (S5).
	//
	// Determinism survives where it is actually required: the headless demo
	// runs against a disposable temp dir, so the seed finds nothing, a
	// single-turn demo still yields s_1 / t_1, and its transcript stays
	// byte-identical.
	seedOnce sync.Once
	sessN    atomic.Uint64
	taskN    atomic.Uint64
}

// New constructs a Manager. CostCtrl defaults to DefaultCostView when nil.
func New(d Deps) *Manager {
	m := &Manager{
		store:    d.Store,
		bus:      d.Bus,
		git:      d.Git,
		costCtrl: d.CostCtrl,
		sessions: make(map[ID]*Session),
		tasks:    make(map[TaskID]*Task),
		cancels:  make(map[TaskID]context.CancelFunc),
	}
	if m.costCtrl == nil {
		m.costCtrl = DefaultCostView{}
	}
	return m
}

// seedCounters advances both ID counters past everything already on disk so a
// fresh process over a durable root does not reissue IDs an earlier one wrote.
// It runs once, lazily on the first allocation, so a Manager over an empty root
// never touches the disk before it has to and still starts at 1.
//
// One ListSessions covers both counters: every task is recorded in its
// session's Tasks slice, so no separate task listing is needed.
//
// Failures are ignored on purpose. The seed is an optimisation that keeps
// allocation O(1) instead of walking up from 1; correctness rests entirely on
// the atomic claim in OpenSession/StartTask, which catches whatever the seed
// missed — an unreadable session file, or a session another process wrote a
// microsecond ago.
func (m *Manager) seedCounters(ctx context.Context) {
	m.seedOnce.Do(func() {
		sessions, err := m.store.ListSessions(ctx, "")
		if err != nil {
			return
		}
		var maxSess, maxTask uint64
		for _, s := range sessions {
			if n, ok := idSeq(string(s.ID), "s_"); ok && n > maxSess {
				maxSess = n
			}
			for _, tid := range s.Tasks {
				if n, ok := idSeq(string(tid), "t_"); ok && n > maxTask {
					maxTask = n
				}
			}
		}
		m.sessN.Store(maxSess)
		m.taskN.Store(maxTask)
	})
}

// OpenSession creates a new session for a project, persists it, and returns its
// ID. The title is auto-derived from the first user message (here passed in).
func (m *Manager) OpenSession(ctx context.Context, projectID, title string) (ID, error) {
	m.seedCounters(ctx)
	now := time.Now().UTC()
	// Claim numbers until one sticks. ErrIDTaken means another process (or an
	// earlier launch whose file the seed could not read) already owns that one.
	// The counter only ever goes up, so this terminates as soon as it passes
	// the highest ID on disk.
	for {
		sid := ID("s_" + itoa(m.sessN.Add(1)))
		s := &Session{
			ID:        sid,
			ProjectID: projectID,
			Title:     title,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := m.store.CreateSession(ctx, s)
		if errors.Is(err, ErrIDTaken) {
			continue
		}
		if err != nil {
			return "", err
		}
		// Registered only once the claim has landed: registering first left a
		// phantom session in the map whenever the write failed.
		m.mu.Lock()
		m.sessions[sid] = s
		m.mu.Unlock()
		return sid, nil
	}
}

// StartTask allocates a task within a session and emits task.started. It does
// NOT call the model — it allocates the framing; the runtime (File 04) later
// transitions the task to RUNNING and drives the loop (File 03 §3.2.3).
func (m *Manager) StartTask(ctx context.Context, sid ID, goal string) (TaskID, error) {
	m.mu.Lock()
	sess, ok := m.sessions[sid]
	m.mu.Unlock()
	if !ok {
		return "", ErrUnknownSession
	}
	m.seedCounters(ctx)

	now := time.Now().UTC()
	var t *Task
	for {
		tid := TaskID("t_" + itoa(m.taskN.Add(1)))
		t = &Task{
			ID:        tid,
			SessionID: sid,
			Goal:      goal,
			Status:    StatusPending,
			RetryMax:  m.costCtrl.ReflectionCap(),
			StartedAt: now,
		}
		err := m.store.CreateTask(ctx, t)
		if errors.Is(err, ErrIDTaken) {
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}

	m.mu.Lock()
	m.tasks[t.ID] = t
	sess.Tasks = append(sess.Tasks, t.ID)
	sess.UpdatedAt = now
	snap := sess.clone()
	m.mu.Unlock()

	if err := m.store.SaveSession(ctx, snap); err != nil {
		return "", err
	}
	if err := m.bus.Publish(ctx, &event.TaskStartedEvent{
		Task: string(t.ID), Session: string(sid), Goal: goal,
	}); err != nil {
		return "", err
	}
	return t.ID, nil
}

// CompleteTask marks a task DONE, stamps its end time, persists, and publishes
// task.completed. Called by the runtime when verification passes (File 04
// §4.3, transition T4/T12).
func (m *Manager) CompleteTask(ctx context.Context, tid TaskID) error {
	now := time.Now().UTC()
	m.mu.Lock()
	t, ok := m.tasks[tid]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownTask
	}
	// Under the lock. These two writes used to happen outside it entirely, so
	// they raced every other mutator and every serialization of the same task.
	t.Status = StatusDone
	t.EndedAt = &now
	snap := t.clone()
	m.mu.Unlock()

	if err := m.store.SaveTask(ctx, snap); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.TaskCompletedEvent{Task: string(tid)})
}

// Fail marks a task FAILED, stamps its end time, persists, and publishes
// task.failed. It is the terminal outcome CompleteTask and Cancel had no
// sibling for: File 03 §3.2 draws RUNNING --> FAILED on "retries exhausted /
// hard error", and until this existed nothing in the tree could take that edge.
//
// StatusFailed was declared terminal in types.go from the start and had zero
// writers, so a task killed by a hard error kept whatever non-terminal status
// it was last saved with. That is not inert: a non-terminal status on disk is
// what Resume reads to decide a task is unfinished work worth offering back to
// the user, so the dead task reappeared on the next launch.
//
// reason is the cause as the caller saw it, carried on the event so a log
// reader can tell one failure from another.
//
// A task that has already ended is left alone (ErrTaskAlreadyTerminal). A
// failure arriving after a completion must not rewrite the outcome — the work
// did land, and the error belongs to whatever happened afterwards.
func (m *Manager) Fail(ctx context.Context, tid TaskID, reason string) error {
	now := time.Now().UTC()
	m.mu.Lock()
	t, ok := m.tasks[tid]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownTask
	}
	if t.Status.IsTerminal() {
		m.mu.Unlock()
		return ErrTaskAlreadyTerminal
	}
	// Mutate and snapshot under the one lock, as CompleteTask does: these
	// writes race every other mutator and every serialization of the same task
	// if they happen outside it.
	t.Status = StatusFailed
	t.EndedAt = &now
	snap := t.clone()
	m.mu.Unlock()

	if err := m.store.SaveTask(ctx, snap); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.TaskFailedEvent{Task: string(tid), Reason: reason})
}

// AttachCancel lets the runtime (File 04) register the task's cancel function so
// the Session Manager can cascade cancellation (§3.6.1).
func (m *Manager) AttachCancel(tid TaskID, cancel context.CancelFunc) {
	m.mu.Lock()
	m.cancels[tid] = cancel
	m.mu.Unlock()
}

// task returns the live task handle, failing if unknown.
func (m *Manager) task(tid TaskID) (*Task, error) {
	m.mu.Lock()
	t, ok := m.tasks[tid]
	m.mu.Unlock()
	if !ok {
		return nil, ErrUnknownTask
	}
	return t, nil
}

// LoadTaskPublic returns the live task handle for a known task, or nil if
// unknown. It is the public face of the internal task lookup.
//
// The pointer is the Manager's own object, not a copy, and the Manager keeps
// writing to it: Cancel sets Status and EndedAt, CompleteTask stamps the end
// time, every save reads the whole struct through clone. So the pointer must
// not outlive the call, and must not cross a goroutine boundary — a caller
// that wants to hold a task wants SnapshotTask.
//
// The aliasing is load-bearing where it is used within one goroutine:
// internal/context/engine_test.go:68 depends on it deliberately, because
// RecordEntry appends to the same *Task the next Build carries. Cloning here
// would trade a race for silent History staleness, which is worse — hence a
// second accessor rather than a change to this one.
func (m *Manager) LoadTaskPublic(tid TaskID) *Task {
	t, err := m.task(tid)
	if err != nil {
		return nil
	}
	return t
}

// SnapshotTask returns a copy of a known task taken under the Manager's mutex,
// or nil if unknown. This is what a caller that retains a task across
// goroutines needs: LoadTaskPublic hands out the live object, so copying it
// afterwards is itself an unsynchronized read of a struct the Manager may be
// writing at that moment. Taking the copy inside the lock closes that window.
//
// The runtime (File 04) holds a task for the whole drive loop and hands it to
// Reflect, which writes Retry — it is the caller this exists for.
func (m *Manager) SnapshotTask(tid TaskID) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[tid]
	if !ok {
		return nil
	}
	return t.clone()
}

// SnapshotSession returns a copy of a known session taken under the Manager's
// mutex, or nil if unknown. It is the Session-shaped twin of SnapshotTask, and
// it exists for the same caller and the same reason.
//
// The runtime holds one *Session for the whole drive loop (it goes into every
// ContextRequest). Before this it got that pointer from Resume, which hands
// back the live object out of m.sessions — and StartTask appends to
// Session.Tasks and stamps UpdatedAt on that very object under m.mu. So a
// second Submit on the same session wrote the slice header the first Submit's
// drive loop was reading, unsynchronized. Reproduced deterministically (the
// retained session's Tasks grew from 1 to 2 underneath its holder) and under
// -race as a genuine write/read pair on manager.go:199.
//
// The copy is deep enough for what leaves the package: Session.clone copies the
// Tasks slice, and every other field is a value. Nothing is lost by it either —
// the runtime reads ID/ProjectID/Title/Model, all fixed for the session's life.
//
// A second accessor rather than a change to Resume, for the reason
// LoadTaskPublic records above: Resume's contract is to install the session in
// the Manager and hand back what it installed, and a caller that resumes in
// order to keep watching it is entitled to the live object. Callers that RETAIN
// one across goroutines want this instead.
func (m *Manager) SnapshotSession(sid ID) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sid]
	if !ok {
		return nil
	}
	return s.clone()
}

// idSeq parses the numeric tail of an ID like "s_12", reporting false for any
// shape it does not recognise — a hand-written ID, or a format a later sprint
// introduces. An unrecognised ID simply does not raise the seed; the store's
// atomic claim still prevents a collision. Spelled out rather than reaching for
// strconv, matching itoa's habit of keeping this package's imports tight.
func idSeq(id, prefix string) (uint64, bool) {
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return 0, false
	}
	var n uint64
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (1<<62)/10 { // absurdly long digit run; refuse rather than wrap
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}
