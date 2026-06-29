// The verification pipeline (File 09 §9.2/§9.6): the 7-stage chain
// AST→Format→Lint→TypeCheck→Build→Tests→PolicyCheck. Each stage is a
// StageRunner; the Pipeline runs them in canonical order and short-circuits on
// a fail (a stage that broke the build stops the chain — later stages can't
// add signal). L8-001 is the infrastructure: each stage runs and emits a
// StageResult; the aggregate Verdict + Verify entry point + events land in
// L8-002.
//
// The seams (Runner, FS) live here because the import matrix (File 15
// §15.15.2) lets verify import only `event` and `patch` — not infra (so the
// real os/exec adapter is wired in cmd/yolo) and not session/sysio. This
// mirrors the patch engine's Filesystem/Checkpointer seams. The AST stage
// reuses patch.Validator (the stdlib go/parser validator L9-004 built) rather
// than a second parser — one source of truth for "does this file parse".

package verify

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baobao1044/yolo-code/internal/patch"
)

// Runner shells out to a tool (gofmt, go vet, go build, go test) scoped to the
// changed paths. The real os/exec adapter is wired in cmd/yolo (verify may not
// import infra); tests inject a fake that returns canned results. A non-nil
// error means the tool didn't run to completion (not found, killed); an exit
// code is the tool's own verdict.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// FS reads a file's content — the AST and Policy stages need the bytes the
// patch produced. The composition root wires the sandbox-confined reader;
// verify stays free of sysio. Mirrors patch.FS.
type FS interface {
	Read(ctx context.Context, path string) (string, error)
}

// StageRunner is one pipeline stage (File 09 §9.6). Name is the stage; Run
// inspects the changed files and returns the stage's verdict.
type StageRunner interface {
	Name() Stage
	Run(ctx context.Context, files []string) StageResult
}

// PipelineDeps wires the pipeline's stages. Runner and FS are the seams; the
// AST stage builds its own patch.Validator (one validator per pipeline —
// stateless). Cache is the L8-005 unchanged-file result cache; nil means every
// stage runs its own logic (no skip). The verify Engine wraps this with an
// event Bus (see engine.go).
type PipelineDeps struct {
	Runner Runner
	FS     FS
	Cache  *FileCache
}

// Pipeline runs the 7 stages in canonical order, short-circuiting on a fail.
type Pipeline struct {
	stages []StageRunner
}

// NewPipeline builds the 7-stage chain. Stages are registered in pipeline order
// (AST first, Policy last) so Run walks them in the right sequence without a
// separate ordering table.
func NewPipeline(d PipelineDeps) *Pipeline {
	return &Pipeline{stages: []StageRunner{
		&astStage{fs: d.FS, validator: patch.NewValidator(), cache: d.Cache},
		&formatStage{runner: d.Runner},
		&lintStage{runner: d.Runner},
		&typeCheckStage{runner: d.Runner},
		&buildStage{runner: d.Runner},
		&testStage{runner: d.Runner},
		&policyStage{fs: d.FS, rules: defaultRules()},
	}}
}

// Run walks the stages in order and returns each stage's StageResult. A fail
// short-circuits the chain (the failing result is included; later stages do
// not run). Warnings do NOT short-circuit (§9.4.1: a warning is acceptable, the
// chain continues). Skips (L8-004) are recorded and continue.
func (p *Pipeline) Run(ctx context.Context, files []string) []StageResult {
	var out []StageResult
	for _, st := range p.stages {
		r := st.Run(ctx, files)
		out = append(out, r)
		if r.Status == SevFail {
			break
		}
	}
	return out
}

// --- AST stage (§9.3.1) ---------------------------------------------------

