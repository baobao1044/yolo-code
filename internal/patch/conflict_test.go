// Tests for conflict detection (File 10 §10.3): before applying, the engine
// checks the patch won't fight the current state. L9-001 covered ErrNotFound
// and ErrAmbiguous; L9-003 adds anchor disambiguation (a multi-hit Search
// resolves via the surrounding Anchor context; still-ambiguous → reject) and
// the staleness race (file changed since the model read it → ErrStale, force
// a re-read). ErrConflict (concurrent agent overlap) is deferred to the
// Coordination Layer (File 12).

package patch

import (
	"strings"
	"testing"
	"time"
)

func TestAnchorDisambiguatesMultipleMatches(t *testing.T) {
	// "x" appears twice; an Anchor naming the surrounding context picks the
	// right one (File 10 §10.2.2). The anchor is text that must immediately
	// precede the Search hit.
	content := "func a() {\n\tx\n}\nfunc b() {\n\tx\n}\n"
	blocks := []Block{{
		Search:  "x",
		Replace: "y",
		Anchor:  "func b() {", // disambiguate: the x inside func b
	}}

	out, err := Apply(content, blocks)
	if err != nil {
		t.Fatalf("Apply(anchor) = %v, want nil (anchor resolves ambiguity)", err)
	}
	// The x inside func b is replaced; the x inside func a stays.
	if !strings.Contains(out, "func a() {\n\tx\n}") {
		t.Errorf("output = %q, want the func a x preserved", out)
	}
	if !strings.Contains(out, "func b() {\n\ty\n}") {
		t.Errorf("output = %q, want the func b x replaced", out)
	}
}

func TestAnchorStillAmbiguousRejects(t *testing.T) {
	// The anchor matches *both* hits (both functions contain the anchor text)
	// → still ambiguous → reject (File 10 §10.2.2 "still ambiguous → reject").
	content := "anchor\nx\nanchor\nx\n"
	blocks := []Block{{
		Search:  "x",
		Replace: "y",
		Anchor:  "anchor",
	}}

	_, err := Apply(content, blocks)
	if err == nil {
		t.Fatal("Apply(anchor matching both hits) = nil, want ErrAmbiguous (still ambiguous)")
	}
	if err != ErrAmbiguous && !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %q, want ErrAmbiguous", err.Error())
	}
}

func TestAnchorNotPresentRejects(t *testing.T) {
	// An anchor that doesn't surround any hit → reject (the model gave bad
	// context; don't guess which hit).
	content := "x\nx\n"
	blocks := []Block{{
		Search:  "x",
		Replace: "y",
		Anchor:  "this anchor isn't in the file",
	}}

	_, err := Apply(content, blocks)
	if err == nil {
		t.Fatal("Apply(anchor absent) = nil, want error (anchor doesn't disambiguate)")
	}
}

func TestStaleFileRejects(t *testing.T) {
	// The file was modified after the model read it (readTime < file mtime) →
	// ErrStale, forcing a re-read (File 10 §10.3). ApplyAt is given the read
	// time and the file's modified-at; a newer mtime means the Search might be
	// wrong now.
	content := "old text\n"
	blocks := []Block{{Search: "old text", Replace: "new text"}}
	// readTime is *before* the file's "modified at" → stale.
	out, err := ApplyAt(content, blocks, time.Now().Add(-time.Hour), time.Now())

	if err == nil {
		t.Fatalf("ApplyAt(stale) = %q, want ErrStale (file changed since read)", out)
	}
	if err != ErrStale && !strings.Contains(err.Error(), "stale") {
		t.Errorf("err = %q, want ErrStale", err.Error())
	}
}

func TestFreshFileApplies(t *testing.T) {
	// readTime is *after* the file's mtime → fresh, applies normally.
	content := "old text\n"
	blocks := []Block{{Search: "old text", Replace: "new text"}}
	out, err := ApplyAt(content, blocks, time.Now(), time.Now().Add(-time.Hour))

	if err != nil {
		t.Fatalf("ApplyAt(fresh) = %v, want nil", err)
	}
	if !strings.Contains(out, "new text") {
		t.Errorf("output = %q, want the replacement applied", out)
	}
}
