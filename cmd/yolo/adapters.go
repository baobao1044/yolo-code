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

	econtext "github.com/yolo-code/yolo/internal/context"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/runtime"
	"github.com/yolo-code/yolo/internal/session"
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
