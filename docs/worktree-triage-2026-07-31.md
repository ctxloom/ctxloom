# Worktree ledger triage — 2026-07-31

Base: `0da6b2d5 chore(scripts): rehome audit_sha_credits.py from the ephemeral session dir`
(`release/0.7` tip at time of analysis).

Method: `git cherry -v release/0.7 <branch>` to separate patch-equivalent from novel work,
`git merge-base --is-ancestor` for full-merge proof, subject/`-S` archaeology to name the
upstream landing commit for every duplicate, `git merge-tree --write-tree` for
non-destructive merge testing, and `git apply --index` + `go vet` in THIS worktree only for
compile-level "does it still apply" evidence. No other worktree was mutated.

**`git cherry` alone is not sufficient.** Four of the seven `+` commits below turned out to
have landed upstream anyway — cherry marks a commit `+` when the *pre-image* differed, even
if the *post-image* work is present. Every `+` was chased to its landing commit or proved
absent.

## Classification

| Worktree | Branch | Bucket | Evidence |
|---|---|---|---|
| `.ctxloom/sessions/pulpy-weary-equal/ephemeral/ctxloom-wt-pulpy-weary-equal-6c3821bc543d` | (detached `79561bdd`) | MERGED-REAPABLE | `79561bdd` is an ancestor of `release/0.7`; `git cherry` empty. Dirty = ` D CLAUDE.md` only |
| `ctxloom--feature-acp-session-lifecycle` | `feature/acp-session-lifecycle` | MERGED-REAPABLE | tip `cc4cb667` is an ancestor of `release/0.7`; clean |
| `ctxloom--s1-content-access` | `s1/content-access` | MERGED-REAPABLE | tip `43d08123` is an ancestor; clean |
| `ctxloom--wave3-gn-vital-deaf-stunt` | `wave3/gn-vital-deaf-stunt` | MERGED-REAPABLE | tip `3de0e355` is an ancestor; clean |
| `ctxloom--acpagent-race` | `fix/acpagent-race` | DUPLICATED | both commits `-`; landed as `9867c5ab` + `9740c364` |
| `ctxloom--mgr-writes` | `feat/config-manager-writes` | DUPLICATED | landed as `e5e9ee26` + `5c23a03c` |
| `ctxloom--test-remote-payload` | `test/remote-payload-e2e` | GENUINELY-PENDING (1 of 2) | `b233c218` landed as `9fd1a648`; `555a6d1c` absent upstream |
| `ctxloom--mech-acptest-index` | `fix/mech-acptest-index` | GENUINELY-PENDING (1 of 2) | `e71186b6` superseded by `d4c7da2c`; `85dd8ef9` absent upstream |
| `ctxloom--feature-acp-elicitation-cancel` | `feature/acp-elicitation-cancel` | GENUINELY-PENDING (does not compile) | `52ed5c99` absent upstream; red-by-design + API drift |
| `ctxloom--mock-engine` | `feat/mock-engine-binary` | GENUINELY-PENDING (superseded in part) | `enginecli.go` half superseded by `b4944b9b`; `internal/acp/argv.go` absent upstream |

Counts: 4 MERGED-REAPABLE, 2 DUPLICATED, 4 GENUINELY-PENDING, 0 AT-RISK. Sums to 10.

## GENUINELY-PENDING detail

### `fix/mech-acptest-index` — `85dd8ef9` only. MERGE. High confidence.

Opt-in unknown-property checking for the L0 ACP conformance harness. The vendored
`schema-v1.19.0` closes no object shape (`additionalProperties` appears 100 times, always a
`_meta` bag set to `true`), so the harness could catch a MISSING required field but never an
EXTRA or misspelled one. `acptest.NewStrictValidator` recompiles the same vendored bytes
with `unevaluatedProperties: false` filled in at every instance-location root, leaving the
vendored file untouched. Test-only; no production path changes. Finding two real gaps
(`models` on `session/new`, and the SDK's UNSTABLE `clientCapabilities.auth` leaking onto
every `initialize`) is exactly the harness's purpose.

Finished: yes. Still applies: yes — none of its four files moved on `release/0.7` since the
branch base `c89b811c`, `git apply --index` is clean, and `go vet ./internal/acptest/
./internal/acp/ ./internal/acpagent/` exits 0 on the applied tree.

