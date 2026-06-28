# 10 — Patch Engine

> **Goal of this document:** design Layer 9 — the algorithm by which the AI
> modifies code accurately. It applies a **hybrid** patch format (search-replace
> primary, unified-diff input fallback), detects conflicts, takes a checkpoint
> before every apply, and rolls back on failure — feeding the failure into
> Reflection (File 07) for a corrected retry.

This file owns **Layer 9 (`internal/patch`)**, invoked by the `Write`/`Patch`
tools (File 08). It uses the git-snapshot primitive from Layer 7 and the
tree-sitter validator shared with File 09.

---

## Table of Contents

1. [Diff vs Patch vs Full Overwrite](#101-diff-vs-patch-vs-full-overwrite)
2. [Hybrid Format: Search-Replace Primary + Diff Fallback](#102-hybrid-format-search-replace-primary--diff-fallback)
3. [Conflict Detection](#103-conflict-detection)
4. [AST Validation](#104-ast-validation)
5. [Checkpoint & Rollback](#105-checkpoint--rollback)
6. [The Engine, consolidated](#106-the-engine-consolidated)

---

## 10.1 Diff vs Patch vs Full Overwrite

| Method | What the model emits | Failure mode | Token cost |
|---|---|---|---|
| **Full overwrite** | the entire new file | any hallucination anywhere is committed | very high |
| **Unified diff** | `@@ -a,b +c,d @@` hunks by line number | **line-number drift**: off-by-one applies to wrong lines | medium |
| **Search-and-replace** | exact old text + exact new text | ambiguous match or no match (loud, recoverable) | low |

### 10.1.1 Why line numbers betray the model
LLMs are bad at counting lines, especially in files longer than a screen. A
unified diff saying "replace lines 42–48" will be wrong by ±2 lines often, and
*the engine cannot tell* — it applies to whatever lines 42–48 currently are,
silently corrupting the file. Search-and-replace sidesteps this: it doesn't care
about line numbers, only content.

### 10.1.2 When full overwrite is acceptable
New files, or small files under `overwrite_threshold` (default 200 lines) where
the change is structural enough that search-replace would cost more tokens than
the file. For creation we always use full overwrite; for modification we always
use search-replace.

---

## 10.2 Hybrid Format: Search-Replace Primary + Diff Fallback

You approved the hybrid: **search-replace is the primary path; unified diff is
accepted as input and converted internally to search-replace**, so the engine
has one internal application path while accepting the most common external
patch format.

### 10.2.1 The SEARCH/REPLACE block (primary)

```
<source path="src/main.go">
<<<<<<< SEARCH
exact old text
=======
exact new text
>>>>>>> REPLACE
</source>
```

- The `<<<<<<< SEARCH` / `>>>>>>> REPLACE` markers are stable strings the model
  is trained (system prompt) to produce.
- Whitespace is significant by default; a `fuzzy="true"` attribute relaxes it.
- An empty SEARCH = insertion at the anchor; an empty REPLACE = deletion.

### 10.2.2 The matching algorithm

```go
package patch

type Block struct {
    Search  string
    Replace string
    Fuzzy   bool
    Anchor  string   // extra context for disambiguation
}

func Apply(content string, blocks []Block) (string, error) {
    for _, b := range blocks {
        idx, err := locate(content, b)
        if err != nil { return "", err }
        content = content[:idx] + b.Replace + content[idx+len(b.Search):]
    }
    return content, nil
}

func locate(content string, b Block) (int, error) {
    if b.Search == "" { return b.InsertAt, nil }
    hits := allIndices(content, b.Search)
    switch len(hits) {
    case 1:  return hits[0], nil
    case 0:
        if b.Fuzzy { return fuzzyLocate(content, b.Search) }
        return 0, ErrNotFound
    default:
        if b.Anchor != "" { return disambiguate(content, b.Search, b.Anchor) }
        return 0, fmt.Errorf("%w (matched %d times)", ErrAmbiguous, len(hits))
    }
}
```

- **Exact single match** → apply.
- **No match** → fuzzy fallback if `Fuzzy`, else loud `ErrNotFound`.
- **Multiple matches** → disambiguate via the anchor (extra surrounding context);
  still ambiguous → reject and ask the model for more context.

### 10.2.3 Fuzzy matching
Opt-in per block. Tolerates trailing-whitespace and tab/space indentation
differences by normalizing whitespace on both sides. A fuzzy match records the
diff between SEARCH and actual text in the event log for audit. Fuzzy is never
the default — it's where silent mis-application hides.

### 10.2.4 Unified diff as input (fallback path)
For providers/workflows that prefer unified diff, the engine accepts a diff and
converts each hunk to a SEARCH/REPLACE block by reading the current file lines
around the hunk's range. The conversion uses line numbers only to *locate* the
SEARCH text, not to apply the patch — so the same content-addressing guarantee
holds. The engine's single internal application path is unchanged.

```go
func FromUnifiedDiff(diff string, fs *sysio.FS) ([]Block, error) {
    hunks := diffParse(diff)
    var blocks []Block
    for _, h := range hunks {
        original, _ := fs.Read(context.Background(), h.Path)
        oldText := extractRange(original, h.OldStart, h.OldCount)
        newText := h.NewBody
        blocks = append(blocks, Block{Search: oldText, Replace: newText})
    }
    return blocks, nil
}
```

---

## 10.3 Conflict Detection

Before applying, the engine checks the patch won't fight the current state:

| Check | Rejection |
|---|---|
| SEARCH text not present | `ErrNotFound` → model retries with correct text |
| SEARCH text ambiguous | `ErrAmbiguous` → model adds anchor context |
| File changed since model last read it (mtime newer than read time) | `ErrStale` → model re-reads and re-patches |
| Patch and a concurrent agent's pending patch touch overlapping lines | `ErrConflict` → Coordination Layer serializes (File 12) |

The staleness check is important: between the model reading a file and the
patch being applied, the user (or another agent) may have edited it. Applying a
search-replace on a diverged file produces nonsense. `ErrStale` forces a
re-read, closing the race.

---

## 10.4 AST Validation

Even a correctly-located patch can produce broken code (half-deleted function,
unbalanced brace). The engine validates after applying — shared tree-sitter
validator from File 09:

```go
func (e *Engine) validate(path, content string) error {
    lang, ok := e.parsers[ext(path)]
    if !ok { return nil }   // unknown language: skip, don't block non-code
    tree, err := sitterParse(lang, content)
    if err != nil { return err }
    var errs []string
    walk(tree.RootNode(), func(n *sitter.Node) {
        if n.IsError() || n.Type() == "ERROR" {
            errs = append(errs, fmt.Sprintf("line %d: %s", n.StartPoint().Row, text(n, content)))
        }
    })
    if len(errs) > 0 { return fmt.Errorf("syntax errors:\n%s", strings.Join(errs, "\n")) }
    return nil
}
```

AST validation is the **first** gate (cheapest). Semantic checks (lint/type/
build/test) run in the Verification Engine (File 09) *after* the patch is
applied and committed to the verify pipeline.

---

## 10.5 Checkpoint & Rollback

### 10.5.1 Two-layer rollback
1. **In-memory snapshot** — the original file content kept before writing. If
   AST validation fails *before* the file is written, this is the rollback.
2. **Git snapshot** — a hidden commit (or, if undesirable, a `go-git` blob) for
   the affected paths *before* the write. If verification (File 09) fails
   *after* the file is written, the git snapshot restores it.

```mermaid
sequenceDiagram
    participant PE as Patch Engine
    participant SM as L1 Session (checkpoint)
    participant FS as L7 FS
    participant VER as L9 Verify
    PE->>FS: Read original (in-memory snapshot)
    PE->>SM: Checkpoint(name, paths) -> snapshot
    PE->>PE: Apply blocks -> new content
    PE->>PE: AST validate
    alt AST ok
        PE->>FS: Write new content
        PE->>VER: (runtime runs Verify)
        alt verify pass
            Note over PE: keep; PatchAppliedEvent
        else verify fail
            PE->>SM: Restore(checkpoint)
            PE->>FS: restore original
            Note over PE: Result.Rejected + reason -> Reflection (File 07)
        end
    else AST fail
        PE->>FS: (no write happened)
        Note over PE: Result.Rejected + ast reason -> Reflection
    end
```

### 10.5.2 The git snapshot
Using `go-git`, a snapshot is a blob/tree written to the object store without
affecting the working tree or HEAD — an "invisible commit" existing only to be
restored. The Session Manager (File 03 §3.4) names and tracks it on the task;
this engine creates it.

### 10.5.3 Non-git fallback
If the project isn't a git repo, the engine falls back to a local shadow copy
under `.yolo/snapshots/<task>/<seq>/`. Slower and less space-efficient, but the
same guarantee: rollback is always possible.

### 10.5.4 Rollback on failure feeds Reflection
A rejected patch returns to the model as:
```
ToolResult{tool: "Patch", exit: 1, stderr: "rejected: verify: go vet: src/main.go:42: undeclared name: foo\nrolled back to checkpoint patch_03"}
```
The runtime routes this into Reflection (File 07 §7.3), which diagnoses the
root cause and proposes a corrected patch — *the file is unchanged on disk*
between attempts. The snapshot id in the message lets the user inspect the
original.

### 10.5.5 Rollback on cancellation
Cancellation during the verify window (File 04 §4.5.5) restores the checkpoint
via the Session Manager's `Restore`. The user never sees a half-edited file
survive an interrupt.

### 10.5.6 Retention
Per-task snapshots are kept until the task ends (for TUI undo); beyond that,
pruned to 24h or 100 MB on a background goroutine; `yolo clean` removes all
with a warning first.

---

## 10.6 The Engine, consolidated

```go
package patch

type Engine struct {
    git       *sysio.Git
    fs        *sysio.FS
    session   *session.Manager
    validator *ast.Validator
    parsers   map[string]*sitter.Language
    config    Config
    log       *slog.Logger
}

type Config struct {
    OverwriteThreshold int
    FuzzyDefault       bool
    AutoFormat         bool
}

type Op struct {
    Path    string
    Blocks  []Block      // empty + FullContent for full overwrite
    FullContent string    // set for new files / overwrite
}

type Result struct {
    Accepted bool
    Rejected bool
    Reason   string
    Snapshot sysio.SnapshotRef
}

func (e *Engine) Apply(ctx context.Context, op Op) (Result, error) {
    original, err := e.fs.Read(ctx, op.Path)
    if err != nil && !os.IsNotExist(err) { return Result{}, err }
    isNew := os.IsNotExist(err)

    snap, err := e.session.Checkpoint(ctx, op.Task, "patch_"+op.Seq, []string{op.Path})
    if err != nil { return Result{}, err }

    var next string
    if isNew || op.FullContent != "" {
        next = op.FullContent
    } else {
        if err := e.detectConflict(op, original); err != nil {
            return Result{Rejected: true, Reason: err.Error(), Snapshot: snap}, nil
        }
        n, err := Apply(string(original), op.Blocks)
        if err != nil {
            return Result{Rejected: true, Reason: err.Error(), Snapshot: snap}, nil
        }
        next = n
    }

    if err := e.validator.Validate(op.Path, next); err != nil {
        return Result{Rejected: true, Reason: "ast: " + err.Error(), Snapshot: snap}, nil
    }

    if err := e.fs.Write(ctx, op.Path, []byte(next)); err != nil {
        return Result{}, err
    }
    // Verification (File 09) runs next via the runtime; on fail the runtime
    // calls session.Restore(snap) — see §10.5.4.
    return Result{Accepted: true, Snapshot: snap}, nil
}
```

### 10.6.1 The model's contract (system prompt excerpt)

```
To modify an existing file, emit one or more SEARCH/REPLACE blocks inside a
<source path="..."> tag. The SEARCH text must match the file verbatim (or mark
fuzzy="true"). If it appears more than once, include enough surrounding lines
to make it unique. Never invent line numbers. You may also emit a unified
diff; the engine will convert it. A rejected patch means the file is unchanged
— read the reason and propose a corrected patch.
```

---

## 10.7 What this file fixes, and what it hands off

**Fixed here:**
- the hybrid format decision (search-replace primary + unified-diff input
  fallback, one internal application path) and the failure-mode reasoning that
  rules out raw line-number application;
- the SEARCH/REPLACE block, exact-first matching, fuzzy fallback, anchor
  disambiguation;
- conflict detection including the staleness race (file changed since read);
- AST validation as the first gate, shared with File 09;
- two-layer rollback (in-memory pre-write, git snapshot post-write) with the
  non-git shadow-copy fallback, and the rule that a rejected patch feeds
  Reflection with the file unchanged on disk.

**Handed off:**
- The checkpoint/restore primitives are owned by the Session Manager → **File
  03 §3.4**; this engine creates snapshots, the Session Manager tracks them.
- The post-apply semantic verification (lint/type/build/test/policy) runs in
  **File 09**.
- A `fail` verdict + rejected patch routes into Reflection → **File 07 §7.3**.

---

*End of File 10 — Patch Engine.*
