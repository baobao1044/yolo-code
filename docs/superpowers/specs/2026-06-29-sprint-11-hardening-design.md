# Sprint 11 — Hardening & Distribution

**Date:** 2026-06-29
**Sprint:** 11 (roadmap §15.14) — Hardening & Distribution
**Predecessor:** Sprint 10 (Coordination Layer L11) — pushed to `master` (`dbb085d`)
**Spec status:** approved (release policy and scope confirmed)

This spec records the final design for the hardening and distribution sprint before any code is written. It covers the eight H-tickets, the deferred §15.9.2 integration bucket, the release/distribution policy, the build-tag isolation strategy, and the TDD discipline.

---

## 1. Decisions (confirmed)

1. **Release: pipeline + dry-run only** — Sprint 11 builds the cross-compile matrix, the `goreleaser` configuration, the GitHub Actions release workflow, and the version-injection path, but it **does not** create a Git tag, a GitHub release, or publish artifacts. The release workflow runs `goreleaser release --snapshot --clean` so the pipeline is exercised end-to-end without producing an official release. Publishing is gated on a later sprint.

2. **Build-tag isolation keeps `go test ./...` fast** — slow, large, or nondeterministic tests are hidden behind build tags: `golden` for determinism suites, `snapshot` for performance budgets, `docs` for doc-coverage verification, and `release` for release/packaging checks. The default `go test ./...` runs only the fast unit suite; CI runs all tagged gates in separate jobs.

3. **§15.9.2 integration bucket remains deferred** — end-to-end wiring that drives real cognitive/exec/verify/patch/restorer adapters through the coordination layer, real multi-agent runs against a live repo, and real `gh release create`/artifact publishing are **not** in Sprint 11. This sprint hardens the existing seams and prepares distribution plumbing.

---

## 2. Scope overview

| Area | Tickets | Outcome |
|---|---|---|
| Distribution | H-001, H-008 | 4-platform cross-compile (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64), goreleaser dry-run, version injection, release workflow |
| Performance budgets | H-002, H-003 | S1 cold-start snapshot ≤ 50 ms cold / ≤ 10 ms warm; S2 token-to-screen ≤ 50 ms per 1k tokens; snapshot tests isolated by build tag |
| Determinism | H-004 | Golden-transcript suite across headless fixtures; replay events produce identical transcript hash |
| Robustness | H-005, H-006 | Fuzz `patch.ParseBlocks` with curated no-panic regression; sandbox red-team command/path checks + checklist doc |
| Documentation | H-007 | User docs (`docs/user/`) and a doc-coverage test that fails when user-facing files drift from SUMMARY.md |
| CI | H-001, H-003, exit bar | Branch trigger fixed from `main` → `master`; snapshot and golden jobs added |

---

## 3. Architecture & import boundary

Sprint 11 adds **no new internal package**. All code lives in existing packages:

- `cmd/yolo` — version command, release config, golden-transcript tests, docs coverage.
- `internal/tui` — S1 cold-start and S2 token-to-screen snapshot budgets.
- `internal/patch` — fuzz harness and hardening regression for `ParseBlocks`.
- `internal/exec` — sandbox red-team regression.
- `docs/user/` — user-facing documentation.
- `.github/workflows/`, root `Makefile`, `.goreleaser.yml` — CI/CD and release packaging.

Import rules from previous sprints remain unchanged. H-001's CI fix and H-003's snapshot Makefile target do not relax the layer-dependent allowlists.

