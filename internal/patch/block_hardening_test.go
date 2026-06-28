// Curated no-panic regression for the SEARCH/REPLACE block parser (Sprint 11
// H-005). The parser must reject malformed input with an error, never panic,
// even when markers are nested, duplicated, or missing.

package patch

import "testing"

func TestParseBlocksNoPanicOnMalformedInput(t *testing.T) {
	cases := []string{
		"",                              // empty input
		"no markers here",               // no block markers
		"<<<<<<< SEARCH",                // open never closed
		"<<<<<<< SEARCH\nold",           // missing =======
		"<<<<<<< SEARCH\nold\n=======",  // missing >>>>>>> REPLACE
		"=======\nnew\n>>>>>>> REPLACE", // ======= before SEARCH
		">>>>>>> REPLACE",               // stray REPLACE marker
		"<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n<<<<<<< SEARCH", // nested/unfinished
		"<<<<<<< SEARCH\n<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE",       // nested SEARCH
		"\x00\x01\x02", // binary garbage
		"<<<<<<< SEARCH\n" + string(make([]byte, 1<<16)) + "\n=======\ny\n>>>>>>> REPLACE", // large block
	}

	for i, c := range cases {
		_, _ = ParseBlocks(c) // must not panic
		if testing.Verbose() {
			t.Logf("case %d parsed without panic", i)
		}
	}
}
