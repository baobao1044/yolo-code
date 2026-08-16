// The task object the drive loop carries, and who else is allowed to touch it.
//
// The Session Manager guards its tasks with a mutex; the runtime got a raw
// pointer past that mutex and then handed it to a port that writes to it. This
// pins the boundary: what the drive loop holds must be the runtime's own, so a
// user event landing on the Manager cannot collide with it.

package runtime

import (
	"context"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/session"
)

// racingCog reaches Reflect and then does what the shipped reflection engine
// does to the task it is handed: internal/cognitive/reflection.go:112 is a bare
// `task.Retry++`, with no lock and no copy. entered/release let the test place
// that write in the same instant as a user cancel instead of hoping the
// scheduler interleaves them.
type racingCog struct {
	entered chan *session.Task
	release chan struct{}
	turns   int
}

func (r *racingCog) Think(context.Context, Prompt) (CognitiveTurn, error) {
	r.turns++
	if r.turns == 1 {
		return CognitiveTurn{ToolCalls: []ToolCall{
			{ID: "c1", Tool: "edit_file", Args: []byte(`{"path":"broken.go"}`)},
		}}, nil
	}
	return CognitiveTurn{Final: true, Text: "done"}, nil
}

func (*racingCog) HasMore(*session.Task) bool { return false }

func (r *racingCog) Reflect(_ context.Context, task *session.Task, _ Verdict, _ Observation) ReflectionDecision {
	r.entered <- task
	<-r.release
	task.Retry++
	return ReflectionDecision{Abort: true, Note: "stop"}
}

func (*racingCog) RecordToolResult(string, string, string) {}
func (*racingCog) Reset()                                  {}

// failingVerifier sends every observation to reflection, which is the only
// route to the write above.
type failingVerifier struct{}

func (failingVerifier) Verify(context.Context, Observation, *session.Task, VerifyPolicy) (Verdict, error) {
	return Verdict{Pass: false, Stage: "ast", Severity: "error", Reason: "stage ast failed"}, nil
}

// TestLiveTaskPointerDoesNotRaceTheSessionManager is the boundary test for
// session.LoadTaskPublic, which returns the live *Task out of the Manager's
// mutex-protected map.
//
// Holding that pointer for the length of the task means two goroutines write
// and read the same struct with no common lock. The drive goroutine reaches
// Reflect, which increments task.Retry. The user-event goroutine calls
// Manager.Cancel for the same task, which takes m.mu and calls (*Task).clone —
// `c := *t`, a read of every field. The Manager's mutex does not exclude the
// drive loop because the drive loop never takes it, so the two collide on a
// user pressing esc while a reflection is in flight. Ordinary, not exotic.
//
// Run with -race: the failure is a WARNING: DATA RACE naming
// session.(*Task).clone as the reader and the reflection's Retry++ as the
// writer. -count is what makes it convincing; the window is the whole reflection
// call, so it does not need luck, but repetition rules out a one-off schedule.
func TestLiveTaskPointerDoesNotRaceTheSessionManager(t *testing.T) {
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	store := session.NewFileStore(t.TempDir())
	smgr := session.New(session.Deps{Store: store, Bus: bus, Git: session.NewInMemCheckpointer()})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	cog := &racingCog{entered: make(chan *session.Task), release: make(chan struct{})}
	core := New(Deps{
		Bus:       bus,
		Session:   smgr,
		Cognitive: cog,
		Exec:      &recordingExec{obs: Observation{Files: []string{"broken.go"}}},
		Verify:    failingVerifier{},
	})

	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		task := <-cog.entered
		tid := task.ID
		// Release first, so the Retry++ and the Manager's clone are in flight
		// together rather than one after the other.
		close(cog.release)
		_ = smgr.Cancel(context.Background(), tid, "user pressed esc")
	}()

	if _, err := core.Submit(context.Background(), sid, "one failing call"); err != nil {
		t.Fatal(err)
	}
	<-cancelled
}
