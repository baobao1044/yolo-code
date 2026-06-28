//go:build release

// Release gate (Sprint 11 H-008): build a binary with link-time version
// injection and verify `yolo --version` reports it. This mirrors the CI
// release build exactly.

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseBinaryReportsVersion(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yolo") + binaryExt()

	build := exec.Command("go", "build", "-o", bin, "-ldflags", "-X main.version=v0.11.0", ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	if !strings.Contains(string(out), "v0.11.0") {
		t.Fatalf("--version output = %q, want v0.11.0", out)
	}
}
