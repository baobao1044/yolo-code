// The "patch" tool writes model-chosen content to a model-chosen path, which
// is what edit_file (RiskHigh) does. These tests pin that it is classified and
// gated the same way rather than being waved past the approval gate.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

// TestPatchToolClassifiedAsHighRiskWrite pins the classification itself: with
// the gate on (no YOLO_AUTO_APPROVE_* set) a patch call needs approval and
// reports the same risk class as the equivalent edit_file write.
func TestPatchToolClassifiedAsHighRiskWrite(t *testing.T) {
	t.Setenv("YOLO_AUTO_APPROVE_MEDIUM", "")
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "")

	root := t.TempDir()
	sandbox := exec.NewSandbox(root, root)
	reg := new(exec.Registry)
	reg.Register(exec.NewEditFile(sandbox))
	adapter := &execAdapter{engine: exec.New(exec.Deps{Registry: reg, Sandbox: sandbox})}

	call := runtime.ToolCall{Tool: "patch", Args: []byte(`{"path":"a.go","body":"package a\n"}`)}
	if got := adapter.RiskOf(call); got != exec.RiskHigh {
		t.Errorf("RiskOf(patch) = %q, want %q (same as edit_file: a model-chosen write)", got, exec.RiskHigh)
	}
	if !adapter.NeedsApproval(call) {
		t.Error("NeedsApproval(patch) = false, want true with the gate on")
	}
}

// TestPatchToolAutoApproveHighOpensTheGate is the other half: the operator's
// documented opt-out must reach patch too, or the escape hatch that exists for
// every other high-risk tool would not work for this one.
func TestPatchToolAutoApproveHighOpensTheGate(t *testing.T) {
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "true")

	root := t.TempDir()
	sandbox := exec.NewSandbox(root, root)
	adapter := &execAdapter{engine: exec.New(exec.Deps{Registry: new(exec.Registry), Sandbox: sandbox})}

	call := runtime.ToolCall{Tool: "patch", Args: []byte(`{"path":"a.go","body":"package a\n"}`)}
	if adapter.NeedsApproval(call) {
		t.Error("NeedsApproval(patch) = true with YOLO_AUTO_APPROVE_HIGH set, want false")
	}
}

// TestPatchToolCallCannotWriteUnprompted drives the real headless path with a
// scripted model that emits a patch tool call at a path the user never named.
// With the gate on and nobody at the keyboard the write must not land: the run
// asks for approval, headless refuses for the absent human, and the file stays
// absent. Before the fix the call was short-circuited past the gate and the
// file appeared with no prompt at all.
func TestPatchToolCallCannotWriteUnprompted(t *testing.T) {
	t.Setenv("YOLO_AUTO_APPROVE_MEDIUM", "")
	t.Setenv("YOLO_AUTO_APPROVE_HIGH", "")

	root := t.TempDir()
	target := filepath.Join(root, "hooks", "install.go")

	bus := event.New()
	sandbox := exec.NewSandbox(root, root)
	reg := new(exec.Registry)
	eng := exec.New(exec.Deps{Registry: reg, Sandbox: sandbox, Bus: bus})

	snap, err := newShadowSnap(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snap.close() })
	patchEng := newPatchEngine(sandbox, newShadowCheckpointer(snap), bus)
	execAd := &execAdapter{engine: eng, patcher: patchEng}

	out, err := runHeadlessDeps(context.Background(), bytes.NewBufferString("say hello\n"), 0,
		&headlessDeps{
			context:  contextAdapter{eng: econtext.New(econtext.Deps{Bus: bus, Repo: root})},
			prompt:   promptAdapter{comp: prompt.New(nil, bus)},
			cog:      newRealCognitiveCore(&unpromptedPatchProvider{}, bus),
			exec:     execAd,
			verify:   &verifyAdapter{engine: newVerifyEngine(sandbox)},
			patcher:  &patchAdapter{engine: patchEng},
			restorer: newShadowRestorer(snap),
			repo:     root,
			bus:      bus,
		})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}

	// patch.applied is the write landing: the transcript carries the path and
	// the new-file flag. Its absence, paired with the approval.request below,
	// is the whole property — the call was asked about instead of performed.
	if strings.Contains(out, `"type":"patch.applied"`) {
		t.Fatalf("patch applied without approval\n%s", out)
	}
	if !strings.Contains(out, `"type":"approval.request"`) || !strings.Contains(out, `"tool":"patch"`) {
		t.Fatalf("no approval.request for the patch call\n%s", out)
	}
	if !strings.Contains(out, `"risk":"high"`) {
		t.Fatalf("approval.request did not state the high risk class\n%s", out)
	}
	// And nothing reached disk. On its own this is weak evidence — a verify
	// failure would roll an unapproved write back out again — so it backs the
	// transcript checks rather than standing in for them.
	if _, serr := os.Stat(target); serr == nil {
		body, _ := os.ReadFile(target)
		t.Fatalf("patch wrote %s with no approval:\n%s", target, body)
	} else if !os.IsNotExist(serr) {
		t.Fatalf("stat %s: %v", target, serr)
	}
}

// unpromptedPatchProvider is a scripted model that answers every turn with a
// patch tool call creating a file the user never asked for.
type unpromptedPatchProvider struct{}

func (p *unpromptedPatchProvider) Window() int { return 128_000 }

func (p *unpromptedPatchProvider) Stream(ctx context.Context, _ cognitive.Request) (<-chan cognitive.Chunk, error) {
	args, _ := json.Marshal(map[string]string{
		"path": "hooks/install.go",
		"body": "package hooks\n\n// Install is the payload the user never asked for.\nfunc Install() {}\n",
	})
	block := fmt.Sprintf("```tool\n{\"tool\":\"patch\",\"args\":%s,\"reason\":\"add a hook\"}\n```\n", args)
	out := make(chan cognitive.Chunk, 1)
	go func() {
		defer close(out)
		select {
		case out <- cognitive.Chunk{Delta: block}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}
