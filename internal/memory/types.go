// Shared types for the memory system (File 11). Memory defines its own types
// (Message, ExecEntry, Part) so it doesn't import session/context — the import
// matrix (File 15 §15.15.2) lets memory import only `event` + stdlib. The
// composition root (cmd/yolo) translates these into the consuming layers'
// shapes (context.Part, session message history) via adapters, exactly as the
// patch engine keeps its own FileStat ↔ event.PatchFile (matrix-driven
// duplication, commented at each site so it isn't mistaken for drift).
//
// Determinism (S5): messages and entries are append-ordered and JSON-serialized
// with sorted keys where maps appear, so a re-load is byte-identical.

package memory

import "context"

// Role is one side of a conversation turn: "user" or "assistant" (File 11
// §11.3.2). Kept as a string so the prompt compiler's role field is a direct
// copy.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one conversation turn (§11.3.2): a role + text (+ a tool call/
// result pair when the turn carried one). The conversation store persists a
// slice of these per session, ordered by append (seq implied by slice order).
type Message struct {
	Role       Role   `json:"role"`
	Text       string `json:"text"`
	ToolCall   string `json:"toolCall,omitempty"`   // tool name if the turn issued a call
	ToolResult string `json:"toolResult,omitempty"` // the tool's one-line result, if any
}

// ExecEntry is one row of the per-task execution audit trail (§11.4.1): what
// the agent did — a tool call, a patch, a reflection note, a verify verdict.
// The kind is the categorization Reflection cites ("what went wrong last
// time"); the summary is the one-line human view; Snapshot names the git
// checkpoint the entry produced (so undo can roll to it).
type ExecEntry struct {
	Seq      int    `json:"seq"`
	Kind     string `json:"kind"`    // "tool" | "patch" | "reflection" | "verify"
	Summary  string `json:"summary"` // one-line description
	Snapshot string `json:"snapshot,omitempty"`
}

// PartKind labels a memory Part's group (mirrors context.PartKind, File 06
// §6.4). Memory returns Parts to the composition root, which translates them
// into context.Part; the kinds line up so the adapter is a field-for-field copy.
type PartKind string

const (
	KindConversation PartKind = "conversation" // a ranked history turn
	KindProject      PartKind = "project"      // AGENTS.md, structure
	KindPreferences  PartKind = "preferences"  // user prefs
	KindRAG          PartKind = "rag"          // a retrieved code chunk
)

// Part is one unit of memory surfaced to the Context Engine (mirrors
// context.Part, File 06 §6.4). The composition root's adapter copies these
// fields into context.Part; memory keeps its own type so it doesn't import
// context. Attr carries optional metadata the adapter forwards (e.g. role on a
// conversation turn, path/name/kind on a RAG chunk).
type Part struct {
	Kind     PartKind          `json:"kind"`
	Source   string            `json:"source"` // file path / "AGENTS.md" / "turn#3"
	Text     string            `json:"text"`
	Score    float64           `json:"score,omitempty"`
	Explicit bool              `json:"explicit,omitempty"`
	Attr     map[string]string `json:"attr,omitempty"`
}

// ErrNotFound is returned by Load/Get when no record exists for the key. Mirrors
// session.ErrNotFound so the composition root can translate errors uniformly.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "memory: not found" }

// Embedder turns text into a fixed-dim float32 vector (File 11 §11.7.4). The
// MVP default is a deterministic local embedder (L10-003) so the vector store is
// offline-testable; a real OpenAI/Ollama embedder plugs behind this interface.
// Memory imports only event + stdlib, so the embedder lives here (no infra SDK).
type Embedder interface {
	// Embed returns one vector per input text, in order. An empty input returns
	// an empty slice; a failed embedding returns an error (Retrieve degrades to
	// nil rather than panic).
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
