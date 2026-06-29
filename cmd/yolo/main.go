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

	coordpkg "github.com/yolo-code/yolo/internal/coord"
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
func run(args []string) error {
	headless := false
	var planGoal string
	var plan bool
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
		case "--version":
			fmt.Println(version)
			return nil
		}
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
