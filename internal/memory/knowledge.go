// Knowledge memory — accumulated experience, cross-session (§11.5.1): short
// lessons the agent learned (patterns, gotchas, success/failure insights),
// fed by task.completed / verify.fail / verify.pass events (the listener,
// §11.2). Distinct from the SemanticStore: that indexes code chunks; this
// indexes short prose insights. Both surface to the Context Engine through
// the same RAG group (§11.6.2 retrieval flow), distinguished by Attr.
//
// JSON-backed at root/knowledge.json for cross-session persistence (§11.3.3):
// an insight learned in session A is recalled in session B (the L10-005 exit
// bar, extended to Knowledge). Vectors are NOT persisted (they re-embed on
// load — the hash embedder is deterministic so the round-trip is stable).

package memory

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Insight is one accumulated lesson (§11.5.1): a short text, the event that
// taught it (source), an assigned seq, and when it was last referenced (LRU).
type Insight struct {
	Text     string    `json:"text"`
	Source   string    `json:"source"`   // "task.completed" | "verify.fail" | "verify.pass" | "task.failed"
	Seq      int       `json:"seq"`      // assigned at Record, monotonic
	LastUsed time.Time `json:"lastUsed"` // bumped on Retrieve hit
}

// KnowledgeStore holds accumulated insights, retrievable by semantic search.
// The embedder turns an insight's text into a vector so Retrieve can cosine-
// rank against a query; vectors are kept parallel to items (rebuilt on Load).
type KnowledgeStore struct {
	root    string
	embed   Embedder
	mu      sync.Mutex
	items   []Insight
	vectors [][]float32 // parallel to items; the embedding of Text
	nextSeq int
}

// NewKnowledgeStore returns a knowledge store rooted at dir (the persistence
// root; knowledge.json lives under it). A nil embedder falls back to the
// default hash embedder (dim 384).
func NewKnowledgeStore(dir string, emb Embedder) *KnowledgeStore {
	if emb == nil {
		emb = NewHashEmbedder(384)
	}
	return &KnowledgeStore{root: dir, embed: emb}
}

func (k *KnowledgeStore) path() string {
	return filepath.Join(k.root, "knowledge.json")
}

// Record appends an insight (§11.5.1). A duplicate text (the same lesson
// learned twice) is deduped — the existing entry's source is refreshed and no
// new entry is added. Idempotent + nil-safe.
func (k *KnowledgeStore) Record(_ context.Context, text, source string) {
	if k == nil || text == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.items {
		if k.items[i].Text == text {
			k.items[i].Source = source
			return
		}
	}
	k.nextSeq++
	k.items = append(k.items, Insight{
		Text:   text,
		Source: source,
		Seq:    k.nextSeq,
	})
	vecs, _ := k.embed.Embed(context.Background(), []string{text})
	if len(vecs) > 0 {
		k.vectors = append(k.vectors, vecs[0])
	} else {
		k.vectors = append(k.vectors, nil)
	}
}

// Retrieve returns the top-k insights whose embedding best matches the query
// (§11.5.1 retrieval). Hits are returned as KindRAG Parts (the same group code
// chunks surface in, §11.6.2) with Source "knowledge#<seq>" and the teaching
// event in Attr["source"]. LastUsed is bumped for hits (LRU). Budget-capped;
// non-positive similarities are filtered.
func (k *KnowledgeStore) Retrieve(ctx context.Context, query string, topK int) []Part {
	if k == nil || query == "" || topK <= 0 {
		return nil
	}
	k.mu.Lock()
	if len(k.items) == 0 {
		k.mu.Unlock()
		return nil
	}
	items := append([]Insight(nil), k.items...)
	vecs := append([][]float32(nil), k.vectors...)
	k.mu.Unlock()

	qvecs, _ := k.embed.Embed(ctx, []string{query})
	if len(qvecs) == 0 {
		return nil
	}
	q := qvecs[0]
	type hit struct {
		seq int
		sim float64
	}
	hits := make([]hit, len(items))
	for i := range items {
		hits[i] = hit{seq: items[i].Seq, sim: cosine(q, vecs[i])}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].sim > hits[b].sim })

	kept := make([]hit, 0, topK)
	for _, h := range hits {
		if len(kept) >= topK {
			break
		}
		if h.sim <= 0 {
			break
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return nil
	}

	now := time.Now()
	keptSeqs := make(map[int]bool, len(kept))
	for _, h := range kept {
		keptSeqs[h.seq] = true
	}
	k.mu.Lock()
	for i := range k.items {
		if keptSeqs[k.items[i].Seq] {
			k.items[i].LastUsed = now
		}
	}
	k.mu.Unlock()

	out := make([]Part, 0, len(kept))
	for _, h := range kept {
		for _, it := range items {
			if it.Seq == h.seq {
				out = append(out, Part{
					Kind:   KindRAG,
					Source: "knowledge#" + strconv.Itoa(it.Seq),
					Text:   it.Text,
					Score:  h.sim,
					Attr:   map[string]string{"source": it.Source},
				})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// All returns a copy of the insights in seq order (for introspection / tests).
func (k *KnowledgeStore) All() []Insight {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]Insight(nil), k.items...)
}

// Persist writes the insights to knowledge.json (cross-session, §11.3.3).
// Vectors are NOT persisted — they re-embed on Load (the hash embedder is
// deterministic).
func (k *KnowledgeStore) Persist(_ context.Context) error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return writeJSON(k.path(), k.items)
}

// Load re-reads knowledge.json and re-embeds the insights (§11.3.3). A missing
// file is not an error (no insights yet → empty store).
func (k *KnowledgeStore) Load(_ context.Context) error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var items []Insight
	if err := readJSON(k.path(), &items); err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	k.items = items
	k.nextSeq = 0
	for _, it := range items {
		if it.Seq > k.nextSeq {
			k.nextSeq = it.Seq
		}
	}
	// Re-embed all loaded insights in one batch.
	k.vectors = make([][]float32, len(items))
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	vecs, _ := k.embed.Embed(context.Background(), texts)
	for i := range vecs {
		k.vectors[i] = vecs[i]
	}
	return nil
}