```
 ┌─────────────────────────────────────────────────────────────┐
 │  Distribution (H-001 / H-008)                                 │
 │   .goreleaser.yml → 4-platform builds, snapshot dry-run        │
 │   cmd/yolo/version.go ← -ldflags injection of version/commit  │
 │   .github/workflows/release.yml → dry-run on push/master       │
 ├─────────────────────────────────────────────────────────────┤
 │  Performance budgets (H-002 / H-003)                          │
 │   internal/tui/snapshot_test.go (build tag: snapshot)         │
 │     S1: measure first fold init vs cached init                │
 │     S2: measure update + View after N×1k tokens               │
 ├─────────────────────────────────────────────────────────────┤
 │  Determinism (H-004)                                          │
 │   cmd/yolo/headless_golden_test.go (build tag: golden)        │
 │     replay fixture events → transcript → hash == expected      │
 ├─────────────────────────────────────────────────────────────┤
 │  Robustness (H-005 / H-006)                                 │
 │   internal/patch/block_fuzz_test.go + block_hardening_test.go │
 │   internal/exec/sandbox_redteam_test.go                      │
 │   docs/security/sandbox-redteam.md                           │
 ├─────────────────────────────────────────────────────────────┤
 │  User docs (H-007)                                            │
 │   docs/user/README.md + quickstart.md + commands.md          │
 │   cmd/yolo/docs_test.go (build tag: docs)                    │
 └─────────────────────────────────────────────────────────────┘
```

---

## 4. Release policy

- **No tag** is created by the release workflow.
- **No `gh release create`** is invoked.
- **No artifact** is uploaded to a release or registry.
- `goreleaser` runs with `--snapshot --clean` so artifacts end up in `dist/`, which is gitignored.
- Version strings are injected at link time via `-ldflags` so `yolo version` reports the git-describe-ish value without hard-coding it in source.

---

## 5. Build-tag isolation

| Tag | Test file | Trigger |
|---|---|---|
| `snapshot` | `internal/tui/snapshot_test.go` | `make test-snapshot` / CI snapshot job |
| `golden` | `cmd/yolo/headless_golden_test.go` | `make test-golden` / CI race-and-golden job |
| `docs` | `cmd/yolo/docs_test.go` | `make test-docs` / CI docs job |
| `release` | `cmd/yolo/release_test.go`, `cmd/yolo/release_config_test.go` | `make test-release` / CI release job |

Default `go test ./...` skips all of the above. This preserves the fast feedback loop on the dev box while giving CI full coverage.

---

## 6. Ticket breakdown (8 tickets + exit bar)

| ID | Title | Exit bar | Files / notes |
|---|---|---|---|
| H-001 | Cross-compile matrix + CI branch fix | `go build ./...` passes on linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 via `GOOS/GOARCH` loop; `.github/workflows/ci.yml` triggers on `branches: [master]` | `Makefile` (cross target), `.github/workflows/ci.yml` |
| H-002 | S1 cold-start snapshot budget | First TUI fold init ≤ 50 ms; second (cached) init ≤ 10 ms | `internal/tui/snapshot_test.go` with `//go:build snapshot` |
| H-003 | S2 token-to-screen snapshot budget + target + CI | Update + View after 1k tokens ≤ 50 ms; `make test-snapshot`; CI snapshot job added | `internal/tui/snapshot_test.go`, `Makefile`, `.github/workflows/ci.yml` |
| H-004 | Golden-transcript suite | Replay a headless fixture through the runtime produces a transcript whose hash matches `testdata/golden_headless_transcript.txt` | `cmd/yolo/headless_golden_test.go` (golden tag), `cmd/yolo/testdata/golden_headless_transcript.txt` |
| H-005 | Fuzz `patch.ParseBlocks` + curated no-panic regression | `go test -tags=fuzz -fuzz=FuzzParseBlocks -fuzztime=30s` runs without panic; curated corpus covers malformed/nested/missing markers | `internal/patch/block_fuzz_test.go` (fuzz tag), `internal/patch/block_hardening_test.go` |
| H-006 | Sandbox red-team regression + checklist | `curl`, `wget`, `ssh`, `scp`, `rsync`, `nc`, `ftp`, `telnet` classified as high; shell-escape (`eval`, `$(...)`, backticks, `source`) classified as critical; `../../etc/passwd` rejected; `sudo rm -rf /` still critical after peeling wrappers | `internal/exec/sandbox_redteam_test.go`, `docs/security/sandbox-redteam.md` |
| H-007 | User docs + coverage test | `docs/user/README.md`, `quickstart.md`, `commands.md` exist and match `docs/user/SUMMARY.md`; `make test-docs` passes | `docs/user/*.md`, `cmd/yolo/docs_test.go` (docs tag) |
| H-008 | Version injection + goreleaser + dry-run release workflow | `yolo version` prints injected version; `make snapshot` runs `goreleaser release --snapshot --clean`; `.github/workflows/release.yml` dry-runs on master | `cmd/yolo/version.go`, `cmd/yolo/version_test.go`, `cmd/yolo/release_test.go` (release tag), `cmd/yolo/release_config_test.go` (release tag), `.goreleaser.yml`, `.github/workflows/release.yml`, `Makefile` |
| Exit bar | Sprint 11 exit bar | All default tests pass; snapshot, golden, docs, and release tagged suites pass locally (release dry-run via snapshot mode); CI matrix green; commit/push | — |