// astStage re-parses each changed file via patch.Validator (the stdlib
// go/parser validator, L9-004). A parse error is a fail — a correctly-located
// patch can still break syntax. Unknown extensions skip (the validator returns
// nil for .md/.txt), so a Markdown edit doesn't fail for "no grammar". The
// L8-005 cache short-circuits a re-verify of an unchanged file: a content-hash
// hit returns the cached StageResult as a SevSkip ("cached: unchanged content")
// so the trace shows the file was skipped, not re-validated.
type astStage struct {
	fs        FS
	validator *patch.Validator
	cache     *FileCache // L8-005: nil → no caching, every file re-validates.
}

func (s *astStage) Name() Stage { return StageAST }

func (s *astStage) Run(ctx context.Context, files []string) StageResult {
	var issues []Issue
	cachedFiles := 0
	for _, f := range files {
		content, err := s.fs.Read(ctx, f)
		if err != nil {
			return StageResult{Stage: StageAST, Status: SevFail, Detail: "read " + f + ": " + err.Error()}
		}
		// L8-005: a cache hit short-circuits this file — re-verify of an
		// unchanged file is O(1) (one read for the hash, no re-parse). Only a
		// cached PASS is reused; a cached fail isn't (a later patch may have
		// fixed it — and the content hash differs after any edit, so this branch
		// only fires when the content is byte-identical to the cached pass).
		if cached, ok := s.cache.Lookup(f, StageAST, content); ok && cached.Status == SevPass {
			cachedFiles++
			continue
		}
		if err := s.validator.Validate(f, content); err != nil {
			issues = append(issues, Issue{Path: f, Code: "ast", Message: err.Error()})
		} else {
			// File validated clean → record it so the next unchanged verify hits.
			s.cache.Record(f, StageAST, content, StageResult{
				Stage: StageAST, Status: SevPass, Detail: "ast valid",
			})
		}
	}
	if len(issues) > 0 {
		return StageResult{Stage: StageAST, Status: SevFail, Detail: "syntax error", Issues: issues}
	}
	// Every file was a cache hit → the whole stage is a skip (re-verify of
	// unchanged files is O(1)). A mixed batch (some cached, some validated)
	// reports a pass — the stage did real work for the uncached files.
	if cachedFiles == len(files) && len(files) > 0 {
		return StageResult{Stage: StageAST, Status: SevSkip, Detail: "cached: unchanged content"}
	}
	return StageResult{Stage: StageAST, Status: SevPass, Detail: "ast valid"}
}

// --- Format stage (§9.3.2) ------------------------------------------------

// formatStage runs `gofmt -l <files>`; the tool lists unformatted files on
// stdout (it exits 0). A non-empty list is a WARNING, not a fail (§9.3.2: a
// format mismatch with AutoFormat off is a warning). The auto-format + re-run
// AST path is deferred to a later ticket; L8-001 reports the mismatch.
type formatStage struct{ runner Runner }

func (s *formatStage) Name() Stage { return StageFormat }

func (s *formatStage) Run(ctx context.Context, files []string) StageResult {
	if !hasToolFor(files, StageFormat) {
		return skipNoTool(StageFormat)
	}
	stdout, _, _, err := s.runner.Run(ctx, "gofmt", append([]string{"-l"}, files...)...)
	if err != nil {
		return StageResult{Stage: StageFormat, Status: SevFail, Detail: "gofmt: " + err.Error()}
	}
	if mismatches := nonEmptyLines(stdout); len(mismatches) > 0 {
		var issues []Issue
		for _, f := range mismatches {
			issues = append(issues, Issue{Path: f, Code: "format", Message: "unformatted (gofmt -l)"})
		}
		return StageResult{
			Stage:  StageFormat,
			Status: SevWarn,
			Detail: "unformatted: " + strings.Join(mismatches, ", "),
			Issues: issues,
		}
	}
	return StageResult{Stage: StageFormat, Status: SevPass, Detail: "formatted"}
}

// --- Lint stage (§9.3.3) --------------------------------------------------

