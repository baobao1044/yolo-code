// The Manager ties sessions, tasks, the store, the bus, the checkpointer, and
// the cost view together (File 03 §3.7). Sprint 1 implements the lifecycle
// (open/start/complete) here; checkpoints, undo, resume, pause, and cancel are
// added in L1-002/L1-003.

package session

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yolo-code/yolo/internal/event"
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

	// Monotonic counters for deterministic IDs. Determinism matters: the
	// headless demo (S5) must produce byte-identical transcripts across runs, so
	// IDs cannot be random. A fresh process starts at 1, so a single-turn demo
	// always yields s_1 / t_1. Sprint 7 (SQLite) will swap these for UUIDs once
	// cross-restart uniqueness is exercised by real resume.
	sessN atomic.Uint64
	taskN atomic.Uint64
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

// OpenSession creates a new session for a project, persists it, and returns its
// ID. The title is auto-derived from the first user message (here passed in).
func (m *Manager) OpenSession(ctx context.Context, projectID, title string) (ID, error) {
	sid := ID("s_" + itoa(m.sessN.Add(1)))
	now := time.Now().UTC()
	s := &Session{
		ID:        sid,
		ProjectID: projectID,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.mu.Lock()
	m.sessions[sid] = s
	m.mu.Unlock()
	if err := m.store.SaveSession(ctx, s); err != nil {
		return "", err
	}
	return sid, nil
}

// StartTask allocates a task within a session and emits task.started. It does
// NOT call the model — it allocates the framing; the runtime (File 04) later
// transitions the task to RUNNING and drives the loop (File 03 §3.2.3).
func (m *Manager) StartTask(ctx context.Context, sid ID, goal string) (TaskID, error) {
	m.mu.Lock()
	if _, ok := m.sessions[sid]; !ok {
		m.mu.Unlock()
		return "", ErrUnknownSession
	}
	m.mu.Unlock()

	tid := TaskID("t_" + itoa(m.taskN.Add(1)))
	now := time.Now().UTC()
	t := &Task{
		ID:        tid,
		SessionID: sid,
		Goal:      goal,
		Status:    StatusPending,
		RetryMax:  m.costCtrl.ReflectionCap(),
		StartedAt: now,
	}

	m.mu.Lock()
	m.tasks[tid] = t
	sess := m.sessions[sid]
	sess.Tasks = append(sess.Tasks, tid)
	sess.UpdatedAt = now
	m.mu.Unlock()

	if err := m.store.SaveTask(ctx, t); err != nil {
		return "", err
	}
	if err := m.store.SaveSession(ctx, sess); err != nil {
		return "", err
	}
	if err := m.bus.Publish(ctx, &event.TaskStartedEvent{
		Task: string(tid), Session: string(sid), Goal: goal,
	}); err != nil {
		return "", err
	}
	return tid, nil
}

// CompleteTask marks a task DONE, stamps its end time, persists, and publishes
// task.completed. Called by the runtime when verification passes (File 04
// §4.3, transition T4/T12).
func (m *Manager) CompleteTask(ctx context.Context, tid TaskID) error {
	m.mu.Lock()
	t, ok := m.tasks[tid]
	m.mu.Unlock()
	if !ok {
		return ErrUnknownTask
	}
	now := time.Now().UTC()
	t.Status = StatusDone
	t.EndedAt = &now
	if err := m.store.SaveTask(ctx, t); err != nil {
		return err
	}
	return m.bus.Publish(ctx, &event.TaskCompletedEvent{Task: string(tid)})
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
