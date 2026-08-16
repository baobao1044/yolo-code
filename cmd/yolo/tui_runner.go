// Sprint 13+ TUI integration runner. This is the default interactive path:
// the TUI renders events from the shared bus and user keystrokes publish
// user.* events that the runtime now consumes.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	cog "github.com/baobao1044/yolo-code/internal/cognitive"
	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/infra"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
	"github.com/baobao1044/yolo-code/internal/tui"
)

// runTUI starts the interactive Bubble Tea TUI wired to a real runtime.Core
// (single-agent goals) and a real coord.Orchestrator (multi-agent goals).
func runTUI(ctx context.Context) error {
	return runTUIFrontend(ctx, func(ctx context.Context, bus *event.Bus) error {
		return tui.Run(ctx, bus, bus)
	})
}

// runTUIFrontend is runTUI with the front end injected. tui.Run needs a TTY, so
// with it hardwired the composition and teardown around it — the part that
// decides whether an interactive session's memory reaches disk — had no
// reachable test at all. frontend is called with the wired bus and returns when
// the session ends; everything before and after it is the real path.
func runTUIFrontend(ctx context.Context, frontend func(context.Context, *event.Bus) error) error {
	ctx, cancel := context.WithCancel(ctx)

	repo, err := repoRoot()
	if err != nil {
		cancel()
		return err
	}

	dir, err := sessionStateDir()
	if err != nil {
		cancel()
		return err
	}

	bus, err := newBus()
	if err != nil {
		cancel()
		return err
	}

	// L12-009: bring the infrastructure layer up on the bus. infra.Start had no
	// production call site at all, so telemetry, metrics, the permission policy
	// and the shared redaction registry were dead in the shipped binary
	// (§4.7/§4.20) — every one of them is an observer of this stream.
	infraCfg := infra.DefaultConfig()
	infraCfg.Permissions.Root = repo // absolute; the auto policy confines writes to it
	// The log projector writes one DEBUG line per event to stderr, and the TUI
	// owns the alternate screen: those lines land on top of the rendered frame
	// and shred it. Raise the threshold above DEBUG here — `--event-log` is the
	// way to capture the transcript in interactive mode.
	infraCfg.Log.Level = 8 // slog.LevelError
	inf, err := infra.Start(ctx, bus, infraCfg)
	if err != nil {
		cancel()
		_ = bus.Close()
		return fmt.Errorf("start infrastructure: %w", err)
	}
	// stopInfra derives from Background() deliberately: on Ctrl-C the run ctx is
	// already cancelled and the exporter flushes would get zero budget.
	stopInfra := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = inf.Stop(stopCtx)
	}

	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   bus,
		Git:   session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(ctx, "tui", "interactive")
	if err != nil {
		cancel()
		_ = bus.Close()
		stopInfra()
		return err
	}

	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		cancel()
		_ = bus.Close()
		stopInfra()
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
		// Scope Loop Engineering + Dynamic Workflow, same as the headless and
		// coord roots. These were missing here and only here, which meant the
		// one entrypoint a human actually drives was the one whose loop never
		// consulted the scope controller or the workflow engine. It did not
		// fail: runtime.New swaps in noopScopeController/noopWorkflowEngine for
		// a nil port (core.go:137-144), so an interactive session ran with the
		// tool gate at core.go:442 permanently open and the legacy fixed phase
		// order, and looked exactly like a session where scope had nothing to
		// say. Wiring them also makes scope.*/workflow.* events observable on
		// the TUI bus, which is what the panes read.
		Scope:    newScopeAdapter(bus),
		Workflow: newWorkflowAdapter(bus),
		Cost:     newCostLedger(),
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
		mem:    deps.memory,
		sid:    sid,
		repo:   repo,
	}
	d.Start()

	defer func() {
		cancel()
		d.Stop()
		costPub.Stop()
		// Close the bus before stopping infra so the root subscriber drains the
		// queued tail (and the durability log is flushed) rather than losing it.
		_ = bus.Close()
		reportDropped(bus)
		// After bus.Close and before anything else: the Store's Close is what
		// flushes what the session learned (Flush writes preference/knowledge/
		// conversations/exec), and runTUI builds its own deps, so — exactly as
		// runHeadlessDeps owns the Close on the path where it builds them — runTUI
		// owns this one. Nothing called it, so every insight and transcript an
		// interactive session recorded died with the process. It has to come after
		// bus.Close because a listening Store's Close blocks on the listener drain,
		// which only ends when the bus closes its subscriber channel; it comes
		// before stopInfra because Close also joins the listener's offloaded
		// persists (Store.bg) and the user's own data should reach disk without
		// waiting on a telemetry flush that has its own five-second budget.
		if deps.memory != nil {
			if err := deps.memory.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "yolo: memory: %v\n", err)
			}
		}
		stopInfra()
		// Last, because it is a delete: defaultHeadlessDeps built a shadow tree
		// under os.MkdirTemp and runTUI is the only owner, so an interactive
		// session left one behind every time. Everything that reads it back — the
		// patch engine and the restorer, driven only by the Core the driver
		// submits to — is quiet by now: d.Stop joined the driver's event loop and
		// cancel() unwound any submission it had started.
		_ = deps.snap.close()
	}()

	return frontend(ctx, bus)
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
	mem    *memory.Store
	sid    session.ID
	repo   string
	busy   atomic.Bool
	stop   chan struct{}
	done   chan struct{}
	events <-chan event.Envelope // subscribed by Start, consumed by run — see Start
}

