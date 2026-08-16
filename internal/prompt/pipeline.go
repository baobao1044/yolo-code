package prompt

import (
	"strconv"

	econtext "github.com/baobao1044/yolo-code/internal/context"
)

// Trimmer applies the §6.7 trimming passes to over-budget groups. Sprint 2
// implements the cheap passes (collapse tool outputs, hard cut); the LLM
// summarization pass (§6.7.2 pass 3) is deferred to Sprint 3. applyBudget
// drives it through Compiler.trimmer — the passes live here so the type means
// what its name says rather than being constructed and ignored.
type Trimmer struct {
	counter Counter
}

// dedup drops cross-group duplicate content (File 06 §6.5.1). A file that appears
// in both Files and Conversation (as a tool result) collapses; the higher-scored
// representation wins. Within Conversation, consecutive identical tool results
// collapse to one.
func (c *Compiler) dedup(pkg econtext.ContextPackage) econtext.ContextPackage {
	// Track file sources already present in the Files group; drop any
	// Conversation part whose source matches a kept File (the file body is the
	// more complete representation; §6.5.1 "higher-scored representation wins").
	// Layer 4 scores Files above same-source Conversation parts in practice
	// (recency/proximity favor the file), so keeping the File is correct.
	fileSrcs := map[string]bool{}
	for _, p := range pkg.Files {
		if p.Source != "" {
			fileSrcs[p.Source] = true
		}
	}
	// Filter into a fresh slice, never in place: Compile takes the package by
	// value but pkg.Conversation shares its backing array with the caller's, so
	// pkg.Conversation[:0] would overwrite the caller's elements through it —
	// leaving them a slice of the original length whose first element is gone
	// and whose last is a duplicate. CompilePackage takes a *ContextPackage,
	// which advertises exactly the reuse that would make that visible.
	conv := make([]econtext.Part, 0, len(pkg.Conversation))
	for _, p := range pkg.Conversation {
		if p.Source != "" && fileSrcs[p.Source] {
			continue
		}
		conv = append(conv, p)
	}
	pkg.Conversation = conv

	// Within Conversation, collapse consecutive identical tool results
	// (§6.5.1). "Identical" means same Text; collapse keeps the first.
	cleaned := make([]econtext.Part, 0, len(pkg.Conversation))
	var prevText string
	for _, p := range pkg.Conversation {
		if p.Text == prevText {
			continue
		}
		cleaned = append(cleaned, p)
		prevText = p.Text
	}
	pkg.Conversation = cleaned
	return pkg
}

// summarize compresses long tool outputs / old turns (File 06 §6.5.2). Long tool
// outputs already carry a 1-line summary (File 08 §8.5); old turns' verbose
// reasoning is dropped, leaving the final statement. No LLM call here. Sprint 2
// is a no-op: Layer 4 supplies the summaries it has, and File 08 doesn't exist
// yet, so the pass-through preserves the §6.5.2 pipeline shape for a later
// sprint to fill.
func (c *Compiler) summarize(pkg econtext.ContextPackage) econtext.ContextPackage {
	return pkg
}

