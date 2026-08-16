// Filesystem change detection for tools that mutate the repo through a channel
// nothing in this package can see — today that is Bash, which hands an
// arbitrary string to `sh -c` (File 08 §8.4.7).
//
// Read and EditFile know what they touched because they touched it. Bash does
// not, and it used to say so by leaving Observation.Files empty. Downstream
// that is not heard as "no information": cmd/yolo's verify adapter copies the
// list into verify.Change.Files verbatim and every one of the seven stages runs
// over exactly that list, so an empty list means every required stage skips,
// noSignalVerdict fires, and the run fails closed with "no verification
// signal". A `go fmt ./...` or `sed -i` that did precisely the right thing was
// therefore rolled back and sent to Reflection for having done it. The verify
// layer had already been hardened to fail rather than certify a change it never
// measured; nothing had been done about the tool that was misreporting.
//
// The detector is a before/after walk of the sandbox root comparing size,
// modification time and mode per file. Two alternatives were rejected:
//
//   - `git status --porcelain`. Cheaper and exact for tracked files, but it
//     reports nothing for output the command wrote into an ignored directory,
//     nothing at all when the sandbox root is not a repository, and it makes a
//     tool with no VCS dependency depend on one being installed and on the repo
//     not being mid-rebase. Its failure mode is silence, which is the one
//     answer this code must never give when it does not know.
//   - Parsing the command string for redirects and known writers. That is the
//     same guess the risk classifier makes, and the classifier only has to be
//     conservative to be safe — here a miss is a false report of "changed
//     nothing", which is worse than no report at all. `make`, `python
//     build.py`, and any script the model just wrote are unparseable by
//     construction.
//
// The walk is the honest one: it measures the filesystem instead of predicting
// it. It costs two directory walks per bash call, which is the price of the
// observation being true.

package exec

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

// Walk budgets. A repository large enough to blow through either of these is
// one where two full walks per bash call would be the dominant cost of running
// a command at all, so the detector gives up rather than pay it — and says it
// gave up (fsSnapshot.partial), because a bounded walk that returned its
// partial result as if it were complete would be reporting "nothing else
// changed" about a tree it never looked at.
// They are vars rather than consts only so a test can lower them: the bounded
// path is the one that must not degrade into a false "nothing changed", and a
// test that had to materialize fifty thousand files to reach it would be a test
// nobody runs.
var (
	fsScanMaxFiles = 50000
	fsScanBudget   = 2 * time.Second
)

// fsScanClockEvery is how often the deadline is consulted; fs.DirEntry.Info is
// a stat per file, so checking the clock every file would roughly double the
// syscall count for no extra accuracy.
const fsScanClockEvery = 256

// errScanBudget stops a walk that hit a bound. It never escapes this file: the
// caller reads fsSnapshot.partial, not the error.
var errScanBudget = errors.New("exec: filesystem scan budget exhausted")

// fsEntry is the per-file fingerprint the diff compares.
//
// Content hashing would be exact but costs a full read of every file in the
// repo, twice per bash call. Size+mtime+mode is what make, rsync and every
// build system settle for, and it misses exactly one shape: an in-place edit
// that keeps the byte count AND lands in the same modification-time tick. Go
// reports mtime at the resolution the filesystem keeps, which is nanoseconds on
// ext4/xfs/APFS/NTFS and one second on a few older or network filesystems —
// only on the latter is that shape reachable, and only for a command that
// rewrites a file within the same second as the snapshot.
type fsEntry struct {
	size  int64
	mtime int64 // UnixNano; see above for the resolution caveat
	mode  fs.FileMode
}

// fsSnapshot is one side of the comparison. partial says the walk did not see
// the whole tree, so the diff it takes part in cannot distinguish "unchanged"
// from "never looked at" and must report neither.
type fsSnapshot struct {
	entries map[string]fsEntry
	partial bool
}

