// Package main is the yolo agent entry point.
//
// In Sprint 0 this was a no-op skeleton. Sprint 1 wires the headless runner
// (File 14 §14.10): `yolo --headless` reads a prompt from stdin and prints one
// JSON line per event to stdout — the cheapest demo path and the one golden
// transcripts assert against. The interactive TUI comes in Sprint 9.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
	"github.com/baobao1044/yolo-code/internal/event"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "yolo:", err)
		os.Exit(1)
	}
}

// run is the CLI entry. Sprint 1 supports `--headless` (pipe a prompt in,
// print the event transcript). Sprint 13 adds `--plan <goal>` which uses the
// multi-agent orchestrator for complex goals and falls back to the headless
// path for simple (Single-mode) goals.
//
// Configuration precedence (highest to lowest): CLI flags > environment vars >
// `.env` file. `.env` is loaded first (LoadDotEnv never overrides an already-set
// env var), then flags override the environment via os.Setenv so the existing
// provider resolution — which reads YOLO_* env vars — picks them up unchanged.
// The same os.Setenv bridge carries the runner flags (--auto-approve, --stub,
// --event-log, --repo): the TUI and coord runners read the env var rather than
// take a widened function signature, matching the YOLO_STUB / YOLO_MODEL idiom
// already in the tree. usage() is the single list of both halves.
func run(args []string) error {
	// Load .env from the current directory before anything reads env config.
	// Missing file is a no-op; shell env and flags take precedence.
	_ = LoadDotEnv(".env")

	headless := false
	var planGoal string
	var plan bool
	// Optional CLI overrides for the LLM provider + repo root. Empty means
	// "use the environment". We apply them to the environment before resolving
	// the provider so cognitive.OpenAICompatProviderFromEnv sees them.
	var flagModel, flagBaseURL, flagRepo, flagEventLog string
	// Lifecycle switches. autoApprove is the HITL escape hatch and defaults to
	// false: the approval gate is ON unless the operator explicitly turns it off.
	var autoApprove, stub bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--headless":
			headless = true
		case "--auto-approve":
			autoApprove = true
		case "--stub":
			stub = true
		case "--event-log":
			if i+1 >= len(args) {
				return errors.New("--event-log requires a path argument")
			}
			flagEventLog = args[i+1]
			i++
		case "--help", "-h":
			fmt.Print(usage)
			return nil
		case "--plan":
			if i+1 >= len(args) {
				return errors.New("--plan requires a goal argument")
			}
			plan = true
			planGoal = args[i+1]
			i++
		case "--model":
			if i+1 >= len(args) {
				return errors.New("--model requires a value argument")
			}
			flagModel = args[i+1]
			i++
		case "--base-url":
			if i+1 >= len(args) {
				return errors.New("--base-url requires a value argument")
			}
			flagBaseURL = args[i+1]
			i++
		case "--repo":
			if i+1 >= len(args) {
				return errors.New("--repo requires a path argument")
			}
			flagRepo = args[i+1]
			i++
		case "--version":
			fmt.Println(version)
			return nil
		}
	}

	// Apply flag overrides into the environment so the runners pick them up
	// without each one plumbing the values separately: YOLO_MODEL /
	// YOLO_BASE_URL feed provider resolution, YOLO_REPO_ROOT feeds repoRoot()
	// (the sandbox + Context Engine root and infra's permission root), and
	// YOLO_AUTO_APPROVE / YOLO_STUB / YOLO_EVENT_LOG feed the lifecycle wiring.
	if flagModel != "" {
		_ = os.Setenv("YOLO_MODEL", flagModel)
	}
	if flagBaseURL != "" {
		_ = os.Setenv("YOLO_BASE_URL", flagBaseURL)
	}
	if flagRepo != "" {
		// Absolute: the sandbox and infra's permission policy compare paths, and
		// a relative root would match nothing once a tool resolves a real path.
		abs, err := filepath.Abs(flagRepo)
		if err != nil {
			return fmt.Errorf("--repo %q: %w", flagRepo, err)
		}
		_ = os.Setenv("YOLO_REPO_ROOT", abs)
	}
	if autoApprove {
		// --auto-approve is shorthand for both documented per-risk switches. The
		// env vars are the interface (README, docs/user/configuration.md) and
		// work on their own, including one without the other.
		_ = os.Setenv("YOLO_AUTO_APPROVE_MEDIUM", "true")
		_ = os.Setenv("YOLO_AUTO_APPROVE_HIGH", "true")
	}
	if stub {
		_ = os.Setenv("YOLO_STUB", "1")
	}
	if flagEventLog != "" {
		_ = os.Setenv("YOLO_EVENT_LOG", flagEventLog)
	}

	if plan {
		if headless {
			return errors.New("--plan and --headless are mutually exclusive")
		}
		if !coordpkg.ShouldOrchestrate(planGoal) {
			// Single-mode requests are answered directly by the runtime.
			out, err := runHeadlessCtx(context.Background(), strings.NewReader(planGoal), 0)
			if err != nil {
				return err
			}
			_, err = os.Stdout.WriteString(out)
			return err
		}
		out, err := runPlanCtx(context.Background(), planGoal)
		if err != nil {
			return err
		}
		_, err = os.Stdout.WriteString(out)
		return err
	}

	if !headless {
		return runTUI(context.Background())
	}
	out, err := runHeadlessCtx(context.Background(), os.Stdin, 0)
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(out)
	return err
}

