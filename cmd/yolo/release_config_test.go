//go:build release

// Release config gate (Sprint 11 H-008): validate `.goreleaser.yml` with the
// official `goreleaser check` tool. Skipped locally when goreleaser is not
// installed; the CI release workflow provides it.

package main

import (
	"os/exec"
	"testing"
)

func TestGoReleaserConfigIsValid(t *testing.T) {
	if _, err := exec.LookPath("goreleaser"); err != nil {
		t.Skipf("goreleaser not in PATH: %v", err)
	}

	cmd := exec.Command("goreleaser", "check", "--config", "../../.goreleaser.yml")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("goreleaser check: %v\n%s", err, out)
	}
}