The second commit `e71186b6` (three findings-index re-points) must NOT be merged: all three
re-points already landed on `release/0.7` in `d4c7da2c` (21:23) 35 minutes after
`e71186b6` (20:48), with richer prose. `e71186b6` is the sole cause of the
`docs/architecture/findings-index.md` merge conflict.

### `test/remote-payload-e2e` — `555a6d1c` only. MERGE. High confidence.

A single `@live` acceptance scenario: a local bundle fragment carries a marker string,
`ctxloom run --print` asks a real backend to echo it back. It is the one hop the hermetic
scenarios cannot cover — that a REAL backend receives the assembled context, not just what
ctxloom believes it sent. Pinned to `claude-haiku-4-5-20251001`, self-skips without
credentials.

Finished: yes. Still applies: yes — `remote_content_live.feature` is absent from
`release/0.7`, the new `steps_live.go` step is absent, `git apply --index` is clean and
`go vet ./tests/acceptance/` exits 0.

Its parent `b233c218` must NOT be merged: the three hermetic scenarios, the `review.feature`
`--bundle origin/demo` fixture fix, the `changes fragment` step def, and
`FindRepoCacheClone` all landed on `release/0.7` via `9fd1a648`. `release/0.7`'s version is
a strict improvement — `1cbf083b` (rare-vixen) additionally changed the skip wording from
"Skipped (already installed)" to "Skipped (kept at their locked commit)" and the scenario
now asserts the honest wording. Merging `b233c218` would REGRESS that. It is the entire
cause of the `remote_content.feature` / `steps_fixture.go` conflicts.

The branch stalled because its useful half landed via a different route and nobody came back
for the `@live` remainder.

### `feature/acp-elicitation-cancel` — `52ed5c99`. DO NOT MERGE AS-IS. High confidence.

A deliberately RED TDD commit: 151 lines of `internal/acpagent/elicitation_test.go` driving
a `forwardElicitation` primitive that was never written. The commit message says so
outright. It is a design artifact, not deliverable work.

Two independent reasons it cannot land unchanged:

1. `forwardElicitation` still does not exist on `release/0.7` — only `forwardPermission`
   (`server.go:891`) and `forwardTerminal` (`server.go:936`).
2. API drift since 2026-07-24. `git merge-tree` reports a CLEAN textual merge, which is
   misleading: `jsonrpc.NewConn` lost its leading `ctx` parameter. Applied and vetted, it
   fails with `too many arguments in call to jsonrpc.NewConn / have (context.Context,
   *io.PipeReader, *io.PipeWriter, nil, *Server) / want (io.Reader, io.Writer, io.Closer,
   jsonrpc.Handler)`.

Recommendation: keep the branch as a design record OR fold the test into a real
elicitation-forward feature. Do not merge. Merging it breaks the `internal/acpagent` build.

### `feat/mock-engine-binary` — `338e5cef`. PARTIAL SALVAGE. Medium confidence.

Self-described "wip: mock engine exploration, halted pending architecture revision". Three
files, two fates:

- `internal/shared/agent/enginecli.go` (204 lines) — SUPERSEDED. `release/0.7` carries a
  568-line matured version added by `b4944b9b` "feat(agent): EngineCLI contract". 677 diff
  lines apart. This is the `add/add` merge conflict. Discard.
- `internal/acp/argv.go` + `argv_test.go` (130 lines) — GENUINELY UNIQUE and NOT adopted.
  It proposes one central declaration of the argv grammar `chatArgv` can emit, plus a
  `TestChatArgv_EmitsOnlyDeclaredFlags` anti-drift gate. `release/0.7` still hardcodes
  `"--agent"`, `"--model"`, `"--agent-engine"` as string literals in `internal/acp/acp.go`
  (lines 330, 350, 354), and no `EmitsOnlyDeclaredFlags` test exists anywhere. The
  per-backend half of the idea did land (`internal/claude/enginecli.go:83 flagModel =
  "--model"`); the driver-side central grammar did not.

The vendor-behaviour notes embedded in `argv.go` are measured facts worth keeping regardless
(`--model` honored by kiro-cli acp, silently ignored by claude-code-acp 0.16.2, rejected at
parse by codex-acp 0.16.0 with exit 2).