// usage lists every flag and the environment variable it sets. Flags reach the
// TUI / coord / headless runners through the environment (the YOLO_STUB idiom),
// so the two columns are the same switch seen from either side.
const usage = `yolo — agentic coding assistant

Usage:
  yolo                      start the interactive TUI (default)
  yolo --headless           read one prompt from stdin, print the JSONL transcript
  yolo --plan <goal>        decompose <goal> and run the multi-agent orchestrator

Flags (env var it sets):
  --headless                run one non-interactive turn
  --plan <goal>             multi-agent plan mode
  --model <name>            LLM model                     (YOLO_MODEL)
  --base-url <url>          OpenAI-compatible endpoint     (YOLO_BASE_URL)
  --repo <path>             workspace root                 (YOLO_REPO_ROOT)
  --stub                    use the deterministic offline stub provider, NOT a
                            real model                     (YOLO_STUB)
  --event-log <path>        append every event to a durable, fsynced log
                                                           (YOLO_EVENT_LOG)
  --auto-approve            DISABLE the human-in-the-loop approval gate for
                            BOTH risk classes: run medium- and high-risk tool
                            calls without asking. Off by default — the gate is
                            on. (YOLO_AUTO_APPROVE_MEDIUM + _HIGH)
  --version                 print the version and exit
  --help, -h                print this help and exit

Other environment variables:
  YOLO_AUTO_APPROVE_MEDIUM  "true" auto-approves medium-risk tool calls
  YOLO_AUTO_APPROVE_HIGH    "true" auto-approves high-risk tool calls
                            (both default to false: the gate is on; set either
                            one alone for per-risk granularity)
  YOLO_PROVIDER             preset name (see the provider registry)
  YOLO_API_KEY              API key for the OpenAI-compatible endpoint
  YOLO_MEMORY_DIR           override the durable memory root (default:
                            <user config dir>/yolo-code/memory)
  YOLO_SESSION_DIR          override the durable session/transcript root
                            (default: <user config dir>/yolo-code/sessions)
  YOLO_COST_RATES           "tool=dollars" pairs priced by you, e.g.
                            "bash=0.005,*=0.001". No default: unpriced runs
                            report tool calls, not money
  YOLO_MAX_TOOL_CALLS       warn once at this many tool invocations. A warning
                            only — the run is NOT halted (no cap)
  YOLO_COST_PER_TOKEN       dollars per token, e.g. 0.000003, charged on
                            (in+out) for turns whose provider reported usage.
                            This is the price the spend cap counts against;
                            without it no dollars accrue (default 0)
  YOLO_MAX_COST             HARD CAP: dollar spend. Reaching it cancels the
                            task ("spend cap reached"). Needs
                            YOLO_COST_PER_TOKEN to ever fire (no cap)
  YOLO_MAX_TIME             HARD CAP: wall clock as a Go duration ("10m",
                            "90s", "1h30m"), measured from task start.
                            Reaching it cancels the task ("time cap reached").
                            A unitless "600" is REJECTED and caps nothing;
                            so are "1d", negatives and garbage (no cap)

  With neither hard cap set the run is genuinely uncapped: no ledger is built
  and nothing can abort on cost. The 6-loop / 10-reflection thresholds degrade
  the agent (less reflection), they do not stop the task.
`

// envBool reads a boolean environment switch. "1", "true", "yes" and "on"
// (any case) are true; everything else — including unset and the empty string —
// is false. Exported within the package so every runner reads the flags the
// same way; main.go is the only writer.
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// repoRoot resolves the workspace root: YOLO_REPO_ROOT (set by --repo) when
// non-empty, the process working directory otherwise. Every runner calls this
// instead of os.Getwd() directly so --repo genuinely reaches the sandbox, the
// Context Engine and infra's permission policy.
func repoRoot() (string, error) {
	if r := strings.TrimSpace(os.Getenv("YOLO_REPO_ROOT")); r != "" {
		return filepath.Abs(r)
	}
	return os.Getwd()
}

// newBus builds the process event bus. YOLO_EVENT_LOG (set by --event-log)
// switches it to the durable form: every envelope is fsynced to an append-only
// log before any subscriber sees it, so a crashed run stays replayable. Unset
// keeps the in-memory bus the tests and the default demo path use.
func newBus() (*event.Bus, error) {
	path := strings.TrimSpace(os.Getenv("YOLO_EVENT_LOG"))
	if path == "" {
		return event.New(), nil
	}
	bus, err := event.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open event log %q: %w", path, err)
	}
	return bus, nil
}

// reportDropped warns on stderr when the bus dropped a delivery. A drop means a
// subscriber never saw an event the agent acted on, so the transcript and the
// durability log disagree with reality — worth a line even on a clean exit.
func reportDropped(bus *event.Bus) {
	if s := bus.Stats(); s.Dropped > 0 {
		fmt.Fprintf(os.Stderr, "yolo: warning: %d of %d events were dropped (a subscriber fell behind); the transcript is incomplete\n", s.Dropped, s.Published)
	}
}
