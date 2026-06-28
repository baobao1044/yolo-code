// Tests for the unified-diff fallback parser (File 10 §10.2.4): the engine
// accepts a `git diff`-style diff and converts each hunk to a SEARCH/REPLACE
// block by reading the current file lines around the hunk's range. Line
// numbers are used only to *locate* the Search text, not to apply — so the
// content-addressing guarantee from L9-001 holds; Apply is unchanged.

package patch

import (
	"strings"
	"testing"
)

// fakeFS implements the FS seam FromUnifiedDiff reads through (the real one is
// wired by the composition root later; the matrix forbids patch importing
// sysio). Read returns the fixture's file content, or a not-found error for a
// missing path — the real FS behaves the same (a missing file can't be read).
type fakeFS map[string]string

func (f fakeFS) Read(path string) (string, error) {
	s, ok := f[path]
	if !ok {
		return "", errMissingFile(path)
	}
	return s, nil
}

type errMissingFile string

func (e errMissingFile) Error() string { return "missing: " + string(e) }

func TestFromUnifiedDiffConvertsHunkToBlock(t *testing.T) {
	fs := fakeFS{"main.go": "package main\n\nfunc old() {}\n"}
	// A diff that replaces "func old() {}" with "func new() {}". The hunk's
	// line range (old start/count) is used only to locate the Search text.
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main

-func old() {}
+func new() {}
`
	blocks, err := FromUnifiedDiff(diff, fs)
	if err != nil {
		t.Fatalf("FromUnifiedDiff = %v, want nil", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 hunk → 1 block", len(blocks))
	}
	if !strings.Contains(blocks[0].Search, "func old() {}") {
		t.Errorf("Search = %q, want it to contain the old text from the file", blocks[0].Search)
	}
	if !strings.Contains(blocks[0].Replace, "func new() {}") {
		t.Errorf("Replace = %q, want it to contain the new hunk body", blocks[0].Replace)
	}
}

func TestFromUnifiedDiffBlockAppliesViaL9_001(t *testing.T) {
	// The converted block must apply through the L9-001 single application
	// path — proving the content-addressing guarantee survives the diff
	// conversion (line numbers were only for locating).
	fs := fakeFS{"main.go": "package main\n\nfunc old() {}\n"}
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main

-func old() {}
+func new() {}
`
	blocks, err := FromUnifiedDiff(diff, fs)
	if err != nil {
		t.Fatalf("FromUnifiedDiff = %v, want nil", err)
	}
	out, err := Apply(fs["main.go"], blocks)
	if err != nil {
		t.Fatalf("Apply(converted blocks) = %v, want nil", err)
	}
	if strings.Contains(out, "func old() {}") {
		t.Errorf("output still has old text: %q", out)
	}
	if !strings.Contains(out, "func new() {}") {
		t.Errorf("output = %q, want the new text applied", out)
	}
}

func TestFromUnifiedDiffMultipleHunks(t *testing.T) {
	fs := fakeFS{"a.go": "alpha\nbeta\ngamma\n"}
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ a/a.go
@@ -1,1 +1,1 @@
-alpha
+ALPHA
@@ -3,1 +3,1 @@
-gamma
+GAMMA
`
	blocks, err := FromUnifiedDiff(diff, fs)
	if err != nil {
		t.Fatalf("FromUnifiedDiff = %v, want nil", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (one per hunk)", len(blocks))
	}
	out, err := Apply(fs["a.go"], blocks)
	if err != nil {
		t.Fatalf("Apply = %v, want nil", err)
	}
	if out != "ALPHA\nbeta\nGAMMA\n" {
		t.Errorf("Apply = %q, want %q", out, "ALPHA\nbeta\nGAMMA\n")
	}
}

func TestFromUnifiedDiffMissingFileFails(t *testing.T) {
	// A hunk against a path the FS doesn't have → ErrNotFound (the old text
	// can't be read to build the Search). The engine doesn't guess.
	fs := fakeFS{}
	diff := `diff --git a/missing.go b/missing.go
--- a/missing.go
+++ b/missing.go
@@ -1,1 +1,1 @@
-x
+y
`
	_, err := FromUnifiedDiff(diff, fs)
	if err == nil {
		t.Fatal("FromUnifiedDiff(missing file) = nil, want error (old text unreadable)")
	}
}
