// Tests for the merge verifier adapter (Sprint 13 S13-002).
//
// The verifier is deliberately strict: "carries no content" and "is the empty
// string" are the same verdict. coord.Merge must therefore never hand it a
// patch built by joining blank per-todo diffs — those look non-empty to a
// `== ""` check but contentless here, and the mismatch is what surfaced as an
// unexplained "merged patch failed verification".

package main

import (
	"context"
	"testing"
)

func TestMergeVerifierRejectsContentlessDiffs(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want bool
	}{
		{"empty", "", false},
		{"single newline", "\n", false},
		{"joined blank diffs", "\n\n", false},
		{"spaces and tabs", "  \t\n ", false},
		{"real patch", "@@ -1 +1 @@\n-a\n+b", true},
		{"padded real patch", "\n@@ -1 +1 @@\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeVerifier{}.Verify(context.Background(), tc.diff)
			if err != nil {
				t.Fatalf("Verify(%q): %v", tc.diff, err)
			}
			if got != tc.want {
				t.Errorf("Verify(%q) = %v, want %v", tc.diff, got, tc.want)
			}
		})
	}
}
