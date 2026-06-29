// End-to-end single-agent integration regression (Sprint 12 INT-005):
// a headless run edits a Go file, verification fails on the broken syntax,
// and the runtime rolls the file back before aborting.

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
)

// TestRegressionPatchVerifyFailRollsBack drives a real headless run through
// PLAN → EXECUTE (patch) → VERIFY (AST fail) → RESTORE → CANCELLED.
func TestRegressionPatchVerifyFailRollsBack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := event.New()
	sandbox := exec.NewSandbox(root, root)

	reg := new(exec.Registry)
	eng := exec.New(exec.Deps{Registry: reg, Sandbox: sandbox, Bus: bus})

	snap, err := newShadowSnap(root)
	if err != nil {
		t.Fatal(err)
	}
	cp := newShadowCheckpointer(snap)
	patchEng := newPatchEngine(sandbox, cp, bus)
	execAd := &execAdapter{engine: eng, patcher: patchEng}
	verifyAd := &verifyAdapter{engine: newVerifyEngine(sandbox)}
	restorer := newShadowRestorer(snap)

	ctxEng := econtext.New(econtext.Deps{Bus: bus, Repo: root})
	comp := prompt.New(nil, bus)
	cogCore := newRealCognitiveCore(&integrationProvider{}, bus)

	out, err := runHeadlessDeps(context.Background(), bytes.NewBufferString("break main.go\n"), 0,
		&headlessDeps{
			context:  contextAdapter{eng: ctxEng},
			prompt:   promptAdapter{comp: comp},
			cog:      cogCore,
			exec:     execAd,
			verify:   verifyAd,
			patcher:  &patchAdapter{engine: patchEng},
			restorer: restorer,
			repo:     root,
			bus:      bus,
		})
	if err != nil {
		t.Fatalf("runHeadlessDeps: %v", err)
	}

	// The transcript must show the patch was applied, verification failed,
	// and the checkpoint was restored. The task must NOT complete.
	for _, want := range []string{
		"\"type\":\"patch.applied\"",
		"\"type\":\"verification.failed\"",
		"\"type\":\"task.restored\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing %s\n%s", want, out)
		}
	}
	if strings.Contains(out, "\"type\":\"task.completed\"") {
		t.Fatal("task completed despite verify failure")
	}

	// The file must be restored to the original valid Go code.
	restored, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "package main\n\nfunc main() {}\n"
	if string(restored) != wantFile {
		t.Fatalf("restored file = %q, want %q", restored, wantFile)
	}
}

// integrationProvider is a scripted model that emits a patch tool call on the
// first (plan) turn and an abort decision on the reflection turn.
type integrationProvider struct{}

func (p *integrationProvider) Window() int { return 128_000 }

func (p *integrationProvider) Stream(ctx context.Context, req cognitive.Request) (<-chan cognitive.Chunk, error) {
	var joined strings.Builder
	for _, m := range req.Messages {
		joined.WriteString(m.Content)
		joined.WriteByte('\n')
	}
	out := make(chan cognitive.Chunk, 1)
	go func() {
		defer close(out)
		if strings.Contains(joined.String(), "Reflect on the failed verification") {
			select {
			case out <- cognitive.Chunk{Delta: "the edit broke the file. DECISION: abort"}:
			case <-ctx.Done():
			}
			return
		}

		body := "<<<<<<< SEARCH\nfunc main() {}\n=======\nfunc main() { foo() }\n>>>>>>> REPLACE"
		args := map[string]string{"path": "main.go", "body": body}
		raw, _ := json.Marshal(args)
		block := fmt.Sprintf("```tool\n{\"tool\":\"patch\",\"args\":%s,\"reason\":\"break syntax\"}\n```\n", raw)
		select {
		case out <- cognitive.Chunk{Delta: block}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}