// lintStage runs `go vet <files>`; a non-zero exit is a fail with the vet
// diagnostics parsed into Issues. Scoping to the changed files keeps it fast
// (§9.5.1). Non-Go lint tools (ruff/clippy/eslint) plug in behind the same
// Runner seam; L8-001 wires Go.
type lintStage struct{ runner Runner }

func (s *lintStage) Name() Stage { return StageLint }

func (s *lintStage) Run(ctx context.Context, files []string) StageResult {
	if !hasToolFor(files, StageLint) {
		return skipNoTool(StageLint)
	}
	dirs := dirOf(files)
	_, stderr, exit, err := s.runner.Run(ctx, "go", append([]string{"vet"}, dirs...)...)
	if err != nil {
		return StageResult{Stage: StageLint, Status: SevFail, Detail: "go vet: " + err.Error()}
	}
	if exit != 0 {
		return StageResult{
			Stage:  StageLint,
			Status: SevFail,
			Detail: "go vet failed",
			Issues: parseIssues(stderr, "vet"),
		}
	}
	return StageResult{Stage: StageLint, Status: SevPass, Detail: "vet clean"}
}

// --- TypeCheck / Build stages (§9.3.4/§9.3.5) -----------------------------

// typeCheckStage runs `go build <dirs>` scoped to the changed files' packages.
// For Go, build IS the type check (§9.5.4 matrix), so this and the Build stage
// share the buildCmd helper; a non-zero exit is a fail with the compile errors.
type typeCheckStage struct{ runner Runner }

func (s *typeCheckStage) Name() Stage { return StageTypeCheck }

func (s *typeCheckStage) Run(ctx context.Context, files []string) StageResult {
	return buildCmd(s.runner, StageTypeCheck, "typecheck", files)
}

// buildStage runs the same scoped `go build` as TypeCheck (for Go the two
// stages are the same command, §9.5.4). Kept as a distinct stage so the trace
// shows both gates and so non-Go languages (where Build differs from
// TypeCheck — e.g. Python has no Build) can diverge later.
type buildStage struct{ runner Runner }

func (s *buildStage) Name() Stage { return StageBuild }

func (s *buildStage) Run(ctx context.Context, files []string) StageResult {
	return buildCmd(s.runner, StageBuild, "build", files)
}

// buildCmd runs `go build <dirs>` for a stage. dirs is the deduped, sorted set
// of the changed files' package directories (§9.5.1 scoping — `go build .`,
// not `./...`). A non-zero exit is a fail with the compile errors parsed.
func buildCmd(r Runner, stage Stage, code string, files []string) StageResult {
	if !hasToolFor(files, stage) {
		return skipNoTool(stage)
	}
	dirs := dirOf(files)
	_, stderr, exit, err := r.Run(context.Background(), "go", append([]string{"build"}, dirs...)...)
	if err != nil {
		return StageResult{Stage: stage, Status: SevFail, Detail: "go build: " + err.Error()}
	}
	if exit != 0 {
		return StageResult{
			Stage:  stage,
			Status: SevFail,
			Detail: "go build failed",
			Issues: parseIssues(stderr, code),
		}
	}
	return StageResult{Stage: stage, Status: SevPass, Detail: code + " ok"}
}

// --- Test stage (§9.3.6) --------------------------------------------------

// testStage runs `go test <dirs>` scoped to the changed packages. A non-zero
// exit is a fail (a broken test blocks the patch). A test *timeout* would be a
// warning (§9.3.6 — a slow CI machine shouldn't veto a correct patch); L8-001
// surfaces the runner's timeout as a fail-via-exit for simplicity, the
// timeout-as-warn refinement rides on the per-stage timeout wiring in a later
// ticket.
type testStage struct{ runner Runner }

func (s *testStage) Name() Stage { return StageTest }