// applyBudget enforces the §6.6.1 budget across groups (File 06 §6.6). Groups
// over their allocated tokens are trimmed (conversation-focused, §6.7.2). The
// never-trimmed parts (system prompt, current user message, @-referenced
// files, most recent tool result) are preserved (§6.7.3).
//
// Sprint 2 implements the cheap passes only: it computes each group's token
// count against its budget slot and trims the trimmable groups (files, then
// oldest conversation turns) until the total fits the window. The LLM-driven
// summarization pass (§6.7.2 pass 3) is deferred to Sprint 3. The trimming
// order is cheapest-first per §6.7.2: drop retrieved files first (pass 1/2
// equivalents — they re-read from disk next turn), then recalled preferences,
// then hard-cut conversation (pass 4: keep system + the most recent turns).
// Every group order() emits is counted here; a group that ships but is not
// budgeted is a hole in the arithmetic that grows with use.
// The second return value reports what this stage did, for Compile to publish
// as a TokenBudgetEvent. Returned rather than published from here because the
// other three pipeline stages perform no I/O and every one of applyBudget's
// tests calls it directly — keeping the bus at the top of Compile means the
// arithmetic stays testable without one.
func (c *Compiler) applyBudget(pkg econtext.ContextPackage) (econtext.ContextPackage, budgetReport) {
	b := pkg.Budget
	rep := budgetReport{window: b.Window}

	// Count current tokens per group, using the rendered wire text where a group
	// gets a section tag (system, project, preferences, retrieved files) — the
	// model sees the rendered text, so the budget must too. Conversation turns
	// and the user message are emitted bare, so they're counted raw.
	//
	// Counted before the unbudgeted guard rather than after. It is pure work, and
	// hoisting it means Used is measured on every path — so a report from an
	// unbudgeted compile says "window 0, used 4210" instead of leaving the reader
	// to guess whether 0 means empty or unmeasured. Window <= 0 already carries
	// the "no cap" fact by itself; one zero meaning two things is the defect this
	// file keeps documenting.
	sysTok := c.counter.Count(render("<system>", pkg.System))
	projTok := c.counter.Count(render("<project>", pkg.Project))
	prefTok := c.counter.Count(render("<preferences>", pkg.Preferences))
	userTok := c.groupTokens(pkg.User)
	retrTok := c.counter.Count(render("<files>", append(append([]econtext.Part{}, pkg.Files...), append(pkg.Graph, pkg.Diagnostics...)...)))
	convTok := c.groupTokens(pkg.Conversation)
	ragTok := c.counter.Count(render("<rag>", pkg.RAG))

	total := sysTok + projTok + prefTok + userTok + convTok + retrTok + ragTok
	rep.used = total

	// Window <= 0 means UNBUDGETED, not exhausted: no window was set, so there
	// is no cap and nothing is trimmed. The opposite reading — 0 means "no room
	// left" — would silently strip every trimmable group from every prompt built
	// without a window, which is the unbudgeted path the tests and the corpus
	// fixture both take. Pinned by TestApplyBudgetWindowZeroMeansNoCap, because
	// a zero limit quietly meaning the wrong thing is this codebase's most
	// frequently repeated defect and this guard was previously untested.
	if b.Window <= 0 {
		return pkg, rep // no budget to enforce (unbudgeted path)
	}
	slots := effectiveSlots(b)

	if total <= b.Window {
		return pkg, rep // fits; no trimming needed
	}

	// Pass A — trim retrieved files under their slot. Files are re-readable from
	// disk next turn, so they're the cheapest to drop. Keep @-referenced files
	// (§6.7.3) by keeping the highest-scored ones (Layer 4 already ranks them
	// first within the group). RAG chunks trim under their own slot next — they
	// re-retrieve from the vector store next turn, so they're also cheap to drop.
	retrBefore := len(pkg.Files) + len(pkg.Graph) + len(pkg.Diagnostics)
	pkg.Files = c.trimmer.trimGroup(pkg.Files, slots.files)
	pkg.Graph = c.trimmer.trimGroup(pkg.Graph, slotNoAllocation) // graph/diagnostics have no slot of their own
	pkg.Diagnostics = c.trimmer.trimGroup(pkg.Diagnostics, slotNoAllocation)
	if n := retrBefore - (len(pkg.Files) + len(pkg.Graph) + len(pkg.Diagnostics)); n > 0 {
		pkg.Files = append(pkg.Files, elided(n, "retrieved files"))
		rep.drop("retrieved files", n)
	}
	ragBefore := len(pkg.RAG)
	pkg.RAG = c.trimmer.trimGroup(pkg.RAG, slots.rag)
	if n := ragBefore - len(pkg.RAG); n > 0 {
		pkg.RAG = append(pkg.RAG, elided(n, "retrieved chunks"))
		rep.drop("retrieved chunks", n)
	}
	retrTok = c.groupTokens(pkg.Files) + c.groupTokens(pkg.Graph) + c.groupTokens(pkg.Diagnostics)
	ragTok = c.groupTokens(pkg.RAG)

	// Recompute; if it fits now, done.
	total = sysTok + projTok + prefTok + userTok + convTok + retrTok + ragTok
	rep.used = total
	if total <= b.Window {
		return pkg, rep
	}

	// Pass A2 — trim recalled preferences. The group is emitted under
	// <preferences> and shipped, so it is budgeted like any other; it was
	// previously in neither the total nor the remaining subtraction, which made
	// it the one group that could grow without limit. It trims after files/RAG
	// (a preference is not re-derivable next turn the way a file re-read is) and
	// before the conversation hard cut. §6.7.3's never-trimmed set is the system
	// prompt, the current user message, @-referenced files and the most recent
	// tool result — preferences are not in it, and they were only untrimmed by
	// omission.
	prefBefore := len(pkg.Preferences)
	pkg.Preferences = c.trimmer.trimGroup(pkg.Preferences, slots.pref)
	if n := prefBefore - len(pkg.Preferences); n > 0 {
		pkg.Preferences = append(pkg.Preferences, elided(n, "recalled preferences"))
		rep.drop("recalled preferences", n)
	}
	prefTok = c.counter.Count(render("<preferences>", pkg.Preferences))

	total = sysTok + projTok + prefTok + userTok + convTok + retrTok + ragTok
	rep.used = total
	if total <= b.Window {
		return pkg, rep
	}

	// Pass B — hard-cut conversation (§6.7.2 pass 4): keep the most recent turns.
	// The conversation is ordered newest-first by Layer 4, so keep the front.
	// Reserve whatever room remains for conversation after the never-trimmed and
	// already-trimmed groups.
	remaining := b.Window - sysTok - projTok - prefTok - userTok - retrTok - ragTok
	if remaining < 0 {
		remaining = 0
	}
	convBefore := len(pkg.Conversation)
	pkg.Conversation = c.trimmer.trimConversation(pkg.Conversation, remaining)
	if n := convBefore - len(pkg.Conversation); n > 0 {
		rep.drop("conversation turns", n)
	}
	// The hard cut is the one pass with no elided() marker — trimConversation
	// keeps whole turns and a marker between them would read as a turn. The
	// event is therefore the only record that it happened at all, which is the
	// clearest single argument for this event existing.
	rep.used = sysTok + projTok + prefTok + userTok + retrTok + ragTok + c.groupTokens(pkg.Conversation)
	return pkg, rep
}