Recommendation: NEEDS-JUDGEMENT. Either cherry-pick `internal/acp/argv.go` alone and wire
`acp.go` to it, or file the anti-drift gap and reap the branch.

## DUPLICATED detail

### `fix/acpagent-race` → landed

- `f2921a3c` "fix(acpagent): close two data races reproduced under load (sole-snore)" →
  **`9867c5ab`** on `release/0.7` (identical subject, patch-equivalent).
- `3a037662` "fix(cli): add missed jsonrpc.Conn.Start call in ACP door loopback test" →
  **`9740c364`** "fix(test): start the jsonrpc read loop in the door-equivalence test".

Nothing unique. Only dirt is gendocs debris (see below).

### `feat/config-manager-writes` → landed

- `5a94c9fa` "refactor(config): route agent/mcp/statusline/container writes through
  Manager.Update" → **`e5e9ee26`** (2026-07-21), identical subject, identical file set.
  `git cherry` marks it `+` only because upstream deleted 27 more lines from
  `internal/operations/helpers.go` — an intervening `release/0.7` fix (the `getFS`/
  `injectedFS` lock-skip and the `Load` → `LoadFresh` change) had enlarged the block being
  removed. Corroborated: `internal/config/interim_setters.go`, whose deletion is the point
  of the commit, does not exist on `release/0.7`.
- `20ee7ce4` "refactor(config): migrate the seventh write site, SetDefaultLLM, onto
  Manager.Update" → **`5c23a03c`** (patch-equivalent, `-`).

Nothing unique.

## Verdict on the stale-ltk-docs question: REFUTED

The premise does not hold. `release/0.7`'s committed ltk CLI docs are CURRENT.

Evidence, two independent lines:

1. **Byte comparison.** All 14 files in `ctxloom--acpagent-race`'s working tree — the
   "regeneration debris" — are byte-identical to the blobs committed on `release/0.7`. The
   worktree is pinned at base `703b1c17` (2026-07-21), where the docs had not yet been
   regenerated; its *committed* copies are the stale ones, and `just gendocs` there simply
   reproduced what `release/0.7` later adopted. Same for `ctxloom--mgr-writes` (base
   `d37130d6`, 2026-07-20).

