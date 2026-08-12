// Sprint 13+ TUI integration runner. This is the default interactive path:
// the TUI renders events from the shared bus and user keystrokes publish
// user.* events that the runtime now consumes.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	cog "github.com/baobao1044/yolo-code/internal/cognitive"
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
		cog:    deps.cogCore,
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
	cog    *cog.Core // for slash-command provider swap (SetProvider)
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
	case *event.UserCommandEvent:
		// Slash command that needs the runtime (model/provider/status).
		// Guard: never swap provider mid-task — Think reads it in the drive
		// goroutine. If busy, tell the user to finish/cancel first.
		if d.busy.Load() {
			d.respond("task running — finish or cancel before switching model/provider")
			return
		}
		d.handleCommand(e.Command, e.Args)
	case *event.UserQuitEvent:
		d.cancel()
	}
}

// handleCommand performs the runtime action for a slash command and publishes
// a CommandResponseEvent with the outcome text.
func (d *tuiDriver) handleCommand(cmd, args string) {
	switch cmd {
	case "model":
		d.cmdModel(args)
	case "provider":
		d.cmdProvider(args)
	case "status":
		d.cmdStatus()
	default:
		d.respond("unknown command: " + cmd)
	}
}

// cmdModel swaps the model name, rebuilds the provider from env, and swaps it
// into the cognitive Core. /model <name> sets YOLO_MODEL then rebuilds (base-url
// + api-key stay; only the model changes).
func (d *tuiDriver) cmdModel(name string) {
	if name == "" {
		d.respond("usage: /model <name>")
		return
	}
	_ = os.Setenv("YOLO_MODEL", name)
	if d.cog == nil {
		d.respond("model: " + name + " (no runtime to swap)")
		return
	}
	d.cog.SetProvider(cog.ResolveProvider())
	d.respond("model: " + name)
}

// cmdProvider swaps the LLM provider preset. /provider <name> looks up the
// preset, sets the env vars (YOLO_PROVIDER, YOLO_BASE_URL, YOLO_MODEL), builds
// the provider from preset + key, and swaps it. /provider (no arg) lists all
// presets so the user can pick.
func (d *tuiDriver) cmdProvider(name string) {
	if name == "" {
		var sb strings.Builder
		sb.WriteString("providers:")
		for _, p := range cog.ListProviders() {
			sb.WriteString("\n  ")
			sb.WriteString(p.Name)
			sb.WriteString(" — ")
			sb.WriteString(p.Description)
		}
		d.respond(sb.String())
		return
	}
	preset, ok := cog.LookupProvider(name)
	if !ok {
		d.respond("unknown provider: " + name + " (try /provider to list)")
		return
	}
	// Set env so a later /model or restart stays consistent.
	_ = os.Setenv("YOLO_PROVIDER", preset.Name)
	_ = os.Setenv("YOLO_BASE_URL", preset.BaseURL)
	if os.Getenv("YOLO_MODEL") == "" {
		_ = os.Setenv("YOLO_MODEL", preset.DefaultModel)
	}
	if d.cog == nil {
		d.respond("provider: " + preset.Name + " (no runtime to swap)")
		return
	}
	key := resolveProviderKey(preset)
	if preset.NeedsKey && key == "" {
		d.respond("provider: " + preset.Name + " — needs API key (" + preset.KeyEnv + " or YOLO_API_KEY)")
		return
	}
	d.cog.SetProvider(cog.ProviderFromPreset(preset, key))
	d.respond(fmt.Sprintf("provider: %s (%s)", preset.Name, preset.DefaultModel))
}

// cmdStatus builds a status string: current model, provider, and accumulated
// cost. Reads env (model) + YOLO_PROVIDER (provider name) — the runtime itself
// doesn't expose a query for the active model, so env is the source of truth
// (set by /model and /provider).
func (d *tuiDriver) cmdStatus() {
	model := os.Getenv("YOLO_MODEL")
	if model == "" {
		model = "gpt-4o"
	}
	provider := os.Getenv("YOLO_PROVIDER")
	if provider == "" {
		provider = "openai (default)"
	}
	d.respond(fmt.Sprintf("status: model=%s · provider=%s · theme=%s", model, provider, tuiCurrentThemeName()))
}

// respond publishes a CommandResponseEvent the TUI folds into the chat pane.
func (d *tuiDriver) respond(text string) {
	_ = d.bus.Publish(d.ctx, &event.CommandResponseEvent{Text: text})
}

// resolveProviderKey resolves the API key for a preset: KeyEnv first, then
// YOLO_API_KEY, then OPENAI_API_KEY. Local providers (NeedsKey=false) get "".
func resolveProviderKey(p cog.ProviderPreset) string {
	if !p.NeedsKey {
		return ""
	}
	if p.KeyEnv != "" {
		if k := os.Getenv(p.KeyEnv); k != "" {
			return k
		}
	}
	if k := os.Getenv("YOLO_API_KEY"); k != "" {
		return k
	}
	if k := os.Getenv("OPENAI_API_KEY"); k != "" {
		return k
	}
	return ""
}

// tuiCurrentThemeName reads YOLO_THEME from the environment. Mirrors the TUI's
// currentThemeName() — the driver lives in cmd/yolo and can't import the TUI
// for a trivial env read (import matrix), so it reads env directly.
func tuiCurrentThemeName() string {
	t := strings.ToLower(strings.TrimSpace(os.Getenv("YOLO_THEME")))
	switch t {
	case "dark", "light", "contrast", "mono":
		return t
	}
	return "dark"
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
