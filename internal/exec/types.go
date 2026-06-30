// The tool vocabulary for Layer 7 (File 08 §8.1.2 / §8.2 / §8.6.4): the Tool
// interface every built-in and MCP tool implements, the ToolCall the
// Cognitive Core hands the dispatcher, the raw ToolOutput a tool produces,
// and the structured Observation the normalizer derives from it.
//
// Per the import matrix (File 15 §15.15.2), exec may not import cognitive,
// session, or runtime — so these types live here, and the composition root
// (cmd/yolo) adapts a cognitive.ToolCall into exec.ToolCall when wiring the
// engine. The Task field carries an event.TaskID so Dispatch can publish tool
// events with the right causal id without reaching into the session package.

package exec

import (
	"context"
	"time"

	"github.com/baobao1044/yolo-code/internal/event"
)

// ToolCall is the unit of work the Cognitive Core emits (File 08 §8.3). Tool
// is the registered name; Args is the raw JSON args object (a json.RawMessage
// carried as []byte); Reason is the model's one-line justification (shown in
// the HITL bar, §8.5.3); Task is the causal task id, threaded from the
// session context by the composition root so exec never imports session.
type ToolCall struct {
	Tool   string
	Args   []byte
	Reason string
	Task   event.TaskID
}

// ToolInput is what a tool's Run receives. Args mirrors the call's args; the
// sandbox/cwd are wired by later tickets as tools that need them (Read, Bash)
// land. Kept minimal here so L7-001's Echo demonstrator can run without a
// sandbox.
type ToolInput struct {
	Args []byte
}

// ToolOutput is the raw, unnormalized result of a tool run (File 08 §8.6.1).
// The Normalizer (L7-007) turns this into a redacted, truncated Observation.
type ToolOutput struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Summary  string
	Files    []string // paths the tool mutated (for memory invalidation)
}

// Observation is the structured, publishable result (File 08 §8.6.4): what
// the context engine, verify engine, and memory see after a tool ran. The
// Summary is the single line that survives history trimming (File 06 pass 1);
// FromPatch marks observations produced by the Patch Engine (File 10).
type Observation struct {
	Tool      string
	Stdout    string
	Stderr    string
	ExitCode  int
	Summary   string
	Truncated bool
	Bytes     int
	Files     []string
	FromPatch bool
}

// Risk classes for HITL (File 08 §8.5.1). Defined as event.Risk values so the
// wire contract stays in event (File 03) while exec names the concrete
// constants the sandbox and dispatcher compare against.
const (
	RiskLow      event.Risk = "low"
	RiskMedium   event.Risk = "medium"
	RiskHigh     event.Risk = "high"
	RiskCritical event.Risk = "critical"
)

// riskLevel returns 0..3 for the risk ladder (low → critical). Used by the
// dispatcher to decide whether a call needs approval (§8.5).
func riskLevel(r event.Risk) int {
	switch r {
	case RiskLow:
		return 0
	case RiskMedium:
		return 1
	case RiskHigh:
		return 2
	case RiskCritical:
		return 3
	}
	return 0
}

// Tool is the interface every static built-in and MCP tool implements (File
// 08 §8.1.2). Name is the registry key; Metadata carries permission/timeout/
// cost for the sandbox and cost controller; Schema validates args before
// Run; Risk classifies the call for the HITL gate; Run does the work under a
// context the dispatcher times out.
type Tool interface {
	Name() string
	Metadata() Metadata
	Schema() Schema
	Risk(call ToolCall) event.Risk
	Run(ctx context.Context, in ToolInput) (ToolOutput, error)
}

// Metadata is the rich per-tool descriptor (File 08 §8.2): what the sandbox
// pre-authorizes (Permission), what the cost controller weights (Cost), the
// per-tool timeout cap (Timeout), and the human-facing category/description
// the <tools> block advertises. The input schema lives on Tool.Schema() (the
// canonical source); an output schema is deferred until a ticket needs it.
type Metadata struct {
	Permission  Permission
	Timeout     time.Duration
	Cost        CostHint
	Category    string
	Description string
}

// Permission declares what a tool may touch (File 08 §8.2). FS is the
// filesystem access class; Net flags a tool that connects (gated by the
// network policy, L7-005); Exec flags a tool that spawns processes; Secret
// marks a tool whose output may carry secret-shaped data (always redacted,
// §8.4.5).
type Permission struct {
	FS     FSAccess
	Net    bool
	Exec   bool
	Secret bool
}

// FSAccess is the filesystem access class (File 08 §8.2): none / read / write.
type FSAccess string

const (
	FSNone  FSAccess = "none"
	FSRead  FSAccess = "read"
	FSWrite FSAccess = "write"
)

// CostHint is the relative cost a tool reports for the cost controller (File
// 08 §8.2). Cheap tools (Read, Grep) run freely; expensive ones (Docker,
// Browser) are weighted heavier in the ledger.
type CostHint int

const (
	CostCheap CostHint = iota
	CostMedium
	CostExpensive
)

// Schema is a hand-rolled minimal JSON-schema subset (stdlib-only, no external
// jsonschema dependency yet — a deliberate later decision). L7-001 enforces
// required keys; per-property type checks are added by a later ticket if a
// tool's schema needs them.
type Schema struct {
	Type     string
	Required []string
}