---

## 7. Documented spec gaps / deferrals

- **§15.9.2 integration bucket** (real multi-agent end-to-end run) remains deferred, as was the case for Sprint 9 and Sprint 10.
- **Actual artifact publishing** is deferred: Sprint 11 only dry-runs the release pipeline.
- **Real performance telemetry** (S1/S2 live dashboards) is not built; snapshot tests assert micro-benchmark budgets only.
- **`headless.go` runtime wiring** is not changed; H-004 tests determinism through existing seams only.

---

## 8. Dependencies (go.mod delta)

**None.** All added tests use the stdlib `testing` package plus existing internal packages. `.goreleaser.yml` is a pipeline config, not a Go dependency. The `Makefile` may shell-out to `goreleaser` if installed, but it is optional and CI provides it via action.

---

## 9. TDD discipline (per ticket)

Every ticket follows the strict TDD discipline established in Sprints 0-10:

1. **RED** — write the failing test (often a compile failure first). Confirm it fails.
2. **GREEN** — write the minimal code to pass. Confirm it passes.
3. **Mutation check** — intentionally break one invariant, confirm the test fails, then restore.
4. **gofmt -w** + **go vet** + **default suite** `go test ./...` + **tagged suite(s)** + **3× stability** `go test -count=1` on the new tests.
5. **commit + push** to `baobao1044/yolo-code` master.

`-race` remains CI/Linux-only because the Windows dev host lacks gcc/cgo. Local 3× `-count=1` stability is used and noted in commit messages.

---

## 10. Sprint exit bar

Sprint 11 is done when:

1. **H-001** — CI triggers on `master`, and a 4-platform `go build` matrix passes.
2. **H-002 + H-003** — TUI cold-start and token-to-screen snapshot budgets pass under the `snapshot` build tag, with a `make test-snapshot` target and a CI snapshot job.
3. **H-004** — The golden-transcript suite passes under the `golden` build tag; replaying the headless fixture yields a stable transcript hash.
4. **H-005** — `patch.ParseBlocks` survives 30s of fuzzing without panic, and a curated regression suite guards the parser against malformed/missing-marker inputs.
5. **H-006** — The sandbox red-team regression denies path escapes, shell-escape constructs, and dangerous wrappers; the checklist doc is committed.
6. **H-007** — User docs are present, complete, and covered by a docs-coverage test under the `docs` build tag.
7. **H-008** — Version injection works via `-ldflags`, `goreleaser --snapshot --clean` dry-runs successfully, and the release workflow is committed but does not publish artifacts.
8. **Repo hygiene** — `go test ./...` + `go test -tags=snapshot,-tags=golden,-tags=docs,-tags=release ./...` are green locally (snapshot/golden/docs tests run locally; release tests exercise snapshot mode); 3× stability is green; commits are pushed to `master`.

**Out of Sprint 11 scope** (deferred): §15.9.2 integration end-to-end run, real multi-agent cost accrual wiring, actual GitHub release artifact publishing, and live performance dashboards.

---

*End of Sprint 11 design spec.*
