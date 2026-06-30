// Package context implements Layer 4 — the Context Engine (File 06). It
// gathers, ranks, scores, and compresses context inputs into a structured
// ContextPackage that the Prompt Compiler (Layer 5) turns into the final
// prompt. This is the module that decides what the model sees.
//
// Sprint 2 scope (L4-001…003): the seven inputs (two real — open files read
// from disk and project rules from AGENTS.md — plus session history, with git
// diff / repo graph / diagnostics / preferences stubbed for future layers), the
// weighted relevance score (recency/proximity/explicit real; semantic and
// centrality stubbed), and the three cheap compression passes. The memory,
// git, graph, and diagnostics collaborators are port interfaces so this package
// builds with only event + session present.
//
// Note on the package name: this package is named `context`, which collides
// with the standard library. Inside this package the stdlib is aliased as
// `stdctx`; importers alias this package (e.g. `econtext`).

package context

import (
	"time"

	"github.com/baobao1044/yolo-code/internal/session"
)

// PartKind classifies a context part by its source.
type PartKind string

const (
	KindSystem       PartKind = "system"       // role, rules, tool schemas
	KindProject      PartKind = "project"      // AGENTS.md, structure
	KindConversation PartKind = "conversation" // ranked history
	KindFile         PartKind = "file"         // open + retrieved files
	KindGraph        PartKind = "graph"        // relevant symbols/edges
	KindDiagnostics  PartKind = "diagnostics"  // current errors
	KindPreferences  PartKind = "preferences"  // user prefs
)

// Group names the seven ordered groups of a ContextPackage (File 06 §6.4).
type Group string

// Part is one unit of context: a kind, a source label (e.g. a file path), the
// rendered text, and attribution used by the ranker (timestamp, score, and
// whether the user explicitly @-referenced it). Attr carries optional metadata
// the Prompt Compiler reads when ordering (e.g. role="assistant" on a
// conversation turn, File 06 §6.6.2 order() reads h.Attr["role"]).
type Part struct {
	Kind     PartKind          `json:"kind"`
	Source   string            `json:"source"` // file path / "AGENTS.md" / "turn#3"
	Text     string            `json:"text"`
	Score    float64           `json:"score"`    // assigned by rank()
	Recency  time.Time         `json:"recency"`  // when this part was last relevant
	Explicit bool              `json:"explicit"` // user @-referenced
	Attr     map[string]string `json:"attr,omitempty"`
}

// ContextRequest is the input to Engine.Build: the task being driven and the
// session it belongs to. Defined here (not in runtime) because the Context
// Engine owns the request shape; the runtime's drive loop constructs an
// equivalent and the wiring adapts.
type ContextRequest struct {
	Task    *session.Task
	Session *session.Session
}

// Budget is the token budget allocated across the prompt groups (File 06
// §6.6.1). The Context Engine computes the window; the Prompt Compiler
// enforces it. Populated by Build so the compiler can trim.
type Budget struct {
	Window       int
	Reserve      int
	System       int
	Project      int
	Conversation int
	Files        int
	User         int
}

// ContextPackage is the structured, ranked, compressed bundle handed to the
// Prompt Compiler (File 06 §6.4). It is structured, not a flat string: the
// compiler orders these groups deterministically. The User group carries the
// current request (the task's goal); the §6.6.2 order() references
// pkg.User[0].Text, which the §6.4 struct omits — Engine.Build fills it.
type ContextPackage struct {
	Task         session.TaskID
	System       []Part
	Project      []Part
	Conversation []Part
	Files        []Part
	Graph        []Part
	Diagnostics  []Part
	Preferences  []Part
	User         []Part // the current request (task goal); ordered third
	Budget       Budget
}
