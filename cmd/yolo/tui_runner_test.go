// Sprint 13+ TUI integration: the driver turns user.submit events into real
// single-agent or multi-agent runs.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

// TestTUIDriverSingleGoal wires a real runtime.Core and checks that a
// user.submit event drives the task through to task.completed.
func TestTUIDriverSingleGoal(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		t.Fatal(err)
	}
	core := runtime.New(runtime.Deps{
		Bus: bus, Session: smgr,
		Context: deps.context, Prompt: deps.prompt, Cognitive: deps.cog,
		Exec: deps.exec, Verify: deps.verify, Patch: deps.patcher, Restore: deps.restorer,
	})

	ctx, cancel := context.WithCancel(context.Background())
	drv := &tuiDriver{
		ctx:    ctx,
		cancel: cancel,
		bus:    bus,
		smgr:   smgr,
		core:   core,
		sid:    sid,
		repo:   t.TempDir(),
	}
	drv.Start()
	defer drv.Stop()

	// Give the driver's user.> subscriber time to register before publishing.
	time.Sleep(50 * time.Millisecond)

	ch := bus.Subscribe(event.Topic(">"))
	done := make(chan struct{})
	var sawStarted, sawCompleted, sawAssistant bool
	go func() {
		for env := range ch {
			switch env.Evt.Type() {
			case "task.started":
				sawStarted = true
			case "task.completed":
				sawCompleted = true
			case "assistant.message":
				sawAssistant = true
			}
			if sawStarted && sawAssistant && sawCompleted {
				close(done)
				return
			}
		}
	}()

	_ = bus.Publish(ctx, &event.UserSubmitEvent{Text: "hello"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("single run not completed; started=%v assistant=%v completed=%v", sawStarted, sawAssistant, sawCompleted)
	}
}

// TestTUIDriverMultiGoal routes a multi-clause goal through the orchestrator.
func TestTUIDriverMultiGoal(t *testing.T) {
	dir := t.TempDir()
	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(context.Background(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		t.Fatal(err)
	}
	core := runtime.New(runtime.Deps{
		Bus: bus, Session: smgr,
		Context: deps.context, Prompt: deps.prompt, Cognitive: deps.cog,
		Exec: deps.exec, Verify: deps.verify, Patch: deps.patcher, Restore: deps.restorer,
	})

	ctx, cancel := context.WithCancel(context.Background())
	drv := &tuiDriver{
		ctx:    ctx,
		cancel: cancel,
		bus:    bus,
		smgr:   smgr,
		core:   core,
		sid:    sid,
		repo:   t.TempDir(),
	}
	drv.Start()
	defer drv.Stop()

	// Give the driver's user.> subscriber time to register before publishing.
	time.Sleep(50 * time.Millisecond)

	ch := bus.Subscribe(event.Topic(">"))
	done := make(chan struct{})
	var sawPlan, sawDone bool
	go func() {
		for env := range ch {
			switch env.Evt.Type() {
			case "coord.plan.ready":
				sawPlan = true
			case "plan.done":
				sawDone = true
			}
			if sawPlan && sawDone {
				close(done)
				return
			}
		}
	}()

	_ = bus.Publish(ctx, &event.UserSubmitEvent{Text: "add a.txt, add b.txt, add c.txt"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("multi run not completed; plan=%v done=%v", sawPlan, sawDone)
	}
}
