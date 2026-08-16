// Compression (File 06 §6.3). Three cheap passes reduce ranked parts to fit a
// soft byte budget *before* the Prompt Compiler applies the hard token budget.
// No LLM is called here: summaries are the pre-computed ones; pass 3 keeps the
// top-K parts by score until the soft budget is met.

package context

// compress runs the three §6.3 passes on ranked parts and assembles a
// ContextPackage grouped by Kind. The ranked input is already score-sorted, so
// pass 3 (top-K) preserves score order within each group.
func (e *Engine) compress(ranked []Part) *ContextPackage {
	pkg := &ContextPackage{}

	// Pass 1: deduplicate parts covering the same file/symbol; keep the most
	// complete (highest score, which equals first occurrence after ranking).
	seen := map[string]bool{}
	deduped := make([]Part, 0, len(ranked))
	for _, p := range ranked {
		key := dedupKey(p)
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		deduped = append(deduped, p)
	}

	// Pass 2: summarize is a no-op in Sprint 2 — the execution engine (File 08
	// §8.5) supplies the 1-line summaries consumed here; until it exists, raw
	// text stands in. (The pass is explicit so the §6.3 shape is preserved.)

	// Pass 3: greedily keep highest-scored parts until the soft byte budget is
	// hit; drop the rest. Group the survivors into the package by Kind.
	//
	// The walk runs twice. §6.6.1 budgets the groups separately (Budget carries
	// a Files share and a RAG share), so no group may swallow another's whole
	// allocation: the first walk offers each Kind present its own highest-ranked
	// part, the second fills what is left in score order. A single walk let two
	// files the user happened to leave open consume the entire budget and
	// evicted every retrieved chunk — the one outcome the RAG group exists to
	// prevent, and invisible downstream because the group is simply empty.
	var bytes int
	kept := make([]bool, len(deduped))
	headOffered := map[PartKind]bool{}

	// Pass 3, step 0: the system frame is admitted before the walk and is not
	// charged to it. At SoftBudget 900 the <system> part — 1505 bytes, ranked
	// 0.05 because a role description shares almost no tokens with any specific
	// goal — was dropped in full while two 400-byte files of literal "z" were
	// kept. A run without the system prompt is not a degraded run; it is a
	// different agent, with no rules, no tool list, and no idea what it is.
	// §6.7.3's never-trimmed set (system prompt / current user message /
	// @-referenced files / most recent tool result) says the same thing, and
	// Layer 5 already honours it — Layer 4 was evicting the part before Layer 5
	// ever saw it.
	//
	// The exemption is exactly one part wide, and that bound is the point. A
	// blanket Kind-level exemption is the losing option here: it makes the
	// system group the one place a caller can put unbounded text that no budget
	// touches, and "the system prompt is protected" would then mean "anything
	// labelled system is protected", which is how a protection becomes a hole.
	// So the highest-ranked KindSystem part — the frame the engine itself
	// authors in gather() — is exempt, and any further system part competes for
	// the soft budget like anything else. Not charging the frame's bytes is the
	// other half: the soft budget exists to bound how much *gathered repository
	// state* is packed in, and the frame is not gathered state. Charging it
	// would have swapped one silent failure for another, since 1505 bytes
	// against 900 would leave the budget already spent and empty every other
	// group instead.
	for i, p := range deduped {
		if p.Kind == KindSystem {
			kept[i] = true
			headOffered[KindSystem] = true // spends the Kind's head slot
			break                          // deduped is score-ordered: this is the frame
		}
	}

	for _, headsOnly := range []bool{true, false} {
		for i, p := range deduped {
			if kept[i] {
				continue
			}
			if headsOnly {
				if headOffered[p.Kind] {
					continue // this Kind already had its turn
				}
				headOffered[p.Kind] = true
			}
			if bytes+len(p.Text) > e.softBudget && bytes > 0 {
				continue // budget hit; this part doesn't fit
			}
			bytes += len(p.Text)
			kept[i] = true
		}
	}
	// Assign in ranked order so each group stays score-sorted regardless of
	// which walk admitted a part.
	for i, p := range deduped {
		if kept[i] {
			assign(pkg, p)
		}
	}
	return pkg
}

// dedupKey returns the file/symbol identity a part collapses on. Files dedup by
// source path; conversation turns by source; others don't dedup (return "").
func dedupKey(p Part) string {
	switch p.Kind {
	case KindFile, KindProject:
		return string(p.Kind) + ":" + p.Source
	case KindConversation:
		return string(p.Kind) + ":" + p.Source
	}
	return ""
}

// assign places a part into its group slot on the package.
func assign(pkg *ContextPackage, p Part) {
	switch p.Kind {
	case KindSystem:
		pkg.System = append(pkg.System, p)
	case KindProject:
		pkg.Project = append(pkg.Project, p)
	case KindConversation:
		pkg.Conversation = append(pkg.Conversation, p)
	case KindFile:
		pkg.Files = append(pkg.Files, p)
	case KindGraph:
		pkg.Graph = append(pkg.Graph, p)
	case KindDiagnostics:
		pkg.Diagnostics = append(pkg.Diagnostics, p)
	case KindPreferences:
		pkg.Preferences = append(pkg.Preferences, p)
	case KindRAG:
		pkg.RAG = append(pkg.RAG, p)
	}
}
