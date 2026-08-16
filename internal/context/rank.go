// Relevance scoring (File 06 §6.2). recency, proximity and explicit are real;
// semantic is keyword overlap standing in for the RAG cosine; centrality needs
// a repo graph that does not exist and is therefore absent from the blend
// rather than stubbed to 0. The blend is the §6.2.2 weighted sum over the
// signals that are live, with no renormalisation: a signal that cannot be
// measured contributes nothing and takes nothing away (see rank).

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
// score order. RAG parts (retrieved code chunks, File 11 §11.6) carry their
// cosine similarity in Score from the SemanticStore; the blend's recency/
// proximity/centrality signals don't apply to semantic chunks, so a RAG part is
// scored from its retrieval similarity plus the explicit bonus rather than
// being re-blended — but the similarity is normalised across the retrieved set
// first (see normalisedCosine), because a raw cosine is not on the [0,1] scale
// the rest of the blend is.
//
// The file weights sum to 0.85, not 1: centrality (repo-graph PageRank,
// §6.2.3) needs a graph store that does not exist, so its 0.15 is simply
// unearnable. An earlier version divided the blend by that 0.85 to close the
// gap, arguing a file matching perfectly must reach the same 1.0 a KindRAG
// cosine can. That was wrong twice: the division scaled *every* file part up
// by 1.176, so it inflated the partial matches (which are almost all of them)
// far more often than it rescued a perfect one; and the default embedder is a
// hashing vectorizer over literal tokens (internal/memory/embed.go), whose
// goal↔chunk cosines measure ~0.05–0.30 in practice, so the 0.15 of headroom
// costs file parts nothing real.
//
// Signals that cannot be measured abstain: they add 0 and their weight is not
// redistributed. Redistributing would raise a part's score for carrying *less*
// information, which is the same error as the phantom maximum proximity used
// to return — just pointing the other way.
func (e *Engine) rank(parts []Part, req ContextRequest) []Part {
	cosMax := maxRetrievalScore(parts)
	out := make([]Scored, len(parts))
	for i, p := range parts {
		var s float64
		if p.Kind == KindRAG {
			// 0.90 rather than 1.00 so the explicit bonus keeps its own 0.10 and
			// the result stays inside the [0,1] §6.2.2 promises; the raw cosine
			// plus the bonus could exceed 1.
			s = 0.90*normalisedCosine(p.Score, cosMax) + 0.10*explicit(p)
		} else {
			s = 0.30*recency(p, time.Now()) +
				0.20*semantic(p, req) +
				0.10*explicit(p)
			if prox, known := proximity(p, req); known {
				s += 0.25 * prox
			}
		}
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

// maxRetrievalScore returns the largest cosine among the batch's KindRAG parts,
// or 0 if there are none (or none scored above zero).
func maxRetrievalScore(parts []Part) float64 {
	m := 0.0
	for _, p := range parts {
		if p.Kind == KindRAG && p.Score > m {
			m = p.Score
		}
	}
	return m
}

// normalisedCosine rescales one retrieval similarity against the best one in
// the same retrieved set, mapping the set onto [0,1]. An empty or all-zero set
// normalises to 0 — the division never happens, so a set with no signal
// contributes no signal.
//
// Why any rescaling. Every other term in the §6.2.2 blend is a fraction of
// something achievable: recency is a fraction of the 24h window, proximity a
// fraction of shared path segments, explicit is {0,1}, and semantic() is the
// fraction of the goal's tokens the text contains. Each genuinely reaches 1.
// A cosine does not. Its denominator carries ‖chunk‖, which for a real code
// chunk is dominated by identifiers and keywords the goal cannot contain, so
// the value is capped by the chunk's length rather than by its relevance.
// Measured against this repo's own embedder (internal/memory/embed.go, dim 384)
// for the goal "fix the Login function so bad passwords are rejected":
//
//	1.0000  the goal's own text
//	0.7647  a chunk that quotes the goal verbatim above two lines of code
//	0.1529  the actual Login body that fixes the bug
//	0.1021  the same Login body inside its real surrounding file
//
// Relevance did not fall by a third between the last two. Length did. Feeding
// that raw number into a blend where an untouched-but-recent file earns 0.30 on
// recency alone means the chunk holding the function under repair loses, every
// time, to whatever the user last happened to open — measured before this
// change as 0.1529 against two 400-byte files of literal "z" at 0.3000.
//
// Why set-relative rather than a constant. docs/rag/vector-store.md's standing
// claim is that swapping Deps.Embedder for a real model upgrades retrieval with
// no other change, because every downstream stage is embedder-agnostic. rank
// was the exception. A fixed scale factor calibrated to this hashing
// vectorizer's 0.10–0.22 working range would be wrong the day a real embedding
// model — whose unrelated pairs sit near 0.7 — is injected. Dividing by the
// set's own maximum survives any embedder, and it is a pure rescale, so it
// never reorders the retrieved set: the store already ordered it.
//
// What this deliberately does not do is second-guess retrieval. The best chunk
// in a non-empty set normalises to 1 even when the index held nothing good, and
// the ranker cannot tell the difference — it has no scale of its own to judge a
// cosine against, which is the whole problem. Deciding a chunk is too weak to
// return is the retrieval layer's job and it has the seam for it
// (LexicalStore.SetThreshold, §11.6.2 θ), which currently defaults to 0. That
// default is not an oversight waiting to be corrected — read SetThreshold's own
// comment before wiring a value. Measured against the shipped lexical embedder,
// the spec's 0.7 returns nothing for any goal, and even 0.2 keeps most hits for
// one query while emptying another over the same index.
//
// Note on the unearnable-weight argument. It was tempting to also divide each
// part by the weight it could actually earn, so a RAG part were scored out of
// 0.30 rather than 0.85. It does not apply, and it makes things worse: a
// constant 0 for an unmeasurable term is only a penalty when you divide by a
// total including that term's weight, and the blend does not divide. Dividing
// was measured: an unrelated recent file goes to 0.30/0.60 = 0.50 while the
// chunk goes to 0.102, widening the very gap this fixes from 2.0x to 4.9x. The
// scale of the cosine was the defect; the abstention rule below is not.
func normalisedCosine(score, setMax float64) float64 {
	if setMax <= 0 || score <= 0 {
		return 0
	}
	return score / setMax
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
// directory path). Same dir → 1; unrelated → 0. The bool reports whether the
// signal could be measured at all — proximity needs a file named by the goal to
// measure against, and the ordinary goal ("fix the login bug") names none. When
// it is false the caller must let the term abstain; scoring an unmeasured
// signal as 0 would penalise every file part against KindRAG parts, and (as the
// old fallback did) scoring it as 1 hands every file part a floor no retrieved
// chunk can reach.
func proximity(p Part, req ContextRequest) (float64, bool) {
	if p.Kind != KindFile || req.Task == nil {
		return 0, false
	}
	goalFile, ok := extractedFileFromGoal(req.Task.Goal)
	if !ok {
		return 0, false
	}
	goalDir, pDir := dirOf(goalFile), dirOf(p.Source)
	if goalDir == pDir {
		return 1, true // same directory — including both at the repo root
	}
	if goalDir == "" || pDir == "" {
		return 0, true // one at the root, one nested: no shared segment
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
	return float64(shared) / float64(max(len(gSegs), len(pSegs))), true
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

// explicit ∈ {0,1}: whether the user @-referenced this part's source.
func explicit(p Part) float64 {
	if p.Explicit {
		return 1
	}
	return 0
}

// extractedFileFromGoal returns the file the goal names — an @-reference first,
// then any bare path token — and whether it found one. There is deliberately no
// fallback: the previous version returned the caller's own part source when the
// goal named nothing, which made every part its own reference point and turned
// proximity into a constant 1 for every file in the repo.
func extractedFileFromGoal(goal string) (string, bool) {
	for _, tok := range strings.Fields(goal) {
		if strings.HasPrefix(tok, "@") {
			return strings.TrimPrefix(tok, "@"), true
		}
	}
	// Otherwise any file path mentioned bare in the goal.
	for _, tok := range strings.Fields(goal) {
		if strings.Contains(tok, "/") && strings.Contains(tok, ".") {
			return tok, true
		}
	}
	return "", false
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