// budgetReport is what applyBudget observed, on its way to a TokenBudgetEvent.
// Unexported and value-typed: it is a courier between two functions in this
// file, not API. dropped stays nil until something is actually dropped, which
// is the common case and keeps the published event free of an empty map.
type budgetReport struct {
	window  int
	used    int
	dropped map[string]int
}

func (r *budgetReport) drop(group string, n int) {
	if r.dropped == nil {
		r.dropped = make(map[string]int, 4)
	}
	r.dropped[group] += n
}

// slotNoAllocation is the slot value meaning "this group has no allocation of
// its own — drop it when the prompt is over budget". Spelled out because a bare
// 0 limit reading as its own opposite ("no cap") is the defect that keeps
// recurring in this tree; trimGroup's contract for it is pinned by
// TestTrimGroupSlotZeroDropsEverything.
const slotNoAllocation = 0

// slots are the per-group caps applyBudget actually enforces. They come from
// Layer 4's Budget except when that Budget is degenerate — see effectiveSlots.
type slots struct{ files, rag, pref int }

// effectiveSlots reads Layer 4's allocation. It is a passthrough, and the fact
// that it can be one is the whole point of it still existing as a named
// function.
//
// It used to re-derive the §6.6.1 percentages here whenever Layer 4's Budget
// looked degenerate. That guard was real: allocate() floored the reply reserve
// at 1024 tokens and subtracted it before clamping avail to 0, so every window
// at or below 1024 came back with every cap 0, and Pass A dropped 100% of the
// retrieved context and RAG. A small-window model silently got a prompt with
// none of what it asked for. Layer 4 has since clamped the reserve to half the
// window and floored each share at 1, so the degenerate Budget no longer
// exists to guard against.
//
// Two things kept the guard from simply being deleted, and both are now closed.
// Its condition was Reserve < Window, which is false at window == 1 (reserveFor
// floors a positive window's reserve at 1), so the fallback stayed reachable at
// exactly one input — where both branches return all-zeros anyway, since the
// fallback's own 17%/8%/8% truncate to 0 on a window of 1. And pref borrowed
// b.Project because Budget had no Preferences field; it has one now, allocated
// from the same percentage under the same ceiling, so the two agree today and
// b.Preferences is the one that stays right if either ever changes.
//
// TestEffectiveSlotsMatchTheRetiredFallback pins the equivalence across the
// window range instead of leaving it as an argument.
func effectiveSlots(b econtext.Budget) slots {
	return slots{files: b.Files, rag: b.RAG, pref: b.Preferences}
}

// elided is the marker part render emits in place of trimmed content. Trimming
// must never be silent: without it a model cannot tell "nothing was retrieved"
// from "what was retrieved did not fit", and neither can anyone reading the
// wire. This is a stopgap for the missing §6.6.3 TokenBudgetEvent (types.go).
func elided(n int, what string) econtext.Part {
	return econtext.Part{
		Text: "[" + strconv.Itoa(n) + " " + what + " omitted: over token budget]",
	}
}

