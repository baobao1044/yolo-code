//go:build fuzz

// Fuzz target for patch.ParseBlocks (Sprint 11 H-005). The only invariant is
// "no panic" — malformed model output must surface as an error, not crash the
// process.

package patch

import "testing"

func FuzzParseBlocks(f *testing.F) {
	f.Add("<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n")
	f.Add("no markers")
	f.Add("<<<<<<< SEARCH\nold\n=======\n")
	f.Add("<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n<<<<<<< SEARCH\nc\n")
	f.Add("<<<<<<< SEARCH\n\x00\n=======\ny\n>>>>>>> REPLACE\n")

	f.Fuzz(func(t *testing.T, text string) {
		_, _ = ParseBlocks(text) // must not panic
	})
}
