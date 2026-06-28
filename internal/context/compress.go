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
	var bytes int
	for _, p := range deduped {
		if bytes+len(p.Text) > e.softBudget && bytes > 0 {
			continue // budget hit; drop the rest
		}
		bytes += len(p.Text)
		assign(pkg, p)
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
	}
}