2. **Authoritative regeneration.** `go run -tags docsgen ./cmd/ltk gendocs --markdown
   /tmp/ltkdocs-check` at `0da6b2d5` (= `release/0.7` tip) produces output identical to
   `website/src/content/docs/ltk/reference/cli/`. The only delta is `index.md`, which the
   generator does not emit — it is a hand-authored Starlight landing page ("Complete
   reference for all `ltk` commands.") with no GENERATED banner, correctly checked in.

Concretely, the `ltk_version.md` example from the brief: `release/0.7` ALREADY has
`--format` as an inherited persistent flag documenting `json, yaml, toml, text, or
markdown`. The `text or json` form the brief cites is what the OLD WORKTREES have committed,
not `release/0.7`.

So the 24 dirty files across the two worktrees are pure regeneration debris with zero
authored content, and `gen-docs-check` in `justfile.container` is doing its job.

**Side finding (real, and unrelated to worktrees):** `*.pb.go` is gitignored
(`.gitignore:26`) and `just proto` depends on `dev-image`. A freshly created worktree
therefore **cannot compile at all** until protobufs are generated in a container —
`go run ./cmd/ltk` fails with `undefined: WindowSize`, `undefined: RunStart`,
`undefined: WatchEvent` in `internal/lm/grpc/interface.go`. I worked around it by copying
the generated `*.pb.go` from the main worktree. This is a papercut that will hit every
agent given a fresh worktree, and it silently looks like broken code rather than missing
generation.

## Active-session signals — FLAG ONLY, do not touch

- **`ctxloom--mech-acptest-index`** — last commit today 20:48:25; `.reprise/base-state/` and
  `.reprise/cache/` written 20:41–20:48 (checkpointing tooling). At the time of writing
  (21:42) no process holds a cwd inside it and no command line references it. Its sibling
  work (`d4c7da2c` at 21:23, `08994dcd` at 21:35) landed DIRECTLY on `release/0.7`, and this
  triage's own base commit `0da6b2d5` (21:35) rehomes `audit_sha_credits.py` — the script
  `e71186b6` was produced by working. Read: the same live workstream moved on from this
  worktree to `release/0.7` within the last hour. Treat as warm, not abandoned.
- **`ctxloom--wave3-gn-vital-deaf-stunt`** — a live `claude --name vital-deaf-stunt` process
  (pid 2508205, started Jul 28, 594 CPU-minutes) plus `ctxloom run` (2486195), `ctxloom llm
  serve claude-code` (2508185), `ctxloom mcp` (2509512), `ctxloom acp server` (2924882). All
  five have cwd `/home/babbitt/workspace/ctxloom/ctxloom/main`, NOT the worktree. The branch
  is fully merged and the worktree is clean, but a running session shares its harp name.
  Confirm that session is finished before reaping.

## Recommended human actions

1. **Cherry-pick `85dd8ef9` (strict L0 validator) onto `release/0.7`** — SAFE. Applies clean,
   vets clean, files untouched upstream. Do NOT merge the branch; `e71186b6` conflicts and is
   already superseded by `d4c7da2c`.
2. **Cherry-pick `555a6d1c` (`@live` payload scenario) onto `release/0.7`** — SAFE. Applies
   clean, vets clean. Do NOT merge the branch; `b233c218` would REGRESS the `rare-vixen`
   skip-wording fix.
3. **Reap the four MERGED-REAPABLE worktrees + branches** — SAFE, with one caveat.
   `feature/acp-session-lifecycle`, `s1/content-access`, `wave3/gn-vital-deaf-stunt`, and the
   detached ephemeral worktree are all ancestors of `release/0.7` with no unique content.
   For `wave3` confirm the live `vital-deaf-stunt` session is done first (item 6). The
   ephemeral worktree's only dirt is a `CLAUDE.md` deletion — the file is intact on
   `release/0.7`, nothing is lost.
4. **Reap `fix/acpagent-race` and `feat/config-manager-writes`** — SAFE. Both fully landed
   (`9867c5ab`/`9740c364`, `e5e9ee26`/`5c23a03c`). Discard the 24 dirty gendocs files
   without review; they are byte-identical to what `release/0.7` already has.
5. **Decide `internal/acp/argv.go` from `feat/mock-engine-binary`** — NEEDS-JUDGEMENT. The
   central argv-grammar declaration and its anti-drift test never landed; `acp.go` still
   hardcodes the three flag literals. Either adopt it (discarding the branch's superseded
   `enginecli.go`) or file the gap and reap. Do not merge the branch.
6. **Confirm the `vital-deaf-stunt` session is finished** — NEEDS-JUDGEMENT. See above.
7. **Decide `feature/acp-elicitation-cancel`** — NEEDS-JUDGEMENT. Red-by-design and now
   also broken by `jsonrpc.NewConn`'s signature change. Keep as a design record or fold into
   real work; either way it will need rewriting, and every further day of drift costs more.
8. **File the `*.pb.go` fresh-worktree bootstrap papercut** — NEEDS-JUDGEMENT. Generated
   protobufs are gitignored and require a dev container, so a new worktree cannot build. A
   documented `just bootstrap` or a clear error would save every future agent the
   misdiagnosis.

## Deferred / not done

- No test suite was run (per the brief). Compile-level evidence only: `go vet` on the
  specific packages each pending commit touches.
- No live merge was performed anywhere. All merge assessments are `git merge-tree
  --write-tree` plus `git apply --index` + `go vet` inside this worktree, reverted after.
- `feat/mock-engine-binary`'s `internal/acp/argv.go` was NOT compile-tested against
  `release/0.7`. Its adoption requires rewiring `acp.go`'s three literals, which is a design
  decision, not a mechanical apply.
- The `@live` scenario `555a6d1c` was not executed against a real backend (needs
  credentials); it is verified to compile and to be absent upstream, not verified to pass.
- The three worktrees named as in-use (`worktree-triage`, `open-rows-triage`,
  `journey-coverage-gaps`) were excluded as instructed and are not classified. Note that
  `docs/journey-coverage-gaps`'s head `d4c7da2c` IS already on `release/0.7`.