func (s *testStage) Run(ctx context.Context, files []string) StageResult {
	if !hasToolFor(files, StageTest) {
		return skipNoTool(StageTest)
	}
	dirs := dirOf(files)
	_, stderr, exit, err := s.runner.Run(ctx, "go", append([]string{"test"}, dirs...)...)
	if err != nil {
		return StageResult{Stage: StageTest, Status: SevFail, Detail: "go test: " + err.Error()}
	}
	if exit != 0 {
		return StageResult{
			Stage:  StageTest,
			Status: SevFail,
			Detail: "tests failed",
			Issues: parseIssues(stderr, "test"),
		}
	}
	return StageResult{Stage: StageTest, Status: SevPass, Detail: "tests pass"}
}

// --- Policy stage (§9.3.7) ------------------------------------------------

// policyStage runs the project-specific guardrails the linters can't express
// (§9.3.7 — rules from AGENTS.md). A Rule inspects the changed files (and their
// content via FS) and returns Issues; the rule's Level decides whether an
// issue fails (hard gate) or warns. L8-001 ships two sample rules; the full
// AGENTS.md rule engine is wired later.
type policyStage struct {
	fs    FS
	rules []Rule
}

func (s *policyStage) Name() Stage { return StagePolicy }

func (s *policyStage) Run(ctx context.Context, files []string) StageResult {
	var warnings, errors []Issue
	for _, rule := range s.rules {
		for _, is := range rule.Check(ctx, files, s.fs) {
			if rule.Level() == SevFail {
				errors = append(errors, is)
			} else {
				warnings = append(warnings, is)
			}
		}
	}
	switch {
	case len(errors) > 0:
		return StageResult{Stage: StagePolicy, Status: SevFail, Detail: "policy violation", Issues: errors}
	case len(warnings) > 0:
		return StageResult{Stage: StagePolicy, Status: SevWarn, Detail: "policy warning", Issues: warnings}
	default:
		return StageResult{Stage: StagePolicy, Status: SevPass, Detail: "policy ok"}
	}
}

// Rule is one project-specific guardrail (§9.3.7). Check returns the issues it
// found; Level is the severity those issues carry (SevFail blocks, SevWarn
// advises).
type Rule interface {
	Check(ctx context.Context, files []string, fs FS) []Issue
	Level() Severity
}

// defaultRules are the two sample guardrails L8-001 ships: no edits under
// vendor/ (a hard fail), and no TODO without an owner (a warning). Real
// AGENTS.md rules plug in behind the same interface later.
func defaultRules() []Rule {
	return []Rule{vendorRule{}, todoRule{}}
}

// vendorRule blocks edits under vendor/ (third-party vendored code).
type vendorRule struct{}

func (vendorRule) Level() Severity { return SevFail }

func (vendorRule) Check(_ context.Context, files []string, _ FS) []Issue {
	var out []Issue
	for _, f := range files {
		if strings.Contains(filepath.ToSlash(f), "vendor/") {
			out = append(out, Issue{Path: f, Code: "no-vendor", Message: "edits under vendor/ forbidden"})
		}
	}
	return out
}

// todoRule warns on a TODO comment without an owner tag (`@owner`).
type todoRule struct{}

func (todoRule) Level() Severity { return SevWarn }

