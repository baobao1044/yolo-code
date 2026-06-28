// The Context Engine (File 06 §6.1/§6.4). Build gathers the seven inputs,
// ranks them, compresses them, allocates the token budget, and publishes
// context.built. gather() is the §6.1 input wiring; rank()/compress() live in
// their own files.
//
// Sprint 2 gather: open files (read from disk), project rules (AGENTS.md),
// and session history are real; git diff, repo graph, diagnostics, and
// preferences are no-op stubs (future layers). The two real inputs are enough
// to make the model see real file contents end-to-end through the headless
// demo.

package context

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	stdctx "context"

	"github.com/yolo-code/yolo/internal/event"
)

// Deps are the Engine's collaborators (File 06 §6.8), injected so tests can
// substitute stubs. Repo is the working-tree root; Open is the set of open
// file paths (relative to Repo) the user/model is looking at.
type Deps struct {
	Bus    *event.Bus
	Repo   string
	Open   []string
	Memory Memory
	Git    GitDiff
	Graph  Graph
	Diags  Diagnostics

	// SoftBudget is the byte budget compression pass 3 keeps under (File 06
	// §6.3). Zero → a generous default; tests set it small to force trimming.
	SoftBudget int
	// Window is the token window the budget is allocated from (File 06 §6.6.1).
	// Zero → a default; the Prompt Compiler also clamps.
	Window int
}

// Engine gathers, ranks, and compresses context into a ContextPackage.
type Engine struct {
	bus        *event.Bus
	repo       string
	open       []string
	memory     Memory
	git        GitDiff
	graph      Graph
	diags      Diagnostics
	softBudget int
	window     int
}

// New constructs an Engine from Deps, applying defaults for unset budgets.
func New(d Deps) *Engine {
	e := &Engine{
		bus: d.Bus, repo: d.Repo, open: d.Open,
		memory: d.Memory, git: d.Git, graph: d.Graph, diags: d.Diags,
		softBudget: d.SoftBudget, window: d.Window,
	}
	if e.softBudget <= 0 {
		e.softBudget = 1 << 20 // 1 MiB default soft budget
	}
	if e.window <= 0 {
		e.window = 128_000 // 128k default window
	}
	if e.memory == nil {
		e.memory = noopMemory{}
	}
	if e.git == nil {
		e.git = noopGitDiff{}
	}
	if e.graph == nil {
		e.graph = noopGraph{}
	}
	if e.diags == nil {
		e.diags = noopDiags{}
	}
	return e
}

// Build assembles a ContextPackage for a task (File 06 §6.1/§6.8): gather the
// seven inputs, rank, compress, allocate the budget, and publish context.built.
func (e *Engine) Build(ctx stdctx.Context, req ContextRequest) (*ContextPackage, error) {
	parts := e.gather(ctx, req)
	ranked := e.rank(parts, req)
	pkg := e.compress(ranked)
	pkg.Task = req.Task.ID
	// The current request (the user's goal for this task) is its own group,
	// ordered third after system + project (File 06 §6.6.2 order() reads
	// pkg.User[0].Text). The §6.4 ContextPackage struct omits a User field;
	// Build fills it from the task goal.
	pkg.User = []Part{{Kind: KindSystem, Source: "goal", Text: req.Task.Goal}}
	pkg.Budget = allocate(e.window)
	_ = e.bus.Publish(ctx, &event.ContextBuiltEvent{Task: event.TaskID(req.Task.ID)})
	return pkg, nil
}

// gather collects the seven context inputs (File 06 §6.1). Returns a flat slice
// of Parts tagged with their Kind; rank() will score and order them.
func (e *Engine) gather(ctx stdctx.Context, req ContextRequest) []Part {
	var parts []Part

	// 1. System: role + tool schemas placeholder. Always present.
	parts = append(parts, Part{
		Kind: KindSystem, Source: "<system>",
		Text: "You are yolo, a terminal coding agent. Use tools to inspect and edit the repo.",
	})

	// 2. Project: AGENTS.md + structure (real).
	if ag := e.readProjectRules(); ag != "" {
		parts = append(parts, Part{Kind: KindProject, Source: "AGENTS.md", Text: ag})
	}

	// 3. Conversation: ranked history (real — from session task history).
	parts = append(parts, e.gatherConversation(req)...)

	// 4. Files: open + retrieved files (real — open files read from disk).
	parts = append(parts, e.gatherFiles()...)

	// 5. Git diff (stub). 6. Repository graph (stub). 7. Diagnostics (stub).
	parts = append(parts, e.git.Diff(ctx, e.repo)...)
	parts = append(parts, e.graph.Symbols(ctx, e.repo, req.Task.Goal)...)
	parts = append(parts, e.diags.Current(ctx, e.repo)...)

	// 8. Preferences (stub) — slotted into its own group.
	parts = append(parts, e.memory.Preferences(ctx, string(req.Task.ID))...)

	// Mark explicit @-references in the goal against gathered file sources.
	markExplicit(parts, req.Task.Goal)

	return parts
}

// gatherFiles reads each open file from disk into a Part. Missing files are
// skipped (an open file may have been deleted).
func (e *Engine) gatherFiles() []Part {
	var parts []Part
	for _, rel := range e.open {
		full := filepath.Join(e.repo, rel)
		body, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		fi, _ := os.Stat(full)
		rec := time.Now()
		if fi != nil {
			rec = fi.ModTime()
		}
		parts = append(parts, Part{
			Kind: KindFile, Source: rel, Text: string(body), Recency: rec,
		})
	}
	return parts
}

// readProjectRules reads AGENTS.md from the repo root if present.
func (e *Engine) readProjectRules() string {
	body, err := os.ReadFile(filepath.Join(e.repo, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(body)
}

// gatherConversation projects the task's history into Conversation parts. Each
// entry becomes a part with its summary as text; the most recent first. Each
// part carries role="assistant" in Attr — history records what the agent did,
// so the Prompt Compiler (§6.6.2 order() reads h.Attr["role"]) emits them as
// assistant turns.
func (e *Engine) gatherConversation(req ContextRequest) []Part {
	if req.Task == nil {
		return nil
	}
	hist := req.Task.History
	parts := make([]Part, 0, len(hist))
	for _, h := range hist {
		parts = append(parts, Part{
			Kind: KindConversation, Source: "history#" + itoa(h.Seq),
			Text: h.Summary, Recency: h.At, Attr: map[string]string{"role": "assistant"},
		})
	}
	// Newest first so the ranker's recency signal orders naturally.
	sort.Slice(parts, func(i, j int) bool { return parts[i].Recency.After(parts[j].Recency) })
	return parts
}

// markExplicit sets Part.Explicit=true for any part whose Source is
// @-referenced in the goal (e.g. "fix @auth/login.go").
func markExplicit(parts []Part, goal string) {
	refs := explicitRefs(goal)
	if len(refs) == 0 {
		return
	}
	for i := range parts {
		if refs[parts[i].Source] {
			parts[i].Explicit = true
		}
	}
}

// explicitRefs parses @file references from the goal into a set. A reference
// runs from '@' to the next whitespace.
func explicitRefs(goal string) map[string]bool {
	refs := map[string]bool{}
	for _, tok := range strings.Fields(goal) {
		if strings.HasPrefix(tok, "@") {
			refs[strings.TrimPrefix(tok, "@")] = true
		}
	}
	return refs
}

// itoa avoids strconv for a tiny int→string used by history projection.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
