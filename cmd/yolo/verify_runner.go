// Real verify.Runner and verify.FS adapters for the verification pipeline
// (Sprint 12 INT-002). These live in cmd/yolo so verify stays free of sysio
// and exec.Sandbox remains the single path-confinement gate.

package main

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/verify"
)

// verifyRunner shells out to tools (gofmt, go vet, go build, go test) via
// os/exec. The runtime context is used directly so cancellation propagates.
type verifyRunner struct{}

func (verifyRunner) Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := osexec.CommandContext(ctx, name, args...)
	var outb, errb strings.Builder
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*osexec.ExitError); ok {
			return outb.String(), errb.String(), exitErr.ExitCode(), nil
		}
		return outb.String(), errb.String(), -1, err
	}
	return outb.String(), errb.String(), 0, nil
}

// verifyFS reads files through the exec sandbox so verification never escapes
// the repo root.
type verifyFS struct {
	sandbox *exec.Sandbox
}

func (f *verifyFS) Read(ctx context.Context, path string) (string, error) {
	full, err := f.sandbox.Resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// newVerifyEngine builds a real verify.Engine with the sandbox-scoped runner
// and filesystem. The composition root uses this to satisfy runtime.Verifier.
func newVerifyEngine(sandbox *exec.Sandbox) *verify.Engine {
	return verify.NewEngine(verify.Deps{
		Runner: verifyRunner{},
		FS:     &verifyFS{sandbox: sandbox},
	})
}
