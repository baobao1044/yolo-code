// Tests for the patch→runtime adapter and shadow checkpointer (Sprint 12 INT-003).

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/runtime"
)

func TestPatchAdapterAppliesBlocksAndRestores(t *testing.T) {
	root := t.TempDir()
	target := "hello.go"
	original := "package main\n\nfunc Hello() string { return \"old\" }\n"
	if err := os.WriteFile(filepath.Join(root, target), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	sandbox := exec.NewSandbox(root, root)
	snap, err := newShadowSnap(root)
	if err != nil {
		t.Fatal(err)
	}
	cp := newShadowCheckpointer(snap)
	engine := newPatchEngine(sandbox, cp, event.New())
	adapter := &patchAdapter{engine: engine}

	patchBody := `<<<<<<< SEARCH
func Hello() string { return "old" }
=======
func Hello() string { return "new" }
>>>>>>> REPLACE`

	res, err := adapter.Apply(context.Background(), runtime.PatchOp{
		Task: "t-1",
		Seq:  1,
		Path: target,
		Body: []byte(patchBody),
	})
	if err != nil {
		t.Fatalf("Apply = %v, want nil", err)
	}
	if !res.Accepted {
		t.Fatalf("patch rejected: %s", res.Reason)
	}
	if res.Checkpoint != "patch_1" {
		t.Fatalf("checkpoint = %q, want patch_1", res.Checkpoint)
	}

	after, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "package main\n\nfunc Hello() string { return \"new\" }\n" {
		t.Fatalf("patched file = %q", after)
	}

	restorer := newShadowRestorer(snap)
	if err := restorer.Restore(context.Background(), "t-1", res.Checkpoint); err != nil {
		t.Fatalf("Restore = %v, want nil", err)
	}

	restored, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Fatalf("restored file = %q, want %q", restored, original)
	}
}
