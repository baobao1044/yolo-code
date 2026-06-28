// Tests for the SEARCH/REPLACE block parser and the single internal
// application path (File 10 §10.2.1/§10.2.2): the engine parses the model's
// patch text into Blocks, then Apply locates each block's Search text in the
// file and replaces it. Exact-first matching; no match → ErrNotFound; many →
// ErrAmbiguous (the engine never guesses, File 10 §10.3). Fuzzy/anchor
// disambiguation is added by later tickets; L9-001 is the foundation.

package patch

import (
	"strings"
	"testing"
)

func TestParseSingleBlock(t *testing.T) {
	text := "<<<<<<< SEARCH\nold line\n=======\nnew line\n>>>>>>> REPLACE\n"
	blocks, err := ParseBlocks(text)
	if err != nil {
		t.Fatalf("ParseBlocks = %v, want nil", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].Search != "old line" {
		t.Errorf("Search = %q, want %q", blocks[0].Search, "old line")
	}
	if blocks[0].Replace != "new line" {
		t.Errorf("Replace = %q, want %q", blocks[0].Replace, "new line")
	}
}

func TestParseMultipleBlocks(t *testing.T) {
	text := "<<<<<<< SEARCH\na\n=======\nA\n>>>>>>> REPLACE\n<<<<<<< SEARCH\nb\n=======\nB\n>>>>>>> REPLACE\n"
	blocks, err := ParseBlocks(text)
	if err != nil {
		t.Fatalf("ParseBlocks = %v, want nil", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Search != "a" || blocks[0].Replace != "A" {
		t.Errorf("block 0 = {%q,%q}, want {a,A}", blocks[0].Search, blocks[0].Replace)
	}
	if blocks[1].Search != "b" || blocks[1].Replace != "B" {
		t.Errorf("block 1 = {%q,%q}, want {b,B}", blocks[1].Search, blocks[1].Replace)
	}
}

func TestParseBlockMissingMarkerFails(t *testing.T) {
	// A block missing the REPLACE marker is malformed; the parser must reject
	// rather than guess where Search ends.
	_, err := ParseBlocks("<<<<<<< SEARCH\nonly old\n=======\n")
	if err == nil {
		t.Fatal("ParseBlocks(missing >>>>>>> REPLACE) = nil, want error")
	}
}

func TestApplySingleBlockReplacesExactMatch(t *testing.T) {
	content := "package main\n\nfunc hello() {}\n"
	blocks := []Block{{Search: "func hello() {}", Replace: "func hello() error { return nil }"}}

	out, err := Apply(content, blocks)
	if err != nil {
		t.Fatalf("Apply = %v, want nil", err)
	}
	if strings.Contains(out, "func hello() {}\n") {
		t.Errorf("output still contains the old text: %q", out)
	}
	if !strings.Contains(out, "func hello() error { return nil }") {
		t.Errorf("output = %q, want it to contain the replacement", out)
	}
}

func TestApplyMultipleBlocksInOrder(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	blocks := []Block{
		{Search: "alpha", Replace: "ALPHA"},
		{Search: "gamma", Replace: "GAMMA"},
	}
	out, err := Apply(content, blocks)
	if err != nil {
		t.Fatalf("Apply = %v, want nil", err)
	}
	want := "ALPHA\nbeta\nGAMMA\n"
	if out != want {
		t.Errorf("Apply = %q, want %q", out, want)
	}
}

func TestApplySearchNotFoundFailsLoudly(t *testing.T) {
	content := "package main\n"
	blocks := []Block{{Search: "this text is not here", Replace: "x"}}

	_, err := Apply(content, blocks)
	if err == nil {
		t.Fatal("Apply(missing Search) = nil, want ErrNotFound (don't guess)")
	}
	if err != ErrNotFound && !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q, want ErrNotFound", err.Error())
	}
}

func TestApplyAmbiguousSearchFails(t *testing.T) {
	// "x" appears twice → the engine must not pick one; it asks for an anchor
	// (File 10 §10.2.2). L9-001 has no anchor yet → ErrAmbiguous.
	content := "x\nx\n"
	blocks := []Block{{Search: "x", Replace: "y"}}

	_, err := Apply(content, blocks)
	if err == nil {
		t.Fatal("Apply(ambiguous Search) = nil, want ErrAmbiguous")
	}
	if err != ErrAmbiguous && !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %q, want ErrAmbiguous", err.Error())
	}
}

func TestApplyEmptySearchIsInsertionAt(t *testing.T) {
	// An empty SEARCH = insertion at the anchor (File 10 §10.2.1). With
	// InsertAt set, the Replace text is inserted at that byte offset.
	content := "abc"
	blocks := []Block{{Search: "", Replace: "X", InsertAt: 1}}

	out, err := Apply(content, blocks)
	if err != nil {
		t.Fatalf("Apply(empty Search) = %v, want nil", err)
	}
	if out != "aXbc" {
		t.Errorf("Apply = %q, want %q (insert at offset 1)", out, "aXbc")
	}
}

func TestApplyEmptyReplaceIsDeletion(t *testing.T) {
	// An empty REPLACE = deletion (File 10 §10.2.1).
	content := "keep this\nremove me\nkeep too\n"
	blocks := []Block{{Search: "remove me\n", Replace: ""}}

	out, err := Apply(content, blocks)
	if err != nil {
		t.Fatalf("Apply(deletion) = %v, want nil", err)
	}
	if out != "keep this\nkeep too\n" {
		t.Errorf("Apply = %q, want %q (deletion)", out, "keep this\nkeep too\n")
	}
}
