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
	}
	if !headless {
		// No TUI yet (Sprint 9); print a hint so `yolo` is not a silent no-op.
		fmt.Fprintln(os.Stderr, "yolo: interactive TUI not built yet — use `yolo --headless`")
		return nil
	}
	out, err := runHeadlessCtx(context.Background(), os.Stdin, 0)
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(out)
	return err
}
