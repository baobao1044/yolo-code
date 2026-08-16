// A deleted file is a change, not a read error.
//
// astStage reads every path in the change list and returns SevFail on any read
// error. A patch (or a `git rm`, or a `rm` through the bash tool) that deletes a
// file puts that file's path in the change list and then removes it from disk,
// so the read fails and the AST stage fails the whole verification — before
// Build and Test, which are the two stages that could actually say whether
// deleting it broke anything. The pipeline short-circuits on SevFail, so they
// never run.
//
// This got sharper when the bash tool learned to report what it changed:
// diffSnapshots names deleted paths explicitly ("deleted: still a mutation
// VERIFY must hear about"), so `rm x.go` in a bash call now reaches VERIFY as a
// change list of one nonexistent file. What was a patch-only path is now the
// ordinary one.
//
// The fix is not to ignore read errors. A file that cannot be read because the
// disk is failing or the sandbox denied it is exactly the case SevFail exists
// for, and collapsing it into "probably deleted" would be the same
// unmeasured-looks-like-measured trade this codebase keeps losing. Only
// not-exist is a deletion; everything else still fails.

package verify

import (
	"context"
	"errors"
	"io/fs"

	"github.com/baobao1044/yolo-code/internal/patch"
	"testing"
)

// missingFS is fakeFS with an explicit hole: paths in gone report
// fs.ErrNotExist (a deletion), paths in broken report something else (a real
// I/O failure). The two must not end up with the same verdict.
type missingFS struct {
	present map[string]string
	broken  map[string]bool
}

func (m missingFS) Read(_ context.Context, path string) (string, error) {
	if m.broken[path] {
		return "", errors.New("input/output error")
	}
	if s, ok := m.present[path]; ok {
		return s, nil
	}
	return "", fs.ErrNotExist
}

// TestDeletedFileDoesNotFailTheASTStage is the headline: a change that deletes
// one file and edits another must let the pipeline reach Build and Test, which
// are the stages that can tell whether the deletion broke the package.
func TestDeletedFileDoesNotFailTheASTStage(t *testing.T) {
	fsys := missingFS{present: map[string]string{
		"pkg/kept.go": "package pkg\n\nfunc Kept() {}\n",
	}}
	st := &astStage{fs: fsys, validator: patch.NewValidator()}

	got := st.Run(context.Background(), []string{"pkg/kept.go", "pkg/deleted.go"}, Policy{})
	if got.Status == SevFail {
		t.Errorf("AST failed a change containing a deletion: %s\n"+
			"A deleted file has no syntax to check, and failing here short-circuits "+
			"the pipeline before Build and Test — the only stages that could say "+
			"whether removing it broke the package", got.Detail)
	}
}

// TestDeletionOnlyChangeSkipsRatherThanFailsOrPasses pins the more delicate
// case. Every path in the change is gone, so the AST stage has nothing to
// parse. It must not fail (a deletion is a legitimate change) and it must not
// pass (it measured no syntax, and a pass here is the stage certifying work it
// never looked at). Skip is the only honest answer, and it is the one the
// engine's no-signal rule already knows how to read.
func TestDeletionOnlyChangeSkipsRatherThanFailsOrPasses(t *testing.T) {
	fsys := missingFS{present: map[string]string{}}
	st := &astStage{fs: fsys, validator: patch.NewValidator()}

	got := st.Run(context.Background(), []string{"pkg/a.go", "pkg/b.go"}, Policy{})
	if got.Status != SevSkip {
		t.Errorf("AST on a deletion-only change = %v (%s), want SevSkip. "+
			"Fail blocks a legitimate delete; pass certifies syntax nobody read",
			got.Status, got.Detail)
	}
}

// TestUnreadableFileStillFailsTheASTStage is the other side, and the reason the
// fix tests errors.Is instead of just swallowing read errors. A file that
// exists but cannot be read is not a deletion — it is the unmeasured state this
// stage's SevFail exists to report.
func TestUnreadableFileStillFailsTheASTStage(t *testing.T) {
	fsys := missingFS{
		present: map[string]string{"pkg/a.go": "package pkg\n"},
		broken:  map[string]bool{"pkg/a.go": true},
	}
	st := &astStage{fs: fsys, validator: patch.NewValidator()}

	got := st.Run(context.Background(), []string{"pkg/a.go"}, Policy{})
	if got.Status != SevFail {
		t.Errorf("AST on an unreadable (not deleted) file = %v (%s), want SevFail. "+
			"If an I/O error is treated as a deletion, a failing disk reads as a "+
			"clean delete and verification certifies a file it could not open",
			got.Status, got.Detail)
	}
}

// TestDeletionDoesNotStopBuildAndTestFromRunning is the end-to-end version of
// the first test: it is not enough that AST declines to fail, the later stages
// have to actually run. This asserts against the pipeline rather than the
// stage, because the short-circuit lives in Pipeline.Run.
func TestDeletionDoesNotStopBuildAndTestFromRunning(t *testing.T) {
	fsys := missingFS{present: map[string]string{
		"pkg/kept.go": "package pkg\n\nfunc Kept() {}\n",
	}}
	p := NewPipeline(PipelineDeps{Runner: passRunner(), FS: fsys})

	results := p.Run(context.Background(), []string{"pkg/kept.go", "pkg/deleted.go"}, Policy{})

	ran := map[Stage]Severity{}
	for _, r := range results {
		ran[r.Stage] = r.Status
	}
	for _, want := range []Stage{StageBuild, StageTest} {
		st, reached := ran[want]
		if !reached {
			t.Errorf("%v never ran: the pipeline short-circuited on the deletion "+
				"before reaching the stage that could judge it. Stages seen: %v",
				want, ran)
			continue
		}
		if st == SevFail {
			t.Errorf("%v failed on a passing runner: %v", want, ran)
		}
	}
}
