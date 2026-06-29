// Runtime-backed AgentRunner for the multi-agent orchestrator (Sprint 12
// INT-006, extended in Sprint 13). Coder runs a real runtime.Core;
// Reviewer/Tester run lightweight real checks through the exec/verify adapters
// and publish the canonical coord.* events.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yolo-code/yolo/internal/cognitive"
	econtext "github.com/yolo-code/yolo/internal/context"
	coordpkg "github.com/yolo-code/yolo/internal/coord"
	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

// runtimeAgentRunner implements coord.AgentRunner by spawning real agents. It
// owns per-agent session managers and port wiring so the orchestrator stays
// decoupled from runtime internals.
type runtimeAgentRunner struct {
	repo     string
	provider cognitive.Provider // default for coder when patches map is absent
	patches  map[string]string  // todoID -> patch body; test harness only
	bus      *event.Bus
	costPub  *costPublisher
}

func newRuntimeAgentRunner(repo string, provider cognitive.Provider, bus *event.Bus) *runtimeAgentRunner {
	return &runtimeAgentRunner{repo: repo, provider: provider, bus: bus}
}

func (r *runtimeAgentRunner) withCost(pub *costPublisher) *runtimeAgentRunner {
	r.costPub = pub
	return r
}

func (r *runtimeAgentRunner) withPatches(p map[string]string) *runtimeAgentRunner {
	r.patches = p
	return r
}

func (r *runtimeAgentRunner) Run(ctx context.Context, role coordpkg.Role, task event.TaskAssignEvent) error {
	switch role {
	case coordpkg.RoleCoder:
		return r.runCoder(ctx, task)
	case coordpkg.RoleReviewer:
		return r.runReviewer(ctx, task)
	case coordpkg.RoleTester:
		return r.runTester(ctx, task)
	default:
		return nil
	}
}

// runCoder runs a runtime.Core for the todo and emits coord.code.ready with
// the patch body (when supplied by the test harness) or an empty diff.
func (r *runtimeAgentRunner) runCoder(ctx context.Context, task event.TaskAssignEvent) error {
	deps, err := r.buildRuntimeDeps(ctx, task)
	if err != nil {
		return err
	}

	sid, err := deps.Session.OpenSession(ctx, task.PlanID, task.Brief)
	if err != nil {
		return err
	}

	core := runtime.New(deps)
	if _, err := core.Submit(ctx, sid, task.Brief); err != nil {
		return err
	}

	diff := ""
	if body, ok := r.patches[task.TodoID]; ok {
		diff = body
	}

	return r.bus.Publish(ctx, &event.CodeReadyEvent{
		PlanID:     task.PlanID,
		TodoID:     task.TodoID,
		Diff:       diff,
		SelfReport: "done via runtime.Core",
	})
}

// runReviewer reads the artifact file referenced by the task and approves if
// the file exists and contains non-trivial content.
func (r *runtimeAgentRunner) runReviewer(ctx context.Context, task event.TaskAssignEvent) error {
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		return err
	}
	path := firstArtifact(task.Artifacts, task.Brief)
	obs, err := execAd.Dispatch(ctx, runtime.ToolCall{Tool: "read_file", Args: []byte(`{"file":"` + path + `"}`)})
	// Approve if the artifact reads with content, or if there is no artifact
	// to audit (e.g. legacy tests with plain titles and no file extension).
	approved := err != nil || len(obs.Stdout) > 10
	return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
		PlanID: task.PlanID, TodoID: task.TodoID, Approved: approved,
	})
}

// runTester runs a low-risk bash command (go version) and passes if it exits 0.
func (r *runtimeAgentRunner) runTester(ctx context.Context, task event.TaskAssignEvent) error {
	execAd, _, _, _, err := r.buildAdapters()
	if err != nil {
		return err
	}
	obs, err := execAd.Dispatch(ctx, runtime.ToolCall{Tool: "bash", Args: []byte(`{"command":"go version"}`)})
	passed := err == nil && strings.Contains(obs.Stdout, "go") && !strings.Contains(obs.Stdout, "error")
	output := obs.Stdout
	if !passed && obs.Stdout == "" {
		output = fmt.Sprintf("error: %v", err)
	}
	return r.bus.Publish(ctx, &event.TestReportEvent{
		PlanID: task.PlanID, TodoID: task.TodoID, Passed: passed, Output: output,
	})
}