func (todoRule) Check(ctx context.Context, files []string, fs FS) []Issue {
	var out []Issue
	for _, f := range files {
		content, err := fs.Read(ctx, f)
		if err != nil {
			continue // a missing file is the AST stage's concern, not the policy's
		}
		if strings.Contains(content, "TODO") && !strings.Contains(content, "@owner") {
			out = append(out, Issue{Path: f, Code: "todo-owner", Message: "TODO without an owner (@owner)"})
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------

// dirOf returns the deduped, sorted package directories of the given files
// (§9.5.1 scoping). `go build`/`go test` run on dirs, not individual files, so
// a one-file edit in `pkg/x` builds `pkg/x`, not the whole module. A file with
// no directory (basename only) scopes to ".".
func dirOf(files []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, f := range files {
		d := filepath.ToSlash(filepath.Dir(f))
		if d == "" {
			d = "."
		}
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// --- per-language stage matrix (§9.5.3 + §9.5.4) ----------------------------

// languageOf returns the language tag for a path's extension, for the per-
// language stage matrix (§9.5.4). "go" for .go; "" (Other) for anything else
// (.md, .txt, …) — the matrix skips all command stages for Other. Adding a
// language means teaching languageOf its extension and listing it in toolLangs,
// not touching the engine (same extensibility rule as the tool registry,
// §9.6.1). L8-004 wires Go only.
func languageOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	default:
		return "" // Other — no command-stage tool (§9.5.4 matrix row).
	}
}

// toolLangs is the per-stage set of languages that have a tool (§9.5.4). Go has
// all five command stages; Other has none. AST/Policy aren't in the matrix —
// AST has its own validator (which skips unknown exts), Policy is the project
// gate that always runs.
var toolLangs = map[Stage]map[string]bool{
	StageFormat:    {"go": true},
	StageLint:      {"go": true},
	StageTypeCheck: {"go": true},
	StageBuild:     {"go": true},
	StageTest:      {"go": true},
}

// hasToolFor reports whether any changed file is in a language the stage has a
// tool for (§9.5.4). A Markdown-only patch → all command stages skip; a mixed
// Go + Markdown patch keeps them running (the .md file doesn't gate them off —
// Go has a tool, so the stage runs and the .md file is simply ignored by the
// Go tools). Stages absent from toolLangs (AST, Policy) always run their own
// logic — hasToolFor returns true so they're never skipped by the matrix.
func hasToolFor(files []string, stage Stage) bool {
	langs, ok := toolLangs[stage]
	if !ok {
		return true // AST/Policy own their skip logic; the matrix doesn't gate them.
	}
	for _, f := range files {
		if langs[languageOf(f)] {
			return true
		}
	}
	return false
}

// skipNoTool is the StageResult a command stage returns when hasToolFor is
// false: a SevSkip naming the reason, so the trace shows *why* the stage didn't
// run (§9.5.3: "skips are recorded in the verdict so the trace shows why a
// stage didn't run, not just that it didn't").
func skipNoTool(stage Stage) StageResult {
	return StageResult{
		Stage:  stage,
		Status: SevSkip,
		Detail: "no tool for the changed languages",
	}
}

// nonEmptyLines splits s on newlines and drops empty lines (framing), returning
// the content lines. Used by the format stage to read gofmt -l's file list.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseIssues turns a compiler/vet stderr into Issues, one per diagnostic line
// of the form `path:line: message`. Lines that don't match are kept as a
// single summary issue so the failure reason is never lost. The code is the
// stage's rule id ("vet"/"build"/"test") since the underlying tool doesn't
// emit one.
func parseIssues(stderr, code string) []Issue {
	var out []Issue
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if is, ok := parseDiagLine(line, code); ok {
			out = append(out, is)
		} else {
			out = append(out, Issue{Code: code, Message: line})
		}
	}
	return out
}

// parseDiagLine parses one `path:line: message` diagnostic. Returns the Issue
// and ok=true if it matched; false otherwise.
func parseDiagLine(line, code string) (Issue, bool) {
	// Find the second colon after a digit run: "path:line: rest".
	// Split on the first two colons that follow a path-like prefix.
	i := strings.Index(line, ":")
	if i < 0 {
		return Issue{}, false
	}
	path := line[:i]
	rest := line[i+1:]
	j := strings.Index(rest, ":")
	if j < 0 {
		return Issue{}, false
	}
	lineNo := 0
	for _, r := range rest[:j] {
		if r < '0' || r > '9' {
			return Issue{}, false
		}
		lineNo = lineNo*10 + int(r-'0')
	}
	return Issue{Path: path, Line: lineNo, Code: code, Message: strings.TrimSpace(rest[j+1:])}, true
}
