// No-op stub ports for the Sprint 1 single-turn loop (File 15 §15.4, L2-005).
//
// The real layers land in Sprints 2–6; until then, the drive loop runs against
// stubs so the spine is testable now. Only the cognitive core has real canned
// behavior (a final answer) — everything else is a no-op that returns benign
// results. L2-005's stubbed cognitive.Core lives here too.

package runtime

import (
	"context"

	"github.com/yolo-code/yolo/internal/session"
)

// noopContextBuilder returns an empty context package.
type noopContextBuilder struct{}

func (noopContextBuilder) Build(context.Context, ContextRequest) (ContextPackage, error) {
	return nil, nil
}

// noopPromptCompiler returns the package unchanged.
type noopPromptCompiler struct{}

func (noopPromptCompiler) Compile(pkg ContextPackage) Prompt { return pkg }

// StubCognitive is the L2-005 stubbed cognitive core: it always answers
// directly (Final == true) with a canned message, so a prompt flows to a
// canned assistant message → DONE. The headless demo relies on this to make
// the single-turn loop observable without a real LLM.
type StubCognitive struct {
	Answer string
}

func (s StubCognitive) Think(context.Context, Prompt) (CognitiveTurn, error) {
	return CognitiveTurn{Final: true, Text: s.Answer}, nil
}

func (StubCognitive) HasMore(*session.Task) bool { return false }
