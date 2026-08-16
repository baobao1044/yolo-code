// Tests for the property that a bash observation tells the truth about what
// the command did to the repo (File 08 §8.6.1 "Files: paths the tool mutated").
//
// Bash used to leave Files empty unconditionally, and downstream that is not
// read as "no information" — verify.Engine copies the list into
// verify.Change.Files and runs its stages over it, so an empty list means every
// required stage skips and noSignalVerdict fails the run closed. A `sh -c
// 'go fmt ./...'` that legitimately rewrote four files therefore arrived at
// VERIFY claiming it had touched nothing and was thrown into rollback and
// reflection over work that was fine. These tests pin the observation to the
// filesystem so that cannot come back.

package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// bashIn returns a Bash confined to root, the way the composition root wires it
// (cmd/yolo passes the repo as both root and cwd).
func bashIn(t *testing.T, root string) *Bash {
	t.Helper()
	return NewBash(NewSandbox(root, root))
}

// runBash runs command and fails the test if the tool itself errored; the
// command's own exit code is the caller's business.
func runBash(t *testing.T, b *Bash, command string) ToolOutput {
	t.Helper()
	out, err := b.Run(context.Background(), ToolInput{Args: []byte(`{"command":` + quoteJSON(command) + `}`)})
	if err != nil {
		t.Fatalf("Bash(%s) returned tool error %v, want nil — the command itself was expected to run", command, err)
	}
	return out
}

// quoteJSON is a minimal JSON string quoter for the command args; the test
// commands contain quotes and slashes but no control characters.
func quoteJSON(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(append(out, '"'))
}

// relFiles maps an observation's absolute paths back to root-relative ones so
// assertions read like the command that produced them.
func relFiles(t *testing.T, root string, files []string) []string {
	t.Helper()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		real = root
	}
	var rel []string
	for _, f := range files {
		r, err := filepath.Rel(real, f)
		if err != nil {
			t.Fatalf("observation named %q, which is not under the sandbox root %q — VERIFY would run stages outside the repo", f, real)
		}
		rel = append(rel, r)
	}
	return rel
}

func wantExactly(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("observation named %v, want exactly %v — VERIFY runs its stages over this list and nothing else, so a missing path is an unverified change and an extra one is a stage run over a file nobody touched", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation named %v, want exactly %v", got, want)
		}
	}
}

func TestBashNamesAFileItCreated(t *testing.T) {
	root := t.TempDir()
	b := bashIn(t, root)

	out := runBash(t, b, "printf hello > created.txt")

	wantExactly(t, relFiles(t, root, out.Files), "created.txt")
}

func TestBashNamesAFileItModifiedWithoutChangingItsSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same-size.txt"), []byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	// Same byte count, different content: size alone cannot see this edit, so
	// the detector has to compare modification times too. A `sed -i` that keeps
	// a line's length is the everyday shape of this.
	out := runBash(t, b, "printf BBBB > same-size.txt")

	wantExactly(t, relFiles(t, root, out.Files), "same-size.txt")
}

func TestBashNamesAFileItDeleted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doomed.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	// A deletion is a mutation VERIFY must hear about: the build stage cares
	// that a file it used to compile is gone.
	out := runBash(t, b, "rm doomed.txt")

	wantExactly(t, relFiles(t, root, out.Files), "doomed.txt")
}

func TestBashReportsNothingWhenTheCommandChangesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "untouched.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	out := runBash(t, b, "cat untouched.txt")

	if len(out.Files) != 0 {
		t.Fatalf("a read-only command named %v as mutated — VERIFY would run its stages over files nobody wrote, and in the runtime a corrective patch may write to any file the last observation named", out.Files)
	}
	if out.FilesUnknown {
		t.Fatal("a command that changed nothing reported FilesUnknown — 'measured, and the answer was none' must stay distinguishable from 'never measured', or the flag is noise and consumers will learn to ignore it")
	}
}

func TestBashNamesEveryFileOfAMultiFileChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	// One command, three outcomes. VERIFY checks the list and nothing else, so
	// a detector that stopped at the first difference would leave two of these
	// unverified.
	out := runBash(t, b, "rm gone.txt; printf yy > kept.txt; printf zz > fresh.txt")

	wantExactly(t, relFiles(t, root, out.Files), "fresh.txt", "gone.txt", "kept.txt")
}

func TestBashSaysUnknownRatherThanEmptyWhenItCannotWatchTheTree(t *testing.T) {
	// A Bash with no sandbox root has nothing to walk. Reporting an empty list
	// there would be a positive claim that the command changed nothing, made by
	// code that never looked — the exact conflation that put this detector here.
	b := NewBash(&Sandbox{})

	out := runBash(t, b, "printf hi")

	if !out.FilesUnknown {
		t.Fatal("Bash with no sandbox root reported FilesUnknown=false — with no tree to walk it cannot know what changed, and 'I did not look' must never be spelled the same way as 'nothing changed'")
	}
	if len(out.Files) != 0 {
		t.Fatalf("an unknown result carried the file list %v — a consumer that ignores the flag must fall through to the fail-closed empty-list path, never to a partial list it would mistake for the whole change", out.Files)
	}
}

func TestBashSaysUnknownWhenTheTreeIsTooLargeToWalk(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restore := fsScanMaxFiles
	fsScanMaxFiles = 2
	defer func() { fsScanMaxFiles = restore }()
	b := bashIn(t, root)

	out := runBash(t, b, "printf hi > a")

	if !out.FilesUnknown {
		t.Fatal("a walk that gave up on a too-large tree reported FilesUnknown=false — a bounded scan that presents its partial view as the whole answer is worse than no scan, because VERIFY would pass on the files it was shown and certify the ones it was not")
	}
	if len(out.Files) != 0 {
		t.Fatalf("a bounded walk still returned %v — the partial list must be dropped, not published", out.Files)
	}
}

func TestChangesInsideSkippedDirectoriesAreNotReportedAsRepoChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	// `git commit` rewrites the object store on every call. Naming those paths
	// would aim the AST, build and test stages at git internals; the skip list
	// is shared with ListFiles so the two walkers agree on what the repo is.
	out := runBash(t, b, "printf ref > .git/HEAD")

	if len(out.Files) != 0 {
		t.Fatalf("a write inside .git was reported as a repo change (%v) — VERIFY would try to parse and build the object store", out.Files)
	}
	if out.FilesUnknown {
		t.Fatal("skipping .git marked the answer unknown — the skip is a deliberate scope decision, not a gap in the measurement, and conflating them would make every git command unverifiable")
	}
}

func TestRepositoryCheckedOutUnderASkippedNameIsStillWatched(t *testing.T) {
	// The skip list matches on directory name. Applied to the root itself it
	// would skip the entire tree and report every command as changing nothing —
	// a silent false clean for anyone whose checkout happens to be called
	// `dist` or `vendor`.
	root := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b := bashIn(t, root)

	out := runBash(t, b, "printf hi > touched.txt")

	wantExactly(t, relFiles(t, root, out.Files), "touched.txt")
}

func TestNormalizerCarriesTheUnknownFlagToTheObservation(t *testing.T) {
	// The Observation is what VERIFY, memory and the transcript see. A
	// normalizer that copied Files but dropped the flag would re-merge the two
	// states one layer after the tool took the trouble to separate them.
	obs := NewNormalizer(DefaultLimits(), nil).Normalize(
		ToolOutput{Stdout: "x", FilesUnknown: true}, Metadata{})

	if !obs.FilesUnknown {
		t.Fatal("Normalize dropped FilesUnknown — the tool said it could not tell what it changed and the Observation says it changed nothing")
	}
}
