// Tests for the --version flag and version injection seam (Sprint 11 H-008).

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestVersionFlagPrintsCurrentVersion(t *testing.T) {
	// Capture stdout while run handles --version.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	err = run([]string{"--version"})

	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("run(--version) = %v, want nil", err)
	}

	out, _ := io.ReadAll(r)
	got := strings.TrimSpace(string(out))
	if got != version {
		t.Fatalf("--version printed %q, want %q", got, version)
	}
}

func TestVersionInjectionViaLdflags(t *testing.T) {
	// Build a temporary binary with an injected version and run --version.
	// This exercises the exact ldflags path CI uses.
	tmp := t.TempDir()
	bin := tmp + "/yolo" + binaryExt()
	if err := runGo(t, "build", "-o", bin, "-ldflags", "-X main.version=v0.11.0-test", "."); err != nil {
		t.Fatalf("go build with ldflags: %v", err)
	}

	out, err := runBinary(bin, "--version")
	if err != nil {
		t.Fatalf("%s --version = %v", bin, err)
	}
	if !strings.Contains(out, "v0.11.0-test") {
		t.Fatalf("injected version output = %q, want v0.11.0-test", out)
	}
}

// binaryExt returns the executable file extension for the current OS.
func binaryExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// runGo runs `go` with the given args inside cmd/yolo.
func runGo(t *testing.T, args ...string) error {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = "."
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// runBinary runs the built binary with args and returns stdout.
func runBinary(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
