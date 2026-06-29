// Merge verifier adapter (Sprint 13 S13-002).
// Implements coord.Verifier by checking that the combined diff is non-empty.
// A full implementation would apply the patch to a temp copy of the repo and
// re-run the verification pipeline; the placeholder still exercises the merge
// seam end-to-end.

package main

import (
	"context"
	"strings"

	coordpkg "github.com/baobao1044/yolo-code/internal/coord"
)

// mergeVerifier is a coord.Verifier that approves any non-empty combined diff.
type mergeVerifier struct{}

// Verify satisfies coord.Verifier.
func (mergeVerifier) Verify(ctx context.Context, combinedDiff string) (bool, error) {
	return strings.TrimSpace(combinedDiff) != "", nil
}

// compile-time check.
var _ coordpkg.Verifier = mergeVerifier{}
