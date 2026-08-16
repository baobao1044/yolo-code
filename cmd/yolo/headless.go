// The headless runner (File 14 §14.10) is the Sprint 1 demo path: pipe a prompt
// to `yolo --headless` and it prints one JSON line per event to stdout. This is
// the cheapest demo (no TUI) and the one golden-transcript tests (File 15
// §15.15.3) assert against — so it must be deterministic across runs (S5).
//
// Determinism: each line carries the deterministic projection of an envelope
// (seq, type, task, and the event's payload), omitting the non-deterministic
// timestamp. Two runs with the same input produce byte-identical output.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	cog "github.com/baobao1044/yolo-code/internal/cognitive"
	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	execpkg "github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/infra"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// runHeadless runs one headless turn against a fresh in-memory store, reading
// the prompt from stdin and writing one JSON line per event to a buffer whose
// bytes it returns. seed makes timestamps deterministic when nonzero (zero
// uses real time, but the projection omits time anyway).
func runHeadless(stdin io.Reader, seed int64) (string, error) {
	return runHeadlessCtx(context.Background(), stdin, seed)
}

// headlessDeps lets a caller override the runtime's port wiring (File 04 §4.6).
// Sprint 1's runHeadless leaves these nil → the runtime fills noop stubs (the
// single-turn demo path). L5-003 injects the real Context Engine + Prompt
// Compiler + an asserting cognitive core so the headless transcript reflects a
// real, compiled prompt carrying real file contents. Repo is the working-tree
// root the Context Engine reads from; Open is the set of open files to gather.
// L10-006 adds Memory + Bus: when both are set, the composition root wires a
// memoryStoreAdapter behind runtime.MemoryStore (publishing a learning event on
// the direct-answer path, §11.2). The caller owns the Store + Bus (the test
// builds them so the memory listener is the event subscriber); runHeadlessDeps
// uses deps.bus when provided and falls back to its own bus otherwise.
//
// L12-009 adds Infra: when set, the caller already called infra.Start on the
// shared bus (so it owns the aggregate for post-run assertions). runHeadlessDeps
// then owns the Stop — it runs after the bus closes in the close chain, so the
// root subscriber range ends → done closes → Stop's wait returns promptly (no
// goroutine leak). Infra is a pure observer (§13.1.2): it adds no events, so the
// transcript stays byte-identical to the unwired run.
type headlessDeps struct {
	context  runtime.ContextBuilder
	prompt   runtime.PromptCompiler
	cog      runtime.CognitiveCore
	cogCore  *cog.Core // raw cognitive.Core for TUI slash-command provider swap (nil in headless)
	exec     runtime.Executor
	verify   runtime.Verifier
	patcher  runtime.Patcher
	restorer runtime.Restorer
	repo     string
	open     []string
	window   int
	memory   *memory.Store
	memDir   string        // durable root the Store was opened on; non-empty means runHeadlessDeps owns Close (never a delete)
	sessions session.Store // nil → the per-run FileStore below; set to reach the error paths a real disk only produces when it is full or read-only
	snap     *shadowSnap   // shadow tree behind patcher+restorer; non-nil means the caller that built these deps owns close (a delete — see shadowSnap.close)
	bus      *event.Bus
	infra    *infra.Infra // L12-009: caller Start'd it on deps.bus; runHeadlessDeps owns the Stop.
}

