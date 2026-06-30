package prompt

import (
	econtext "github.com/baobao1044/yolo-code/internal/context"
)

// Trimmer applies the §6.7 trimming passes to over-budget groups. Sprint 2
// implements the cheap passes (collapse tool outputs, hard cut); the LLM
// summarization pass (§6.7.2 pass 3) is deferred to Sprint 3.
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
	conv := pkg.Conversation[:0]
	for _, p := range pkg.Conversation {
		if p.Source != "" && fileSrcs[p.Source] {
			continue
		}
		conv = append(conv, p)
	}
	pkg.Conversation = conv

	// Within Conversation, collapse consecutive identical tool results
	// (§6.5.1). "Identical" means same Text; collapse keeps the first.
	cleaned := pkg.Conversation[:0]
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
// equivalents — they re-read from disk next turn), then hard-cut conversation
// (pass 4: keep system + the most recent turns).
func (c *Compiler) applyBudget(pkg econtext.ContextPackage) econtext.ContextPackage {
	b := pkg.Budget
	if b.Window <= 0 {
		return pkg // no budget to enforce (unbudgeted path)
	}

	// Count current tokens per group, using the rendered wire text where a group
	// gets a section tag (system, project, retrieved files) — the model sees the
	// rendered text, so the budget must too. Conversation turns and the user
	// message are emitted bare, so they're counted raw.
	sysTok := c.counter.Count(render("<system>", pkg.System))
	projTok := c.counter.Count(render("<project>", pkg.Project))
	userTok := c.groupTokens(pkg.User)
	retrTok := c.counter.Count(render("<files>", append(append([]econtext.Part{}, pkg.Files...), append(pkg.Graph, pkg.Diagnostics...)...)))
	convTok := c.groupTokens(pkg.Conversation)

	total := sysTok + projTok + userTok + convTok + retrTok
	if total <= b.Window {
		return pkg // fits; no trimming needed
	}

	// Pass A — trim retrieved files under their slot. Files are re-readable from
	// disk next turn, so they're the cheapest to drop. Keep @-referenced files
	// (§6.7.3) by keeping the highest-scored ones (Layer 4 already ranks them
	// first within the group).
	pkg.Files = c.trimGroup(pkg.Files, b.Files)
	pkg.Graph = c.trimGroup(pkg.Graph, 0) // graph/diagnostics have no slot; drop fully if over
	pkg.Diagnostics = c.trimGroup(pkg.Diagnostics, 0)
	retrTok = c.groupTokens(pkg.Files) + c.groupTokens(pkg.Graph) + c.groupTokens(pkg.Diagnostics)

	// Recompute; if it fits now, done.
	total = sysTok + projTok + userTok + convTok + retrTok
	if total <= b.Window {
		return pkg
	}

	// Pass B — hard-cut conversation (§6.7.2 pass 4): keep the most recent turns.
	// The conversation is ordered newest-first by Layer 4, so keep the front.
	// Reserve whatever room remains for conversation after the never-trimmed and
	// already-trimmed groups.
	remaining := b.Window - sysTok - projTok - userTok - retrTok
	if remaining < 0 {
		remaining = 0
	}
	pkg.Conversation = c.trimConversation(pkg.Conversation, remaining)
	return pkg
}

// trimGroup keeps the leading (highest-scored, since Layer 4 ranks descending)
// parts until the slot is full; drops the rest. A slot of 0 drops everything.
func (c *Compiler) trimGroup(parts []econtext.Part, slot int) []econtext.Part {
	if slot <= 0 {
		return nil
	}
	var kept []econtext.Part
	tokens := 0
	for _, p := range parts {
		t := c.counter.Count(p.Text)
		if tokens+t > slot && len(kept) > 0 {
			break
		}
		// Admit the first part even if it alone exceeds the slot, mirroring
		// Layer 4's compress edge guard (avoid an empty group).
		tokens += t
		kept = append(kept, p)
	}
	return kept
}

// trimConversation keeps the most recent turns (parts are newest-first) until
// the remaining token budget is spent.
func (c *Compiler) trimConversation(parts []econtext.Part, remaining int) []econtext.Part {
	if remaining <= 0 {
		return nil
	}
	var kept []econtext.Part
	tokens := 0
	for _, p := range parts {
		t := c.counter.Count(p.Text)
		if tokens+t > remaining && len(kept) > 0 {
			break
		}
		tokens += t
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
