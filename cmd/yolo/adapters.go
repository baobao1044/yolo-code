// L5-003 composition-root adapters (File 02 §2.2 import matrix). The runtime
// (L2) is NOT allowed to import context (L4) or prompt (L5) — only session,
// event, infra, exec, verify, patch. So the wiring that connects the real
// Context Engine and Prompt Compiler to the runtime's port interfaces lives
// here, in cmd/yolo (the binary's composition root). The runtime sees only its
// own ContextBuilder/PromptCompiler interfaces; these adapters satisfy them
// by delegating to the concrete layers.
//
// Sprint 2 wires these into the headless runner so the stub cognitive core
// receives a real, compiled prompt carrying real file contents from a fixture
// repo — the Sprint 2 exit bar (S6: "what is it doing now" is testable).

package main

import (
	"context"

	econtext "github.com/baobao1044/yolo-code/internal/context"
	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/memory"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// contextAdapter adapts the Layer 4 Context Engine to the runtime's
// ContextBuilder port. It owns the engine; Build delegates and translates the
// runtime's ContextRequest into the context package's shape (the two are
// structurally identical; the runtime keeps its own copy so it doesn't import
// context).
type contextAdapter struct {
	eng *econtext.Engine
}

// Build satisfies runtime.ContextBuilder. It maps the runtime's
// ContextRequest to the context package's ContextRequest, runs the Engine, and
// returns the pointer the Engine produces as the opaque runtime.ContextPackage
// (typed `any`).
func (a contextAdapter) Build(ctx context.Context, req runtime.ContextRequest) (runtime.ContextPackage, error) {
	return a.eng.Build(ctx, econtext.ContextRequest{
		Task:    req.Task,
		Session: req.Session,
	})
}

// promptAdapter adapts the Layer 5 Prompt Compiler to the runtime's
// PromptCompiler port. Compile receives the opaque runtime.ContextPackage
// (which the contextAdapter produced as a *econtext.ContextPackage), asserts
// its shape, and forwards to the compiler's CompilePackage, returning the
// []Message as the opaque runtime.Prompt.
type promptAdapter struct {
	comp *prompt.Compiler
}

// Compile satisfies runtime.PromptCompiler. It type-asserts the package the
// context adapter produced and forwards; a nil or wrong-typed package yields
// nil (the stub core then answers with its canned message — no prompt lost the
// turn).
func (a promptAdapter) Compile(pkg runtime.ContextPackage) runtime.Prompt {
	cp, ok := pkg.(*econtext.ContextPackage)
	if !ok {
		return nil
	}
	return a.comp.CompilePackage(cp)
}

// assertCognitive is the L5-003 cognitive core: it behaves like the Sprint 1
// StubCognitive (always answers, Final) but first asserts that the prompt it
// received actually contains the expected real file content. The assertion is
// recorded so the test can inspect it. This is the S6 exit bar: the model's
// view of the task is now testable — the stub inspects the prompt and asserts
// it saw the expected files.
type assertCognitive struct {
	// want is the substring the prompt must contain (e.g. a file's func body).
	want string
	// saw records whether the assertion fired at least once.
	saw bool
	// ok records the last assertion's outcome.
	ok bool
	// answer is the canned final text the core returns (mirrors StubCognitive).
	answer string
}

// Think satisfies runtime.CognitiveCore. It asserts the compiled prompt
// contains want, records the outcome, and returns a final answer so the drive
// loop reaches DONE. A nil/empty prompt fails the assertion but still answers
// (the task completes; the test checks saw/ok).
func (a *assertCognitive) Think(_ context.Context, p runtime.Prompt) (runtime.CognitiveTurn, error) {
	a.saw = true
	msgs, ok := p.([]prompt.Message)
	if !ok {
		a.ok = false
		return runtime.CognitiveTurn{Final: true, Text: a.answer}, nil
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	a.ok = containsStr(joined, a.want)
	return runtime.CognitiveTurn{Final: true, Text: a.answer}, nil
}

func (a *assertCognitive) HasMore(*session.Task) bool { return false }

// RecordToolResult satisfies runtime.CognitiveCore. The assert core ignores
// tool results — it always answers Final, so multi-turn never reaches it.
func (a *assertCognitive) RecordToolResult(string, string) {}

// Reflect on the assertion core aborts (it never reaches the verify path; a
// failure here would be a wiring bug). The real cognitive core (Sprint 6
// wiring) overrides this with the LLM reflection; assertCognitive is the Sprint
// 2/3 test core that only checks the prompt carried real content.
func (a *assertCognitive) Reflect(context.Context, *session.Task, runtime.Verdict, runtime.Observation) runtime.ReflectionDecision {
	return runtime.ReflectionDecision{Abort: true, Note: "assert cognitive has no reflection"}
}

// containsStr is a local strings.Contains stand-in (kept local so this non-test
// file doesn't add a strings import just for one call site).
func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- L10-006 memory adapters (File 11 §11.8 + File 06 §6.1) ---
//
// The composition root bridges memory into the two seams it must satisfy:
// runtime.MemoryStore.Update (the runtime's write trigger on the direct-answer
// path) and context.Memory.Preferences/Project (the Context Engine's read
// surface). Memory can't implement either directly (it may import only event +
// stdlib; runtime/context are forbidden), so the adapters live here, exactly as
// the context/prompt adapters do. The adapters translate memory.Part →
// context.Part (matrix-driven duplication, File 15 §15.15.2) and route the
// runtime's Update to a published event the memory listener reacts to — never
// mutating a sub-store directly (§11.2).

// memoryStoreAdapter satisfies runtime.MemoryStore. Update publishes a
// task.completed-like learning event the memory listener reacts to (it does
// NOT mutate a sub-store directly — the listener is the only writer, §11.2).
// The runtime calls Update(ctx, taskID) on the direct-answer path; the adapter
// turns that into a memory learning by publishing the event the listener
// dispatches. The store + bus are owned by the composition root.
type memoryStoreAdapter struct {
	store *memory.Store
	bus   *event.Bus
}

// Update satisfies runtime.MemoryStore. It publishes a memory-relevant event
// (task.completed) so the listener persists the conversation + records the
// learning — the event-driven path, not a direct write.
func (a memoryStoreAdapter) Update(ctx context.Context, taskID session.TaskID) error {
	if a.bus == nil {
		return nil
	}
	return a.bus.Publish(ctx, &event.TaskCompletedEvent{Task: string(taskID)})
}

// contextMemoryAdapter satisfies context.Memory, surfacing memory's preferences
// + project memory into the Context Engine (File 06 §6.1). It translates
// memory.Part → context.Part field-for-field (the kinds line up; see
// memory.PartKind vs context.PartKind).
type contextMemoryAdapter struct {
	store *memory.Store
}

// Preferences returns the user's preferences as context.Parts (one per key).
func (a contextMemoryAdapter) Preferences(ctx context.Context, _ string) []econtext.Part {
	if a.store == nil {
		return nil
	}
	return toContextParts(a.store.Preferences().Preferences(ctx))
}

// Project returns the project memory (AGENTS.md) as context.Parts.
func (a contextMemoryAdapter) Project(ctx context.Context, projectID string) []econtext.Part {
	if a.store == nil {
		return nil
	}
	return toContextParts(a.store.Project().Project(ctx))
}

// toContextParts translates memory.Part → context.Part field-for-field. The
// kinds align (memory.KindPreferences → context.KindPreferences, etc.); the
// adapter maps the kind string so the ranker/compress group them correctly.
func toContextParts(ps []memory.Part) []econtext.Part {
	out := make([]econtext.Part, 0, len(ps))
	for _, p := range ps {
		out = append(out, econtext.Part{
			Kind:   econtext.PartKind(p.Kind),
			Source: p.Source,
			Text:   p.Text,
			Score:  p.Score,
			Attr:   p.Attr,
		})
	}
	return out
}
