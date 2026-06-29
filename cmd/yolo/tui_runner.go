// Sprint 13+ TUI integration runner. This is the default interactive path:
// the TUI renders events from the shared bus and user keystrokes publish
// user.* events that the runtime now consumes.

package main

import (
	"context"
	"os"
	"sync/atomic"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
	"github.com/baobao1044/yolo-code/internal/tui"
)

// runTUI starts the interactive Bubble Tea TUI wired to a real runtime.Core
// (single-agent goals) and a real coord.Orchestrator (multi-agent goals).
func runTUI(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)

	repo, err := os.Getwd()
	if err != nil {
		cancel()
		return err
	}

	dir, err := os.MkdirTemp("", "yolo-tui-*")
	if err != nil {
		cancel()
		return err
	}

	bus := event.New()
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(ctx, "tui", "interactive")
	if err != nil {
		cancel()
		return err
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		cancel()
		return err
	}

	core := runtime.New(runtime.Deps{
		Bus:       bus,
		Session:   smgr,
		Context:   deps.context,
		Prompt:    deps.prompt,
		Cognitive: deps.cog,
		Exec:      deps.exec,
		Verify:    deps.verify,
		Patch:     deps.patcher,
		Restore:   deps.restorer,
	})

	costPub := newCostPublisher(bus)
	costPub.Start(ctx)

	d := &tuiDriver{
		ctx:    ctx,
		cancel: cancel,
		bus:    bus,
		smgr:   smgr,
		core:   core,
		sid:    sid,
		repo:   repo,
	}
	d.Start()

	defer func() {
		cancel()
		d.Stop()
		_ = bus.Close()
		costPub.Stop()
		_ = os.RemoveAll(dir)
	}()

	return tui.Run(ctx, bus, bus)
}

// tuiDriver listens to user.* events from the TUI and starts the right runtime
// path. It serializes submissions so only one task/plan runs at a time.
type tuiDriver struct {
	ctx    context.Context
	cancel context.CancelFunc
	bus    *event.Bus
	smgr   *session.Manager
	core   *runtime.Core
	sid    session.ID
	repo   string
	busy   atomic.Bool
	stop   chan struct{}
	done   chan struct{}
}

func (d *tuiDriver) Start() {
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go d.run()
}

func (d *tuiDriver) Stop() {
	close(d.stop)
	<-d.done
}

func (d *tuiDriver) run() {
	defer close(d.done)
	ch := d.bus.Subscribe(event.Topic("user.>"))
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return
			}
			d.handle(env)
		case <-d.stop:
			return
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *tuiDriver) handle(env event.Envelope) {
	switch e := env.Evt.(type) {
	case *event.UserSubmitEvent:
		if !d.busy.CompareAndSwap(false, true) {
			return
		}
		go func(text string) {
			defer d.busy.Store(false)
			if coordpkg.ShouldOrchestrate(text) {
				d.runOrchestrator(text)
			} else {
				_, _ = d.core.Submit(d.ctx, d.sid, text)
			}
		}(e.Text)
	case *event.UserQuitEvent:
		d.cancel()
	}
}

func (d *tuiDriver) runOrchestrator(goal string) {
	costPub := newCostPublisher(d.bus)
	costPub.Start(d.ctx)
	runner := newRuntimeAgentRunner(d.repo, resolveProvider(), d.bus).withCost(costPub)
	o := coordpkg.NewOrchestrator(
		coordpkg.Config{MaxReworkCycles: 3, Concurrency: 1},
		&heuristicPlanner{},
		d.bus, d.bus,
		runner,
	)
	o.Verifier = mergeVerifier{}
	_ = o.Run(d.ctx, goal)
	costPub.Stop()
}
