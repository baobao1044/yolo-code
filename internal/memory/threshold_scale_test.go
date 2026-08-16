// The scale of §11.6.2's θ, and why the spec's number is not this store's
// number.
//
// LexicalStore.SetThreshold used to be documented as "a positive value (e.g.
// 0.7 from the spec) drops low-similarity noise". Nothing in cmd/yolo ever
// called it, so the sentence was never tested against the embedder that
// actually ships. Measured, 0.7 does not drop noise — it drops everything, and
// it does so silently, because Retrieve returning nil is exactly what an index
// with nothing relevant in it also returns. Wiring the spec value in good faith
// would have turned RAG off and looked like RAG finding nothing.
//
// This file pins the measurement so the sentence cannot come back.

package memory

import (
	"context"
	"testing"
)

// codeCorpus is a small fixed set of code-shaped chunks. Inline rather than
// indexed off disk on purpose: the point is a stable claim about the embedder's
// output scale, and a corpus that changes whenever the repo does could not make
// one.
func codeCorpus() map[string]string {
	return map[string]string{
		"auth/login.go": `func Login(ctx context.Context, user, pass string) (*Session, error) {
	u, err := s.store.FindUser(ctx, user)
	if err != nil { return nil, err }
	if !u.CheckPassword(pass) { return nil, ErrBadCredentials }
	return s.newSession(ctx, u)
}`,
		"auth/handler.go": `func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, err := h.login.Login(r.Context(), r.FormValue("user"), r.FormValue("pass"))
	if err != nil { http.Error(w, "unauthorized", 401); return }
	h.write(w, sess)
}`,
		"store/user.go": `func (s *Store) FindUser(ctx context.Context, name string) (*User, error) {
	row := s.db.QueryRowContext(ctx, selectUser, name)
	var u User
	return &u, row.Scan(&u.ID, &u.Name, &u.Hash)
}`,
		"render/table.go": `func renderTable(cols []Column, rows [][]string) string {
	var b strings.Builder
	for _, c := range cols { b.WriteString(pad(c.Title, c.Width)) }
	return b.String()
}`,
	}
}

func loadCorpus(t *testing.T, s *LexicalStore) {
	t.Helper()
	ctx := context.Background()
	for path, body := range codeCorpus() {
		s.Reindex(ctx, path, []byte(body))
	}
	if s.Size() == 0 {
		t.Fatal("corpus indexed to nothing; the rest of this file measures a store that isn't there")
	}
}

// TestSpecThetaEmptiesLexicalRetrieval is the headline. θ=0.7 is the number
// §11.6.2 names and the number the old comment recommended; against the shipped
// hashed-term-frequency embedder it returns nothing at all, for a query written
// to match one of the chunks almost word for word.
//
// The assertion is deliberately two-sided. Proving 0.7 returns nothing would be
// worthless on its own — a broken store returns nothing at every θ. The θ=0 arm
// proves the corpus is retrievable, so the emptiness at 0.7 is the threshold's
// doing and not the fixture's.
func TestSpecThetaEmptiesLexicalRetrieval(t *testing.T) {
	ctx := context.Background()
	s := NewLexicalStoreWith(NewHashEmbedder(256))
	loadCorpus(t, s)

	const goal = "fix the login handler so bad credentials return an error"

	s.SetThreshold(0)
	baseline := s.Retrieve(ctx, goal, 10)
	if len(baseline) == 0 {
		t.Fatal("the corpus retrieves nothing even at θ=0, so this file cannot measure θ")
	}

	s.SetThreshold(0.7)
	if got := s.Retrieve(ctx, goal, 10); len(got) != 0 {
		t.Errorf("θ=0.7 returned %d chunks; this test exists because it returned 0. "+
			"If the embedder's scale genuinely changed, re-measure and rewrite "+
			"SetThreshold's doc comment — do not just relax this assertion", len(got))
	} else {
		t.Logf("θ=0 → %d chunks, θ=0.7 → 0. The spec's θ is an off switch on this embedder, "+
			"not a noise filter", len(baseline))
	}
}

// TestLexicalThresholdIsGoalDependent is the subtler half, and the reason the
// answer is not "pick a smaller constant". A threshold low enough to look
// harmless already discriminates between two queries against the same index:
// one keeps hits, the other loses all of them. Absolute cosine has no fixed
// meaning across queries on a lexical embedder — short queries sharing few
// tokens score low however relevant they are — so no single constant serves
// every goal, and a filter that has to be relative is a design change rather
// than a config value.
func TestLexicalThresholdIsGoalDependent(t *testing.T) {
	ctx := context.Background()
	s := NewLexicalStoreWith(NewHashEmbedder(256))
	loadCorpus(t, s)

	// Heavy lexical overlap with auth/login.go.
	const strong = "login user pass session store FindUser CheckPassword error"
	// A real question about the same code that happens to share almost no
	// literal tokens with it. Relevance is unchanged; cosine is not.
	const weak = "why is sign-in rejecting valid people"

	s.SetThreshold(0)
	strongBase := len(s.Retrieve(ctx, strong, 10))
	weakBase := len(s.Retrieve(ctx, weak, 10))
	if strongBase == 0 {
		t.Fatal("even the token-matched query retrieves nothing at θ=0")
	}
	if weakBase == 0 {
		t.Skip("the low-overlap query already retrieves nothing at θ=0 on this " +
			"corpus, so there is no threshold effect left to demonstrate")
	}

	// A value that reads as cautious next to the spec's 0.7.
	s.SetThreshold(0.2)
	strongAt := len(s.Retrieve(ctx, strong, 10))
	weakAt := len(s.Retrieve(ctx, weak, 10))

	t.Logf("θ=0.2: token-matched %d→%d, low-overlap %d→%d", strongBase, strongAt, weakBase, weakAt)
	if strongAt <= weakAt {
		t.Errorf("expected one absolute threshold to treat two queries over the SAME index "+
			"very differently (that is the finding): token-matched kept %d, low-overlap kept %d. "+
			"If this no longer holds, the embedder's behaviour changed and SetThreshold's "+
			"doc comment needs re-measuring", strongAt, weakAt)
	}
}
