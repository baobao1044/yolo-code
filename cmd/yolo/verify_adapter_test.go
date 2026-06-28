// Tests for the verify→runtime adapter (Sprint 12 INT-002).

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

func TestVerifyAdapterDetectsSyntaxError(t *testing.T) {
	root := t.TempDir()
	badFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(badFile, []byte("package main\n\nfunc main() {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sandbox := exec.NewSandbox(root, root)
	engine := newVerifyEngine(sandbox)
	adapter := &verifyAdapter{engine: engine}

	obs := runtime.Observation{Files: []string{"main.go"}}
	pol := runtime.VerifyPolicy{RequireAST: true}
	v, err := adapter.Verify(context.Background(), obs, &session.Task{ID: "t-1"}, pol)
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if v.Pass {
		t.Fatalf("verdict passed for broken Go file")
	}
	if v.Stage != "ast" {
		t.Fatalf("failing stage = %q, want ast", v.Stage)
	}
}