// Start subscribes and then launches the loop, in that order.
//
// Subscribe used to be the first line of run, which meant Start returned before
// the driver was listening and every user.* published in that window went
// nowhere — the bus has no subscriber to hand it to, so it is not queued, not
// counted as dropped, and not logged. Interactively this never bit: the window
// is a goroutine scheduling delay and the earliest possible event needs a human
// to finish typing. It bites anything that drives the composition root
// programmatically, which is how it was found — a test published a slash
// command the instant runTUIFrontend handed it the bus, and the command
// vanished with no error anywhere.
//
// Subscribing here makes Start mean what every call site already assumes it
// means: on return, events are being received.
func (d *tuiDriver) Start() {
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	d.events = d.bus.Subscribe(event.Topic("user.>"))
	go d.run()
}

func (d *tuiDriver) Stop() {
	close(d.stop)
	<-d.done
}

func (d *tuiDriver) run() {
	defer close(d.done)
	ch := d.events
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
		// Slash command that needs the runtime (model/provider/status/pref).
		// Guard: never swap provider mid-task — Think reads it in the drive
		// goroutine. If busy, tell the user to finish/cancel first.
		//
		// /pref is exempt. It publishes a preference event the memory listener
		// records and touches nothing the drive loop reads, so refusing it
		// mid-task would deny a harmless write — and deny it with a message
		// about switching models, which does not describe what was asked. It is
		// also the command a user is most likely to reach for *during* a run,
		// because the reason to record a preference is usually watching the
		// agent do the thing you want it to stop doing.
		if e.Command != "pref" && d.busy.Load() {
			d.respond("task running — finish or cancel before switching model/provider")
			return
		}
		d.handleCommand(e.Command, e.Args)
	// user.approve / user.reject are deliberately NOT handled here: the runtime's
	// own userEventLoop drives the FSM side (WAIT_USER → EXECUTE) and
	// watchApprovalDecisions — installed on this engine by defaultHeadlessDeps —
	// drives the exec side (Engine.ResolveApproval). A third resolver here would
	// be a second source of truth for the same verdict.
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
	case "pref":
		d.cmdPref(args)
	default:
		d.respond("unknown command: " + cmd)
	}
}

