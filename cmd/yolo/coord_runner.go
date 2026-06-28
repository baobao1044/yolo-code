// Runtime-backed AgentRunner for the multi-agent orchestrator (Sprint 12
// INT-006). A coder role runs a real runtime.Core through the headless
// adapters; the runner translates task completion into the canonical
// coord.code.ready event. Reviewer/Tester roles remain event-only seams for
// this sprint.

package main

import (
	"context"
	"os"

	"github.com/yolo-code/yolo/internal/cognitive"
	econtext "github.com/yolo-code/yolo/internal/context"
	coordpkg "github.com/yolo-code/yolo/internal/coord"
	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/exec"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
)

// runtimeAgentRunner implements coord.AgentRunner by spawning a real
// runtime.Core for the coder role. It owns the per-agent session manager and
// port wiring so the orchestrator stays decoupled from runtime internals.
type runtimeAgentRunner struct {
	repo     string
	provider cognitive.Provider
	bus      *event.Bus
}

func newRuntimeAgentRunner(repo string, provider cognitive.Provider, bus *event.Bus) *runtimeAgentRunner {
	return &runtimeAgentRunner{repo: repo, provider: provider, bus: bus}
}

// Run dispatches one agent turn for role. The coder role is backed by the full
// runtime.Core; reviewer/tester publish deterministic coord.* events.
func (r *runtimeAgentRunner) Run(ctx context.Context, role coordpkg.Role, task event.TaskAssignEvent) error {
	switch role {
	case coordpkg.RoleCoder:
		return r.runCoder(ctx, task)
	case coordpkg.RoleReviewer:
		return r.bus.Publish(ctx, &event.ReviewVerdictEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Approved: true,
		})
	case coordpkg.RoleTester:
		return r.bus.Publish(ctx, &event.TestReportEvent{
			PlanID: task.PlanID, TodoID: task.TodoID, Passed: true, Output: "ok",
		})
	default:
		return nil
	}
}

func (r *runtimeAgentRunner) runCoder(ctx context.Context, task event.TaskAssignEvent) error {
	deps, smgr, err := r.buildRuntimeDeps(ctx)
	if err != nil {
		return err
	}

	sid, err := smgr.OpenSession(ctx, task.PlanID, task.Brief)
	if err != nil {
		return err
	}

	core := runtime.New(deps)
	if _, err := core.Submit(ctx, sid, task.Brief); err != nil {
		return err
	}

	return r.bus.Publish(ctx, &event.CodeReadyEvent{
		PlanID:     task.PlanID,
		TodoID:     task.TodoID,
		Diff:       "",
		SelfReport: "done via runtime.Core",
	})
}

// buildRuntimeDeps wires the same real adapters the headless runner uses:
// context, prompt, cognitive, exec (with patch routing), verify, patch, and
// the shadow-copy restorer.
func (r *runtimeAgentRunner) buildRuntimeDeps(ctx context.Context) (runtime.Deps, *session.Manager, error) {
	dir, err := os.MkdirTemp("", "yolo-coord-*")
	if err != nil {
		return runtime.Deps{}, nil, err
	}
	smgr := session.New(session.Deps{
		Store: session.NewFileStore(dir),
		Bus:   r.bus,
		Git:   session.NewInMemCheckpointer(),
	})

	sandbox := exec.NewSandbox(r.repo, r.repo)
	reg := new(exec.Registry)
	execEng := exec.New(exec.Deps{Registry: reg, Sandbox: sandbox, Bus: r.bus})

	snap, err := newShadowSnap(r.repo)
	if err != nil {
		return runtime.Deps{}, nil, err
	}
	cp := newShadowCheckpointer(snap)
	patchEng := newPatchEngine(sandbox, cp, r.bus)

	d := runtime.Deps{
		Bus:       r.bus,
		Session:   smgr,
		Context:   contextAdapter{eng: econtext.New(econtext.Deps{Bus: r.bus, Repo: r.repo})},
		Prompt:    promptAdapter{comp: prompt.New(nil, r.bus)},
		Cognitive: newRealCognitiveCore(r.provider, r.bus),
		Exec:      &execAdapter{engine: execEng, patcher: patchEng},
		Verify:    &verifyAdapter{engine: newVerifyEngine(sandbox)},
		Patch:     &patchAdapter{engine: patchEng},
		Restore:   newShadowRestorer(snap),
	}
	return d, smgr, nil
}
