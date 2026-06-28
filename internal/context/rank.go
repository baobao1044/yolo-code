// Relevance scoring (File 06 §6.2). Sprint 2 implements recency, proximity, and
// explicit signals for real; semantic (RAG cosine) and centrality (repo-graph
// PageRank) are stubbed (0) until their owning layers exist. The weighted blend
// is exactly §6.2.2.

package context

import (
	"sort"
	"strings"
	"time"
)

// Scored pairs a Part with its relevance score.
type Scored struct {
	Part  Part
	Score float64
}

// rank scores each part with the §6.2.2 blend and returns them in descending
// score order.
func (e *Engine) rank(parts []Part, req ContextRequest) []Part {
	out := make([]Scored, len(parts))
	for i, p := range parts {
		s := 0.30*recency(p, time.Now()) +
			0.25*proximity(p, req) +
			0.20*semantic(p, req) +
			0.15*centrality(p) +
			0.10*explicit(p)
		out[i] = Scored{Part: p, Score: s}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	res := make([]Part, len(out))
	for i, s := range out {
		s.Part.Score = s.Score
		res[i] = s.Part
	}
	return res
}

// recency ∈ [0,1]: how recently the part was touched. Newer → closer to 1.
// A zero time yields 0 (no signal); otherwise it decays over the last 24h.
func recency(p Part, now time.Time) float64 {
	if p.Recency.IsZero() {
		return 0
	}
	age := now.Sub(p.Recency)
	if age < 0 {
		age = 0
	}
	if age >= 24*time.Hour {
		return 0
	}
	return 1 - age.Hours()/24
}

// proximity ∈ [0,1]: how near the part's source is to the task's files (by
// directory path). Same dir → 1; unrelated → 0.
func proximity(p Part, req ContextRequest) float64 {
	if p.Kind != KindFile || req.Task == nil {
		return 0
	}
	goalDir := dirOf(extractedFileFromGoal(req.Task.Goal, p.Source))
	pDir := dirOf(p.Source)
	if goalDir == "" || pDir == "" {
		return 0
	}
	if goalDir == pDir {
		return 1
	}
	// Partial credit for shared path prefix segments.
	gSegs := strings.Split(goalDir, "/")
	pSegs := strings.Split(pDir, "/")
	shared := 0
	for i := 0; i < len(gSegs) && i < len(pSegs); i++ {
		if gSegs[i] == pSegs[i] {
			shared++
		} else {
			break
		}
	}
	return float64(shared) / float64(max(len(gSegs), len(pSegs)))
}

// semantic ∈ [0,1]: keyword-overlap stub for the goal↔part text. The real
// version (File 11) is a vector cosine; Sprint 2 uses token overlap so the
// ranker has a usable signal without the embedding store.
func semantic(p Part, req ContextRequest) float64 {
	if req.Task == nil || req.Task.Goal == "" || p.Text == "" {
		return 0
	}
	goalTokens := tokenize(strings.ToLower(req.Task.Goal))
	partTokens := tokenize(strings.ToLower(p.Text))
	if len(goalTokens) == 0 {
		return 0
	}
	hits := 0
	for t := range goalTokens {
		if partTokens[t] {
			hits++
		}
	}
	return float64(hits) / float64(len(goalTokens))
}

// centrality ∈ [0,1]: repo-graph PageRank weight (File 06 §6.2.3). Stubbed to
// 0 until the graph store (File 11) exists.
func centrality(p Part) float64 { _ = p; return 0 }

// explicit ∈ {0,1}: whether the user @-referenced this part's source.
func explicit(p Part) float64 {
	if p.Explicit {
		return 1
	}
	return 0
}

// extractedFileFromGoal returns the @-referenced file if the goal names one,
// else a fallback (the part's own source) so proximity has a reference point.
func extractedFileFromGoal(goal, fallback string) string {
	for _, tok := range strings.Fields(goal) {
		if strings.HasPrefix(tok, "@") {
			return strings.TrimPrefix(tok, "@")
		}
	}
	// Fall back to any file path mentioned bare in the goal.
	for _, tok := range strings.Fields(goal) {
		if strings.Contains(tok, "/") && strings.Contains(tok, ".") {
			return tok
		}
	}
	return fallback
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func tokenize(s string) map[string]bool {
	toks := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' || r == '/' || r == '(' || r == ')' || r == '"' || r == '\''
	}) {
		if len(f) > 2 { // skip very short tokens (noise)
			toks[f] = true
		}
	}
	return toks
}