// cmdPref reads or records a user preference (§11.5.2).
//
// This is the producer side of user.preference, which until now had a consumer
// and nothing else: memory/listener.go has had the arm that writes the
// Preference store since L10-002, cmd/yolo/adapters.go:203 feeds that store
// into every context build, context/compress.go has a KindPreferences case, and
// the prompt pipeline renders the parts into the system prompt. The whole read
// side was wired end to end and permanently empty, because nothing anywhere
// constructed the event that fills it.
//
// The write goes through the bus rather than calling d.mem.Preferences().Set
// directly. Reaching around the listener would give the Preference store a
// second writer that orders independently of the memory event stream, and
// would skip the memory.update the listener publishes — the flash that tells
// the user a store just learned something. The read goes direct: a query has no
// ordering to preserve and no listener arm to route through.
func (d *tuiDriver) cmdPref(args string) {
	key, value, _ := strings.Cut(args, " ")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)

	if key == "" {
		d.respond(d.prefList())
		return
	}
	if value == "" {
		// A bare key is a read, not a delete. PreferenceStore has no Delete,
		// so inventing one here would mean inventing it in the store too —
		// and silently interpreting "/pref style" as "forget style" would be
		// the worst possible reading of an ambiguous line.
		d.respond(d.prefGet(key))
		return
	}
	if err := d.bus.Publish(d.ctx, &event.UserPreferenceEvent{Key: key, Value: value}); err != nil {
		d.respond("preference: " + key + " — " + err.Error())
		return
	}
	// Deliberately not phrased as "saved". The listener does the write on its
	// own goroutine and reports a failure as an error event; claiming durability
	// here would be asserting something this function has not observed.
	d.respond("preference: " + key + " = " + value)
}

// prefList renders every recorded preference, or says plainly that there are
// none. An empty store is the normal state on a fresh checkout and reporting it
// as such is more useful than an empty response the user has to interpret.
func (d *tuiDriver) prefList() string {
	if d.mem == nil {
		return "usage: /pref <key> <value>  (no memory store wired, so nothing to list)"
	}
	all, err := d.mem.Preferences().All(d.ctx)
	if err != nil {
		return "preferences: " + err.Error()
	}
	if len(all) == 0 {
		return "no preferences recorded — set one with /pref <key> <value>"
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys) // same deterministic order the context build uses
	var b strings.Builder
	b.WriteString("preferences:")
	for _, k := range keys {
		b.WriteString("\n  " + k + " = " + all[k])
	}
	return b.String()
}