// runHeadlessDeps is the injectable form: real L4/L5 ports when deps is
// non-nil; the Sprint 1 stub path otherwise. The shared core drives the task
// and prints the transcript. runHeadless/runHeadlessCtx delegate here so
// there's one drive/print path, not two.
func runHeadlessDeps(ctx context.Context, stdin io.Reader, seed int64, deps *headlessDeps) (string, error) {
	prompt := readPrompt(stdin)

	// Fresh per-run store keeps the transcript reproducible: session and task
	// ids start at s_1/t_1 every time (S5 byte-identical). That is also why the
	// durable-root fix applied to the memory and session stores is wrong here:
	// this directory is genuinely disposable, so it has to be removed rather
	// than kept — and nothing removed it, leaving 681 yolo-headless dirs on one
	// dev box between `--headless` runs and the test suite.
	dir, err := os.MkdirTemp("", "yolo-headless")
	if err != nil {
		return "", err
	}
	// Registered first so it runs last (defers are LIFO). session.Manager.Resume
	// re-reads the session JSON off disk mid-run, so the store has to outlive the
	// drive loop, the transcript drain and every close registered below.
	defer func() { _ = os.RemoveAll(dir) }()
	var store session.Store = session.NewFileStore(dir)
	if deps != nil && deps.sessions != nil {
		store = deps.sessions
	}
	// L10-006: when the caller injects a bus (the memory-wiring test does, so
	// the memory listener it owns is the event subscriber), reuse it instead of
	// making a fresh one — the runtime, the memory listener, and the headless
	// transcript subscriber must all share one bus. The caller closes it.
	// Otherwise make our own and close it on exit (Sprint 1 path).
	var bus *event.Bus
	busOwned := true
	if deps != nil && deps.bus != nil {
		bus = deps.bus
		busOwned = false
	} else {
		// newBus honours YOLO_EVENT_LOG (--event-log): the durability log had no
		// production call site at all before this, so a crashed run left nothing
		// to replay. Unset still yields the in-memory bus the tests rely on.
		b, err := newBus()
		if err != nil {
			return "", err
		}
		bus = b
	}

	// L12: start the infrastructure layer (telemetry, metrics, permissions,
	// secret redaction). Nothing on the real startup path used to call
	// infra.Start, so the whole layer was dead in the shipped binary. A caller
	// that already Start'd its own aggregate on deps.bus keeps it (one Start per
	// bus — a second one would double-observe the same stream).
	inf := (*infra.Infra)(nil)
	if deps != nil {
		inf = deps.infra
	}
	if inf == nil {
		root, err := repoRoot()
		if err != nil {
			return "", err
		}
		infraCfg := infra.DefaultConfig()
		infraCfg.Permissions.Root = root // absolute; empty would deny every real write
		started, err := infra.Start(ctx, bus, infraCfg)
		if err != nil {
			return "", fmt.Errorf("start infrastructure: %w", err)
		}
		inf = started
	}
	// One close chain for all three — bus, then memory, then infra — registered
	// once, here, rather than as three defers spread down the function. The
	// order between them is a correctness constraint, not a preference, and a
	// separate defer registered later runs *earlier* (LIFO): the memory Close
	// used to sit on its own below and deadlocked on every early return between
	// the two. Assigning memStore into a variable this closure already captures
	// is what makes the ordering hold for a return statement nobody has written
	// yet.
	//
	//  1. The bus first, so the queued tail drains into the observers and the
	//     subscriber channels close. Unconditional (Bus.Close is idempotent —
	//     bus.go CAS-guards it, and the success path below already closes an
	//     injected bus at the end of the run): an injected bus nominally belongs
	//     to the caller, but memStore.Close cannot complete until it is closed,
	//     so "the caller will get to it" is not a shutdown order that terminates.
	//     reportDropped stays conditional — that diagnostic is about our own bus.
	//  2. Memory next. Store.Close flushes the sub-stores and then waits on the
	//     listener's drain goroutine, which exits when its subscription channel
	//     closes — i.e. only after step 1. Closing memory first is the deadlock;
	//     this is the same ordering runTUI settled on. It is also before
	//     inf.Stop, so what the run learned reaches disk without queueing behind
	//     a telemetry flush, and before the deferred shadow-tree delete (which
	//     is registered later, so it runs before this and can only observe
	//     listener I/O that has already been joined).
	//  3. Infra last, flushing the observers that just received the tail.
	//     stopCtx derives from Background deliberately — on Ctrl+C the run ctx is
	//     already cancelled and the flushes would get zero budget. inf.Stop is
	//     unconditional because we may have Start'd the aggregate on an injected
	//     bus ourselves.
	var memStore *memory.Store
	defer func() {
		_ = bus.Close()
		if busOwned {
			reportDropped(bus)
		}
		if memStore != nil {
			_ = memStore.Close()
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = inf.Stop(stopCtx)
	}()

	// Headless has no human at the keyboard, so with the approval gate on the
	// runtime would park a risky tool call in WAIT_USER forever. Answer for the
	// absent human — by refusing. A no-op unless a park actually happens, so it
	// costs nothing on the auto-approved or tool-free paths.
	rejectRiskyWithoutHuman(ctx, bus)

	// L10-006: when the default path opened a memory Store (memDir set), own its
	// lifecycle here — by handing it to the close chain above rather than by
	// registering another defer, which is what made this a hang. The directory
	// itself is durable and deliberately NOT removed: deleting it here used to
	// throw away everything the run had just learned. A test that injects its
	// own memory (memDir empty) owns the Close.
	//
	// This covers the callers that build defaultHeadlessDeps themselves and pass
	// the result in (runTUI, the coord runner). The `deps == nil` case — a plain
	// `yolo --headless`, which builds its deps below — has its own twin of this
	// handover at the point where those deps exist; both feed the same memStore.
	if deps != nil && deps.memory != nil && deps.memDir != "" {
		memStore = deps.memory
	}

	smgr := session.New(session.Deps{
		Store: store, Bus: bus, Git: session.NewInMemCheckpointer(),
	})
	sid, err := smgr.OpenSession(ctx, "headless", "demo")
	if err != nil {
		return "", err
	}

	d := runtime.Deps{Bus: bus, Session: smgr}
	// Hard cost caps, when the operator configured any. nil leaves the drive
	// loop uncapped via the runtime's noop stub, which is the historic
	// behaviour; the point of wiring it is that YOLO_MAX_COST / YOLO_MAX_TIME
	// now stop a run instead of only being read by a warning printer.
	d.Cost = newCostLedger()
	// Scope Loop Engineering + Dynamic Workflow: always wire both adapters so
	// the drive loop consults the scope controller (VERIFY arm) and the workflow
	// engine (PLAN arm). The buses are nil-safe; tests that inject a fresh bus
	// share it with these adapters so scope./workflow. events are observable.
	d.Scope = newScopeAdapter(bus)
	d.Workflow = newWorkflowAdapter(bus)
	if deps != nil {
		d.Context = deps.context
		d.Prompt = deps.prompt
		d.Cognitive = deps.cog
		d.Exec = deps.exec
		d.Verify = deps.verify
		d.Patch = deps.patcher
		d.Restore = deps.restorer
		// L10-006: bridge memory into the runtime's MemoryStore port. The
		// adapter publishes a learning event the memory listener reacts to (it
		// does NOT mutate a sub-store directly — §11.2). Only wire it when the
		// caller injected a Store; the Sprint 1/2 stub path leaves Memory nil
		// and the runtime's nil-guard skips the Update call.
		if deps.memory != nil {
			d.Memory = memoryStoreAdapter{store: deps.memory, bus: bus}
		}
	} else {
		// Sprint 12 INT-008: the default --headless path uses the real
		// context/prompt/cognitive/exec/verify/patch/restorer adapters wired to
		// the current working directory. Tests and the TUI deferred-wiring
		// path still inject headlessDeps explicitly.
		defaultDeps, err := defaultHeadlessDeps(bus)
		if err != nil {
			return "", err
		}
		// We built these deps, so we own the shadow tree they carry. Deferred
		// rather than removed at the end of the function: the patch engine and
		// the restorer read it back throughout the drive loop, and the early
		// returns below would leak it. A caller that injects its own deps owns
		// its own snap (runTUI, the coord runner).
		defer func() { _ = defaultDeps.snap.close() }()
		d.Context = defaultDeps.context
		d.Prompt = defaultDeps.prompt
		d.Cognitive = defaultDeps.cog
		d.Exec = defaultDeps.exec
		d.Verify = defaultDeps.verify
		d.Patch = defaultDeps.patcher
		d.Restore = defaultDeps.restorer
		// The twin of the memStore handover above. It was missing here, and the
		// asymmetry is the whole bug: every test and every other caller injects
		// deps and so registers its Store for close, while the one path that
		// does NOT — a real `yolo --headless` run — opened a Store nothing ever
		// closed. Nothing then called Flush, so what survived a run was whatever
		// the listener goroutine happened to finish first.
		//
		// d.Memory is deliberately NOT wired to match the injected branch: that
		// port's adapter fabricates a task.completed the runtime publishes for
		// real two lines later (runtime/core.go:299), so wiring it here would
		// duplicate a lifecycle event rather than learn anything. Memory still
		// persists this run — off the genuine task.completed, same as always.
		if defaultDeps.memory != nil && defaultDeps.memDir != "" {
			memStore = defaultDeps.memory
		}
	}
	core := runtime.New(d)

	// Subscribe to the root wildcard BEFORE driving so no event is missed.
	ch := bus.Subscribe(event.Topic(">"))

	var out strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		enc := json.NewEncoder(&out)
		seq := uint64(0) // normalized: the Nth event in the transcript (not bus Seq)
		for env := range ch {
			// memory.update is observational telemetry from the L10 listener
			// goroutine; its interleaving with the spine is non-deterministic
			// (the listener is a separate goroutine that races with the drive
			// loop). Excluding it keeps the headless transcript byte-identical
			// across runs (S5) — the transcript pins the agent's decision spine,
			// not memory telemetry. The memory listener still learns (the TUI
			// sees memory.update via its own subscriber).
			if env.Evt.Type() == "memory.update" {
				continue
			}
			seq++
			proj := projectEnvelope(env)
			proj.Seq = seq // normalize: 1, 2, 3, … independent of bus-assigned seq
			_ = enc.Encode(proj)
		}
	}()

	// Drive the task; the drive loop runs on this goroutine (MVP inline).
	submitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-submitCtx.Done():
		}
	}()
	_, _ = core.Submit(submitCtx, sid, prompt)

	// Close the bus so the subscriber drain ends and the transcript is flushed.
	_ = bus.Close()
	wg.Wait()
	// L12-009: with Infra wired, the root subscriber's range ended (the bus just
	// closed) so done is closed and Stop's wait returns promptly — flush the
	// observers. Bound by the run's ctx so a misbehaving flush can't hang the
	// headless exit; the stubs are no-ops so this is effectively free. Skipped
	// when no Infra is wired (the Sprint 1 path).
	if deps != nil && deps.infra != nil {
		_ = deps.infra.Stop(ctx)
	}
	return out.String(), nil
}

