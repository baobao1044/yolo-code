// Package main is the yolo agent entry point.
//
// In Sprint 0 this is a no-op skeleton: it builds and exits cleanly. Later
// sprints wire the event bus (L3), the runtime FSM (L2), and — optionally —
// the interactive TUI. The headless runner (File 14 §14.10) is the primary
// demo path and is exercised from Sprint 0 onward.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "yolo:", err)
		os.Exit(1)
	}
}

// run is the CLI entry. Sprint 0: no-op. Later sprints parse flags such as
// `--headless` and drive the runtime via the event bus.
func run(args []string) error {
	_ = args
	return nil
}
