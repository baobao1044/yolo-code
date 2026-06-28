// Package main is the yolo agent entry point.
//
// In Sprint 0 this was a no-op skeleton. Sprint 1 wires the headless runner
// (File 14 §14.10): `yolo --headless` reads a prompt from stdin and prints one
// JSON line per event to stdout — the cheapest demo path and the one golden
// transcripts assert against. The interactive TUI comes in Sprint 9.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "yolo:", err)
		os.Exit(1)
	}
}

// run is the CLI entry. Sprint 1 supports `--headless` (pipe a prompt in,
// print the event transcript). Other flags land with later sprints.
func run(args []string) error {
	headless := false
	for _, a := range args {
		if a == "--headless" {
			headless = true
		}
		if a == "--version" {
			fmt.Println(version)
			return nil
		}
	}
	if !headless {
		// TUI is built (Sprint 9) but interactive wiring lands in the integration
		// sprint (the runtime doesn't subscribe to user.* yet — see Sprint 9
		// spec Decision 4). Until then the interactive path can't drive the
		// runtime, so keep the hint pointing at --headless.
		fmt.Fprintln(os.Stderr, "yolo: interactive TUI pending integration wiring — use `yolo --headless`")
		return nil
	}
	out, err := runHeadlessCtx(context.Background(), os.Stdin, 0)
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(out)
	return err
}