// runHeadlessCtx is the context-aware form: canceling ctx cancels the task
// mid-run (Ctrl+C path). It delegates to runHeadlessDeps with no injected
// ports (the Sprint 1 stub path).
func runHeadlessCtx(ctx context.Context, stdin io.Reader, seed int64) (string, error) {
	return runHeadlessDeps(ctx, stdin, seed, nil)
}

// readPrompt trims whitespace/newlines from the first line of stdin; that line
// becomes the task goal.
func readPrompt(stdin io.Reader) string {
	r := bufio.NewReader(stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// cannedAnswer is the stubbed cognitive core's reply. In Sprint 1 it echoes the
// goal so the transcript visibly carries the user's input end-to-end.
func cannedAnswer(goal string) string {
	if goal == "" {
		return "hello"
	}
	return goal
}

// projectEnvelope renders the deterministic projection of an envelope: the
// bus-assigned seq, the event type, the causal task id, and the event payload
// as JSON. The timestamp is deliberately omitted so two runs of the same input
// produce byte-identical output (S5).
type projection struct {
	Seq  uint64          `json:"seq"`
	Type string          `json:"type"`
	Task string          `json:"task,omitempty"`
	Evt  json.RawMessage `json:"evt"`
}

func projectEnvelope(env event.Envelope) projection {
	payload, _ := json.Marshal(env.Evt)
	return projection{
		Seq:  env.Seq,
		Type: string(env.Evt.Type()),
		Task: string(env.Evt.CausalID()),
		Evt:  payload,
	}
}

// defaultHeadlessDeps wires the real adapters for a production run (Sprint 12
// INT-008). It is the ONLY place the production port graph is built: both
// `--headless` (via runHeadlessDeps) and the interactive TUI (runTUI) call it,
// so a change here — notably the HITL approval gate below — applies to both.
// The repo root comes from repoRoot() (--repo / YOLO_REPO_ROOT, else the
// working directory) and the provider from resolveProvider(), which fails fast
// rather than pretending a stub is a model.
func defaultHeadlessDeps(bus *event.Bus) (*headlessDeps, error) {
	repo, err := repoRoot()
	if err != nil {
		return nil, err
	}

	sandbox := execpkg.NewSandbox(repo, repo)
	reg := new(execpkg.Registry)
	reg.Register(execpkg.NewBash(sandbox))
	reg.Register(execpkg.NewRead(sandbox))
	reg.Register(execpkg.NewListFiles(sandbox))
	reg.Register(execpkg.NewEditFile(sandbox))
	reg.Register(execpkg.NewGrep(sandbox))
	execEng := newExecEngine(reg, sandbox, bus)
	// The exec gate publishes approval.request and then blocks on
	// ResolveApproval(id) — which had no caller anywhere outside exec's own
	// tests, so the gate could only ever deadlock. Bridge the bus to it.
	watchApprovalDecisions(execEng, bus)

	snap, err := newShadowSnap(repo)
	if err != nil {
		return nil, err
	}
	cp := newShadowCheckpointer(snap)
	patchEng := newPatchEngine(sandbox, cp, bus)
	// reg is passed a second time, to the adapter rather than the engine: it is
	// what lets unrouted() tell "no such tool" from "a harmless tool". Without
	// it the fail-closed gate in exec_adapter.go is inert, because it reports
	// false for every name when registry is nil.
	execAd := &execAdapter{engine: execEng, patcher: patchEng, registry: reg}
	verifyAd := &verifyAdapter{engine: newVerifyEngine(sandbox)}
	restorer := newShadowRestorer(snap)

	// L10-006: open the memory Store wired to the shared bus so its listener is
	// the event subscriber (the only sub-store writer, §11.2). The LexicalStore
	// (Store.Semantic()) gets the sandbox-confined FS so Reindex can read a
	// path's new content on patch.applied. Cold-start indexing runs best-effort
	// next (Phase C); a nil FS (sandbox absent) leaves Reindex a no-op but the
	// store still answers Preferences/Project.
	// Every failure from here on has to unwind the shadow tree: newShadowSnap
	// already made a directory, and returning an error without it is the same
	// leak as never closing at all — a misconfigured provider left one behind on
	// every startup attempt.
	memDir, err := memoryRoot()
	if err != nil {
		_ = snap.close()
		return nil, err
	}
	// Redactor: memory's listener is a writer to disk (knowledge.json,
	// conversations/<sid>.json) and memory cannot import infra, so the root
	// injects the process-wide registry here — the same one behind exec's
	// normalizer, the log line, the Sentry event and the durability log.
	memStore, err := memory.Open(memory.Deps{
		Root:     memDir,
		Bus:      bus,
		FS:       newMemoryFS(sandbox),
		Redactor: mustRedactor(),
	})
	if err != nil {
		_ = snap.close()
		return nil, err
	}
	// memory.Open no longer aborts on a corrupt file — it quarantines it to
	// <name>.corrupt and carries on. Silently losing a user's preferences is not
	// acceptable, so every quarantine gets a line on stderr.
	for _, w := range memStore.Warnings() {
		fmt.Fprintf(os.Stderr, "yolo: memory: %v\n", w)
	}
	// Cold-start: index the repo so the first turn already has RAG signal
	// (§11.7.5). Best-effort — a walk error or empty repo leaves the store
	// empty (Retrieve returns nil, the prompt just omits the <rag> group). The
	// timeout bounds a huge repo; the walk is deterministic so two runs of the
	// same tree produce byte-identical indexes (S5).
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = memory.IndexRepo(indexCtx, memStore.Semantic(), repo)
	indexCancel()

	provider, err := resolveProvider()
	if err != nil {
		_ = memStore.Close()
		_ = snap.close()
		return nil, err
	}
	cogCore, cogAd := newCognitiveCore(provider, bus)
	return &headlessDeps{
		// Tools makes the AVAILABLE TOOLS block the context engine renders read
		// from the set the provider is actually offered, instead of a fourth
		// hand-maintained copy of the same names. Only the composition root may
		// wire this: L4 importing L6 would invert the layering (§15.13).
		context:  contextAdapter{eng: econtext.New(econtext.Deps{Bus: bus, Repo: repo, Memory: contextMemoryAdapter{store: memStore}, Tools: cog.DefaultTools()})},
		prompt:   promptAdapter{comp: prompt.New(nil, bus)},
		cog:      cogAd,
		cogCore:  cogCore,
		exec:     execAd,
		verify:   verifyAd,
		patcher:  &patchAdapter{engine: patchEng},
		restorer: restorer,
		repo:     repo,
		memory:   memStore,
		memDir:   memDir,
		snap:     snap,
		bus:      bus,
	}, nil
}

// resolveProvider returns the provider selected by YOLO_PROVIDER (preset
// registry) or YOLO_API_KEY (env-only), or the deterministic stub when the
// operator opted into it (--stub / YOLO_STUB=1). Nothing is configured →
// cognitive.ErrNoProvider, and the caller aborts at startup: a misconfigured
// run should fail while the user is still looking at the command line, not at
// the first Stream call halfway through a task.
// It is also where the run's credential becomes known, so it is where that
// credential is added to the redaction registry (registerResolvedAPIKey).
func resolveProvider() (cog.Provider, error) {
	registerResolvedAPIKey()
	return cog.ResolveProviderErr()
}

// mustRedactor returns the process-wide redaction registry, refusing to
// continue without one.
//
// The nil check is not ceremony. Every seam that takes a Redactor tolerates
// nil by design — exec falls back to four local Sprint-4 patterns, event's log
// and memory's listener pass text through — because those packages sit below
// infra in the import matrix (§15.15.2) and must work without it. That
// tolerance is correct for them and wrong for us: a composition root that
// hands a boundary a nil redactor produces a system that looks wired and
// redacts nothing. Worse, a typed-nil *infra.Secrets is non-nil as an
// interface and every method passes through, so the failure is invisible.
// DefaultRedactor never returns nil, so this can only fire on a wiring
// mistake — at process start, before any tool has run.
func mustRedactor() *infra.Secrets {
	r := infra.DefaultRedactor()
	if r == nil {
		panic("yolo: composition root: infra.DefaultRedactor() returned nil; refusing to run with no secret redaction")
	}
	return r
}

// init wires the redaction registry into the event package's durability log —
// the sink every event reaches, and the one boundary that was writing
// PatchAppliedEvent.Diff and ErrorEvent.Msg to YOLO_EVENT_LOG in the clear.
//
// It runs at package init rather than next to a bus construction because there
// are three composition roots (headless, TUI, --plan) and the bus is built
// first in all of them; a call sited in any one of them would leave the others
// publishing into an unredacted log until they happened to reach it. This
// injection needs no configuration — it is a pure "connect the top layer to the
// bottom one" statement — so there is nothing for init to get wrong or to read
// too early. The patterns themselves are read from the registry on every
// Redact, so rules registered later (registerResolvedAPIKey) apply here too.
func init() { event.SetLogRedactor(mustRedactor()) }

// registeredKeys dedupes registerResolvedAPIKey: it is called from
// resolveProvider and from newExecEngine (the coord runner builds one engine
// per agent run), and re-registering the same literal would grow the registry's
// rule list for the life of the process.
var (
	registeredKeysMu sync.Mutex
	registeredKeys   = map[string]bool{}
)

// registerResolvedAPIKey adds the API key this run actually authenticates with
// to the redaction registry as a literal pattern.
//
// This is the one hole no shape rule can close. The defaults in
// infra/secrets.go match keys with a recognizable prefix (sk-, sk-ant-, gsk_,
// hf_, ghp_, …), but most of the 28 presets in cognitive/providers.go issue
// prefix-less hex or base62 keys, and a rule that matched those on shape alone
// would redact every hash, commit id and checksum in the output. The exact
// value is the only thing that distinguishes them — and the root is the only
// place that knows it. infra.Secrets.Register documents this call site.
//
// The length guard is load-bearing, not defensive noise. QuoteMeta("") compiles
// to a pattern that matches at every position, so registering an empty or
// near-empty value would splice [REDACTED:api_key] between every character of
// every string that passes through any boundary — the log, the transcript, tool
// output. Eight characters is well below any real key and well above anything
// that could plausibly be a substring of ordinary text.
func registerResolvedAPIKey() {
	key := resolvedAPIKey()
	if len(key) < 8 {
		return
	}
	registeredKeysMu.Lock()
	defer registeredKeysMu.Unlock()
	if registeredKeys[key] {
		return
	}
	registeredKeys[key] = true
	_ = mustRedactor().Register(infra.SecretPattern{
		Name:    "provider_api_key",
		Pattern: regexp.MustCompile(regexp.QuoteMeta(key)),
		Replace: "[REDACTED:api_key]",
	})
}

// resolvedAPIKey mirrors cognitive.ResolveProviderErr's key resolution and
// returns the credential this run will send, or "" when there is none (the
// stub, a local Ollama/LM Studio preset, an unconfigured run). The resolution
// is duplicated rather than exported from cognitive because it is one lookup
// against an already-exported registry, and because the two answers only have
// to agree on *which env var holds a secret* — over-covering by a variable that
// turns out to be unused costs nothing, while under-covering leaks.
func resolvedAPIKey() string {
	if name := strings.TrimSpace(os.Getenv("YOLO_PROVIDER")); name != "" {
		p, ok := cog.LookupProvider(name)
		if !ok || !p.NeedsKey || p.KeyEnv == "" {
			return "" // unknown preset, or a local server that needs no key
		}
		return strings.TrimSpace(os.Getenv(p.KeyEnv))
	}
	// The env-only path (YOLO_BASE_URL + YOLO_API_KEY), with cognitive's
	// OPENAI_API_KEY fallback for an OpenAI endpoint.
	if k := strings.TrimSpace(os.Getenv("YOLO_API_KEY")); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

// newExecEngine builds the tool dispatcher with the HITL approval gate wired
// from configuration (defect 4.2). The gate is ON by default: AutoApprove is
// empty unless the operator opts a risk class out, so a medium- or high-risk
// tool call blocks for a human. Critical risk is denied by exec itself and is
// not expressible here.
//
// YOLO_AUTO_APPROVE_MEDIUM / YOLO_AUTO_APPROVE_HIGH are the documented
// interface (README, docs/user/configuration.md, tools.md, commands.md);
// --auto-approve is shorthand that sets both. Before this they were read
// nowhere and the map was hardcoded to {medium:true, high:true}, so no shipped
// path ever asked a human anything.
func newExecEngine(reg *execpkg.Registry, sandbox *execpkg.Sandbox, bus *event.Bus) *execpkg.Engine {
	auto := map[event.Risk]bool{}
	if envBool("YOLO_AUTO_APPROVE_MEDIUM") {
		auto[execpkg.RiskMedium] = true
	}
	if envBool("YOLO_AUTO_APPROVE_HIGH") {
		auto[execpkg.RiskHigh] = true
	}
	// The output boundary (File 08 §8.4.5). Leaving Normalizer nil installs
	// exec's passthrough, which copies raw stdout/stderr straight onto the bus —
	// an API key echoed by a tool went into the ToolResultEvent, the transcript
	// and the durability log verbatim. infra.DefaultRedactor is the process-wide
	// registry (the same one backing the log and Sentry boundaries), so nothing
	// needs an *infra.Infra plumbed here. A nil summarizer is the documented
	// fallback to the heuristic one.
	//
	// mustRedactor refuses to build the engine without a registry; see its
	// comment for why a nil-tolerant seam is the wrong contract at the root.
	// This helper is the single construction point for headless, TUI and coord,
	// which is also why the key registration is repeated here: the TUI's
	// /provider slash command re-resolves a provider through cognitive directly
	// (tui_runner.go), so a swapped-in credential first becomes visible to us on
	// the next engine build. The call dedupes, so paying it twice is free.
	redactor := mustRedactor()
	registerResolvedAPIKey()
	return execpkg.New(execpkg.Deps{
		Registry:   reg,
		Sandbox:    sandbox,
		Bus:        bus,
		Normalizer: execpkg.NewNormalizerWithRedactor(execpkg.DefaultLimits(), nil, redactor),
		Config:     execpkg.Config{AutoApprove: auto},
	})
}

// watchApprovalDecisions feeds bus-borne user verdicts to a Dispatch parked in
// exec's approval gate. exec publishes approval.request carrying an ApprovalID
// and then blocks until Engine.ResolveApproval(id, …) is called; the TUI only
// publishes user.approve / user.reject, and nothing joined the two, so the
// gate had no way to ever unblock. The composition root owns that join.
//
// The goroutine ends when the bus closes its subscriber channel.
func watchApprovalDecisions(eng *execpkg.Engine, bus *event.Bus) {
	ch := bus.Subscribe(event.Topic("user.approve"), event.Topic("user.reject"))
	go func() {
		for env := range ch {
			switch e := env.Evt.(type) {
			case *event.UserApproveEvent:
				eng.ResolveApproval(e.ApprovalID, true)
			case *event.UserRejectEvent:
				eng.ResolveApproval(e.ApprovalID, false)
			}
		}
	}()
}

// rejectRiskyWithoutHuman answers the approval prompt in headless mode, where
// there is nobody to answer it. The runtime parks a medium/high-risk call in
// WAIT_USER and then blocks on a channel only a user command can feed, so with
// the gate on (the default) an unattended run would hang forever. Refusing is
// the only honest answer: silently approving defeats the gate, and hanging is
// worse than a clear failure. The refusal is visible in the transcript (the
// task ends CANCELLED) and explained on stderr.
//
// The verdict is published from a second goroutine so the subscriber keeps
// draining while Publish applies backpressure. Both goroutines end when the bus
// closes the channel.
func rejectRiskyWithoutHuman(ctx context.Context, bus *event.Bus) {
	ch := bus.Subscribe(event.Topic("state.change"))
	stalls := make(chan string, 8)
	go func() {
		defer close(stalls)
		for env := range ch {
			sc, ok := env.Evt.(*event.StateChangeEvent)
			if !ok || sc.To != "WAIT_USER" || sc.Why != "approval" {
				continue
			}
			select {
			case stalls <- string(sc.Task):
			default: // a refusal is already queued for this park
			}
		}
	}()
	go func() {
		for task := range stalls {
			fmt.Fprintf(os.Stderr,
				"yolo: task %s requested a medium/high-risk action and no human is present to approve it — refusing. Re-run with --auto-approve (or YOLO_AUTO_APPROVE_MEDIUM/HIGH=true) to allow it.\n",
				task)
			_ = bus.Publish(ctx, &event.UserRejectEvent{Task: task, Reason: "headless: no human to approve"})
		}
	}()
}

// memoryRoot is the durable root for the memory Store: YOLO_MEMORY_DIR when
// set (tests and sandboxed runs point it at a temp dir), else a per-user
// directory under os.UserConfigDir. It used to be os.MkdirTemp + RemoveAll on
// exit, which deleted everything the run had just flushed — memory that never
// survives a process is not memory.
func memoryRoot() (string, error) {
	if d := strings.TrimSpace(os.Getenv("YOLO_MEMORY_DIR")); d != "" {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "yolo-code", "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
