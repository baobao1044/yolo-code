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
	"strings"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
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
	var flagModel, flagBaseURL, flagRepo string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--headless":
			headless = true
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

	// Apply flag overrides into the environment so downstream provider
	// resolution (which reads YOLO_MODEL / YOLO_BASE_URL / YOLO_REPO_ROOT)
	// picks them up without each adapter plumbing the values separately.
	if flagModel != "" {
		_ = os.Setenv("YOLO_MODEL", flagModel)
	}
	if flagBaseURL != "" {
		_ = os.Setenv("YOLO_BASE_URL", flagBaseURL)
	}
	if flagRepo != "" {
		_ = os.Setenv("YOLO_REPO_ROOT", flagRepo)
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