// trimGroup keeps the leading (highest-scored, since Layer 4 ranks descending)
// parts until the slot is full; drops the rest. A slot of 0 — or any negative
// slot — means the group has no allocation and is dropped entirely; applyBudget
// depends on exactly that for graph/diagnostics. Any positive slot admits at
// least the first part, even when that part alone overflows the slot.
func (t *Trimmer) trimGroup(parts []econtext.Part, slot int) []econtext.Part {
	if slot <= 0 {
		return nil
	}
	var kept []econtext.Part
	tokens := 0
	for _, p := range parts {
		n := t.counter.Count(p.Text)
		if tokens+n > slot && len(kept) > 0 {
			break
		}
		// Admit the first part even if it alone exceeds the slot, mirroring
		// Layer 4's compress edge guard (avoid an empty group).
		tokens += n
		kept = append(kept, p)
	}
	return kept
}

// trimConversation keeps the most recent turns (parts are newest-first) until
// the remaining token budget is spent. As in trimGroup, a remaining of 0 means
// there is no room left, so nothing is kept.
func (t *Trimmer) trimConversation(parts []econtext.Part, remaining int) []econtext.Part {
	if remaining <= 0 {
		return nil
	}
	var kept []econtext.Part
	tokens := 0
	for _, p := range parts {
		n := t.counter.Count(p.Text)
		if tokens+n > remaining && len(kept) > 0 {
			break
		}
		tokens += n
		kept = append(kept, p)
	}
	return kept
}

// groupTokens sums the token count of a group's parts.
func (c *Compiler) groupTokens(parts []econtext.Part) int {
	n := 0
	for _, p := range parts {
		n += c.counter.Count(p.Text)
	}
	return n
}

// order emits the Messages in the deterministic §6.6.2 order: system, project,
// user (current request), retrieved context (files + graph + diagnostics),
// then conversation turns. Each group is rendered with its stable wire-format
// section tag. Empty groups are omitted (render returns "" for them, and the
// retrieved-context block is skipped entirely when it has no parts).
func (c *Compiler) order(pkg econtext.ContextPackage) []Message {
	var msgs []Message

	// 1. System (role + tool schemas + rules), role "system".
	if len(pkg.System) > 0 {
		msgs = append(msgs, Message{Role: "system", Content: render("<system>", pkg.System)})
	}
	// 2. Project rules (AGENTS.md), role "system".
	if len(pkg.Project) > 0 {
		msgs = append(msgs, Message{Role: "system", Content: render("<project>", pkg.Project)})
	}
	// 2b. Recalled preferences (File 11 §11.8), role "system". The Preferences
	// group is populated by Layer 4's Memory seam (the context.MemoryAdapter
	// surfaces user prefs + project memory here). Sprint 2 left this slot empty
	// (the noop Memory stub returns none), so order() never emitted it; L10-006
	// wires the real memory.Store behind the seam, so the group now carries
	// recalled memory that must reach the model. It is ordered within the system
	// block — persistent guidance, like project rules — under its own stable
	// <preferences> tag so the parser round-trips it (§6.6.2).
	if len(pkg.Preferences) > 0 {
		msgs = append(msgs, Message{Role: "system", Content: render("<preferences>", pkg.Preferences)})
	}
	// 3. The current request (user goal), role "user". The §6.4 ContextPackage
	// omits a User field; Layer 4's Build fills it with the task goal. The
	// current message is ordered third (§6.6.2) and is never trimmed (§6.7.3).
	if len(pkg.User) > 0 {
		msgs = append(msgs, Message{Role: "user", Content: pkg.User[0].Text})
	}
	// 4. Retrieved context: files + graph + diagnostics, role "user", rendered
	// under one <files> tag. Omit entirely when there is nothing to show.
	retrieved := append(append([]econtext.Part{}, pkg.Files...), pkg.Graph...)
	retrieved = append(retrieved, pkg.Diagnostics...)
	if len(retrieved) > 0 {
		msgs = append(msgs, Message{Role: "user", Content: render("<files>", retrieved)})
	}
	// 4b. RAG: semantically retrieved code chunks (File 11 §11.6), role "user",
	// under a stable <rag> tag so the parser round-trips it. Each chunk carries
	// path/name/kind in Attr (set by the SemanticStore adapter); render emits
	// them as a labeled block. Omitted when the store returned no hits (the
	// noop-memory path).
	if len(pkg.RAG) > 0 {
		msgs = append(msgs, Message{Role: "user", Content: render("<rag>", pkg.RAG)})
	}
	// 5. Conversation turns, each its own message with the role the part
	// carries in Attr (default "assistant" — history records the agent's turns).
	for _, h := range pkg.Conversation {
		role := h.Attr["role"]
		if role == "" {
			role = "assistant"
		}
		msgs = append(msgs, Message{Role: role, Content: h.Text})
	}
	return msgs
}