// snapshotTree fingerprints every regular file under root.
//
// Errors are not skipped the way ListFiles skips them. A directory that cannot
// be read may be hiding the very file the command changed, and a walk that
// swallowed that would hand the diff a tree with a hole in it and no way to
// know — so any read error marks the snapshot partial and the whole detection
// degrades to "don't know".
//
// Symlinks are fingerprinted as links (WalkDir lstats and does not descend), so
// a symlink loop cannot hang the walk and a link pointing out of the sandbox
// cannot drag the walk outside it. A change to such a link's target is
// invisible here; if the target is inside the root it is seen directly on its
// own path, and if it is outside, verification could not check it anyway.
func snapshotTree(root string) fsSnapshot {
	snap := fsSnapshot{entries: make(map[string]fsEntry)}
	if root == "" {
		// No confinement root means nothing to compare against. Saying so is
		// the point of partial.
		snap.partial = true
		return snap
	}
	deadline := time.Now().Add(fsScanBudget)
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			snap.partial = true
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// The skip list is shared with ListFiles so the two cannot drift
			// into disagreeing about what the repo is. Skipping is deliberate
			// and does NOT mark the snapshot partial: a change confined to
			// .git or node_modules is one no verification stage would run over,
			// and reporting it would only aim the stages at files the tool
			// cannot check. A bash call that touched nothing else still lands
			// on the empty-list path, which fails closed.
			if path != root && skipWalkDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		n++
		if n > fsScanMaxFiles {
			snap.partial = true
			return errScanBudget
		}
		if n%fsScanClockEvery == 0 && time.Now().After(deadline) {
			snap.partial = true
			return errScanBudget
		}
		info, ierr := d.Info()
		if ierr != nil {
			// The file existed a moment ago and cannot be stat'd now. Same
			// reasoning as a walk error: a gap we cannot characterize.
			snap.partial = true
			return nil
		}
		snap.entries[path] = fsEntry{
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
			mode:  info.Mode(),
		}
		return nil
	})
	if err != nil && !errors.Is(err, errScanBudget) {
		snap.partial = true
	}
	return snap
}

// diffSnapshots returns the paths that differ between the two walks, and
// whether the answer is unknown.
//
// unknown is returned with a nil path list on purpose. A caller that ignores
// the flag then sees an empty list and takes the fail-closed route the verify
// engine already has for it — the same false failure as before this detector
// existed, which is the acceptable floor. Handing back a partial list instead
// would invite the opposite and unrecoverable outcome: VERIFY running its
// stages over the two files we did see, passing, and certifying the third one
// we never looked at.
func diffSnapshots(before, after fsSnapshot) (files []string, unknown bool) {
	if before.partial || after.partial {
		return nil, true
	}
	for path, a := range after.entries {
		if b, ok := before.entries[path]; !ok || b != a {
			files = append(files, path)
		}
	}
	for path := range before.entries {
		if _, ok := after.entries[path]; !ok {
			files = append(files, path) // deleted: still a mutation VERIFY must hear about
		}
	}
	// Map iteration order is randomized; an observation that reorders itself
	// between identical runs would break the golden-transcript hash and make
	// any diff of two runs unreadable.
	sort.Strings(files)
	return files, false
}

// observeFSChanges runs do while watching root, and reports what do changed.
//
// The walks bracket the command rather than running inside it: anything the
// command spawned into the background and did not wait for is not accounted
// for, which is the same blind spot the process-group kill exists to bound.
func observeFSChanges(root string, do func()) (files []string, unknown bool) {
	before := snapshotTree(root)
	if before.partial {
		// The "after" walk cannot rescue a partial "before" — skip it rather
		// than pay for a second walk whose result is already unusable.
		do()
		return nil, true
	}
	do()
	return diffSnapshots(before, snapshotTree(root))
}

// skipWalkDir reports whether a directory is one the repo walkers step over.
// Shared by ListFiles and the change detector so a directory that is invisible
// to one is invisible to the other; two independent copies of this list is how
// a tool ends up listing a file that the verifier was told does not exist.
func skipWalkDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "__pycache__", ".cache", "dist":
		return true
	}
	return false
}