// prefGet reads one key. A miss is reported as a miss rather than as an empty
// value: "style = " reads like a preference that was set to nothing.
func (d *tuiDriver) prefGet(key string) string {
	if d.mem == nil {
		return "usage: /pref <key> <value>  (no memory store wired)"
	}
	v, err := d.mem.Preferences().Get(d.ctx, key)
	if err != nil {
		return "no preference named " + key + " — set it with /pref " + key + " <value>"
	}
	return "preference: " + key + " = " + v
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
	// ResolveProviderErr, not ResolveProvider: the silent form hands back an
	// unconfiguredProvider whose failure only surfaces on the next Stream, long
	// after the user has been told the swap worked.
	prov, err := cog.ResolveProviderErr()
	if err != nil {
		d.respond("model: " + name + " — " + err.Error())
		return
	}
	d.cog.SetProvider(prov)
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
	// Set env so a later /model or restart stays consistent. The model resets to
	// the new provider's default rather than carrying over: model names are
	// provider-scoped namespaces, so `/model gpt-4o` followed by `/provider groq`
	// used to ask api.groq.com for "gpt-4o" — a name it has never heard of, and
	// on an aggregating proxy a *different* model billed under the old name. The
	// user's own choice is not lost, it is re-stated: `/model <name>` after the
	// switch sets it against the provider it was meant for.
	_ = os.Setenv("YOLO_PROVIDER", preset.Name)
	_ = os.Setenv("YOLO_BASE_URL", preset.BaseURL)
	_ = os.Setenv("YOLO_MODEL", preset.DefaultModel)
	if d.cog == nil {
		d.respond("provider: " + preset.Name + " (no runtime to swap)")
		return
	}
	// One key policy for the whole binary. The resolver that used to live here
	// was a copy that fell back KeyEnv → YOLO_API_KEY → OPENAI_API_KEY, so
	// `/provider groq` put the user's OpenAI key in an Authorization header to
	// api.groq.com. cognitive reads the preset's own KeyEnv and nothing else.
	prov, err := cog.ResolveProviderErr()
	if err != nil {
		d.respond("provider: " + preset.Name + " — " + err.Error())
		return
	}
	d.cog.SetProvider(prov)
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

// sessionStateDir returns the durable per-user directory the TUI's session and
// task transcripts live in, creating it if needed. It used to be an
// os.MkdirTemp that the deferred os.RemoveAll deleted on exit — the runner
// flushed every session to disk and then threw the disk away, so nothing could
// ever be resumed.
//
// YOLO_SESSION_DIR overrides the location, mirroring YOLO_MEMORY_DIR. Without
// it the only path under test is the developer's real config directory, so the
// function was untestable and any test would have polluted their sessions.
func sessionStateDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv("YOLO_SESSION_DIR")); d != "" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "yolo-code", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
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
	// resolveProvider now fails loudly rather than handing back a stub: say so
	// in the chat pane instead of spawning sub-agents that each error out.
	provider, err := resolveProvider()
	if err != nil {
		d.respond(err.Error())
		return
	}
	// No costPublisher is started here. runTUI already runs one for the whole
	// session, and the publisher meters the bus, not the caller: a second one on
	// the same bus gets its own tool.result subscription and publishes a second
	// cost.incurred for every tool call, so every multi-agent goal in the TUI
	// double-counted itself in the cost rail and the transcript.
	runner := newRuntimeAgentRunner(d.repo, provider, d.bus)
	// A runner is built per goal, and each one allocates its own shadow tree —
	// so a session that ran three multi-agent goals left three directories.
	// Deferred rather than closed right after Run: the early return paths below
	// would skip it, and Run itself must finish first (the patch engine reads the
	// tree back to roll a todo out). Run returning is enough — its terminal path
	// joins every inflight agent turn (coord.Orchestrator.quiesceAgents) so the
	// caller can tear the runner down immediately after.
	defer func() { _ = runner.close() }()
	o := coordpkg.NewOrchestrator(
		coordpkg.Config{MaxReworkCycles: 3, Concurrency: 1},
		&heuristicPlanner{},
		d.bus, d.bus,
		runner,
	)
	o.Verifier = mergeVerifier{}
	d.reportPlanOutcome(o.Run(d.ctx, goal))
}

// reportPlanOutcome puts a terminated multi-agent run's error in the chat pane.
// It was discarded, and it is the only thing that separates "the plan failed"
// from "the plan is still running" for a TUI user: the board simply stopped
// moving and nothing ever said why.
//
// A cancelled run is reported as a cancel, not a failure. internal/coord goes
// out of its way not to tell that lie in its return value — Run answers a
// cancelled run with ctx.Err() and deliberately never ErrPlanFailed — and
// repeating it one layer up would undo that. The test is d.ctx.Err() rather
// than errors.Is(err, context.Canceled): the driver's context is the one Run
// watched, so it says exactly what coord's own termCanceled says, while the
// returned error is a join that can also carry the context.Canceled of an agent
// turn the terminal path cancelled on a run nobody cancelled.
//
// The notice goes out on a background context for the reason stopInfra does:
// the cancel path arrives here with d.ctx already dead, and publishing through
// it would drop the one message that explains why the run stopped. The budget
// bounds a slow subscriber; a closed bus returns immediately.
func (d *tuiDriver) reportPlanOutcome(err error) {
	if err == nil {
		return
	}
	text := "plan failed: " + err.Error()
	if d.ctx.Err() != nil {
		text = "plan cancelled"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.bus.Publish(ctx, &event.CommandResponseEvent{Text: text})
}