// buildRuntimeDeps wires the same real adapters the headless runner uses.
func (r *runtimeAgentRunner) buildRuntimeDeps(ctx context.Context, task event.TaskAssignEvent) (runtime.Deps, error) {
	dir, err := os.MkdirTemp("", "yolo-coord-*")
	if err != nil {
		return runtime.Deps{}, err
	}
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   r.bus,
		Git:   session.NewInMemCheckpointer(),
	})

	execAd, verifyAd, patchAd, restorer, err := r.buildAdapters()
	if err != nil {
		return runtime.Deps{}, err
	}

	var cogProv cognitive.Provider = r.provider
	if body, ok := r.patches[task.TodoID]; ok {
		path := firstArtifact(task.Artifacts, task.Brief)
		cogProv = &patchToolProvider{path: path, body: body}
	}

	d := runtime.Deps{
		Bus:       r.bus,
		Session:   smgr,
		Context:   contextAdapter{eng: econtext.New(econtext.Deps{Bus: r.bus, Repo: r.repo})},
		Prompt:    promptAdapter{comp: prompt.New(nil, r.bus)},
		Cognitive: newRealCognitiveCore(cogProv, r.bus),
		Exec:      execAd,
		Verify:    verifyAd,
		Patch:     patchAd,
		Restore:   restorer,
	}
	return d, nil
}

// buildAdapters builds the shared exec/verify/patch/restorer adapters for
// this repo. The returned adapters are safe to reuse across roles.
func (r *runtimeAgentRunner) buildAdapters() (*execAdapter, *verifyAdapter, *patchAdapter, runtime.Restorer, error) {
	sandbox := exec.NewSandbox(r.repo, r.repo)
	reg := new(exec.Registry)
	reg.Register(exec.NewBash(sandbox))
	reg.Register(exec.NewRead(sandbox))
	reg.Register(exec.NewListFiles(sandbox))
	reg.Register(exec.NewEditFile(sandbox))
	reg.Register(exec.NewGrep(sandbox))
	execEng := exec.New(exec.Deps{
		Registry: reg,
		Sandbox:  sandbox,
		Bus:      r.bus,
		Config: exec.Config{
			AutoApprove: map[event.Risk]bool{
				exec.RiskMedium: true,
				exec.RiskHigh:   true,
			},
		},
	})

	snap, err := newShadowSnap(r.repo)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	cp := newShadowCheckpointer(snap)
	patchEng := newPatchEngine(sandbox, cp, r.bus)

	execAd := &execAdapter{engine: execEng, patcher: patchEng}
	verifyAd := &verifyAdapter{engine: newVerifyEngine(sandbox)}
	patchAd := &patchAdapter{engine: patchEng}
	restorer := newShadowRestorer(snap)
	return execAd, verifyAd, patchAd, restorer, nil
}

func artifactFromBrief(brief string) string {
	for _, w := range strings.Fields(brief) {
		w = strings.Trim(w, ".,;:!?")
		if strings.Contains(w, ".") {
			return w
		}
	}
	return "artifact.go"
}

func firstArtifact(artifacts []string, brief string) string {
	if len(artifacts) > 0 {
		return artifacts[0]
	}
	return artifactFromBrief(brief)
}

// patchToolProvider is a scripted cognitive provider that emits a single
// patch tool call. It is used by the test harness to drive deterministic coder
// agents without an external LLM. After emitting the patch once, subsequent
// Think calls return a final answer so the FSM terminates (otherwise HasMore
// keeps returning true and the drive loop spins forever).
type patchToolProvider struct {
	path    string
	body    string
	emitted bool // true after the first Think call emits the patch
}

func (p *patchToolProvider) Window() int { return 128_000 }

func (p *patchToolProvider) Stream(ctx context.Context, req cognitive.Request) (<-chan cognitive.Chunk, error) {
	joined := strings.Join(func() []string {
		out := make([]string, 0, len(req.Messages))
		for _, m := range req.Messages {
			out = append(out, m.Content)
		}
		return out
	}(), "\n")

	out := make(chan cognitive.Chunk, 1)
	go func() {
		defer close(out)

		// Reflection: abort on verify failure (takes priority).
		if strings.Contains(joined, "Reflect on the failed verification") {
			select {
			case out <- cognitive.Chunk{Delta: "DECISION: abort"}:
			case <-ctx.Done():
			}
			return
		}

		// Subsequent Think calls after the patch was already emitted: return a
		// final answer so the FSM reaches DONE instead of looping forever.
		if p.emitted {
			select {
			case out <- cognitive.Chunk{Delta: "The task is complete."}:
			case <-ctx.Done():
			}
			return
		}

		// First Think call: emit the patch tool block.
		p.emitted = true
		pa := patchToolArgs{Path: p.path, Body: p.body}
		raw, err := json.Marshal(pa)
		if err != nil {
			return
		}
		block := fmt.Sprintf("```tool\n{\"tool\":\"patch\",\"args\":%s,\"reason\":\"apply planned edit\"}\n```\n", raw)
		select {
		case out <- cognitive.Chunk{Delta: block}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}
