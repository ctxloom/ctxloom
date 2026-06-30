# Monorepo consolidation — 7 modules → 1, everything on `v0.7.0-pre1`

## Goal & end state

Collapse the 7-module `go.work` family into **one Go module** (`github.com/ctxloom/ctxloom`)
in **one repo**, with the integration landing on the **`v0.7.0-pre1`** branch. Three shipped
binaries (`ctxloom`, `ltk`, `taskloom`) build from this single module. No `go.work`, no
cross-module pseudo-version pins, no stray ctxloom branches/worktrees.

## Decisions (LOCKED 2026-06-30)
1. **Versioning: lockstep** — one `VERSION`, one `v*` tag, one goreleaser/3 builds. ltk/taskloom
   version leap accepted. **pre1 stays the integration line for now (no release cut); it becomes
   `main` and is cut at v0.7.0 later.**
2. **Fold from current sibling tips:** shared@d657d9f, claude@085501e, codex@2768365,
   antigravity@be44816, ltk@e72958a, taskloom@57491dd. Clean claude's 3 dirty files first.
3. **All 6 siblings at once.**
4. **Prune** cruft branches (containerized-run; vscode-companion after Phase 0; old `release/*`;
   `docs/website-rewrite` — but verify it's pushed before deleting unpushed work).
5. **Move `ctxloom` binary to `cmd/ctxloom/`** — full symmetry with `cmd/ltk`, `cmd/taskloom`. The
   cobra `cmd` package relocates (→ `internal/cli`); repoint `-X …/cmd.Version` → new path in
   `justfile`, `justfile.container`, `.goreleaser.yml`.
6. **Snapshot move — history NOT preserved.** No `git filter-repo`/no pip install; copy each source
   subtree into place. Simpler Round A (cp/`git mv`, not filter-repo passes).
7. **Drop the genproto pin** (drop ltk `tool` directives, lint via devcontainer golangci-lint).
8. **Consolidate duplicated packages INLINE** (not deferred): converge shared's
   `{gitutil,ptyrunner,textutil,tokens,upgrade}` + `tasks/{paths,operations}` with ctxloom's own
   copies — requires per-package diff + canonical choice (in progress).
9. ltk `tools/` → `internal/ltk/tools/`.

> Ripple: decisions 5/6/8 revise Phases 2–4 and the haiku appendix — Round A becomes a copy (not
> filter-repo). Phases + appendix below are PARTIALLY SUPERSEDED; the LOCKED decisions + the
> Execution Log are authoritative.

## Execution Log & corrected state (2026-06-30)

**Corrected fold sources — fold each repo from its `v0.7.0-pre1` branch (the integration superset),
NOT the feature tips.** Investigation showed every sibling's pre1 contains its feature branch + more
(tooling standardization + code-review fixes). Folding from `gemini-parity`/`main` would have silently
dropped that work.

**Step 1 (merge tips → each repo's `v0.7.0-pre1`):**
- ✅ **shared/pre1**: merged `feat/gemini-parity` (the prompt→skill rename, +1) → `6d1f843`; 20 pkgs green.
- ✅ **claude, codex, antigravity, ltk, taskloom**: pre1 already contains the feature tips — no-op.
- ⏳ **ctxloom/pre1**: `multi-agent` (rename + mcp/skills profile-scoping) predates pre1's trust-rework
  → 12 security-sensitive collisions. Per decision: **re-applying fresh** on scratch branch
  `reapply/skill-rename` (keep pre1 trust-gating + rename + thread `gate`+`profileNames`); merge to
  pre1 after review + green suite.
- ✅ **website-rewrite**: unpushed → archived to tag `archived/website-rewrite`, branch pruned.

**LOCKED #8 (inline dedup) is a NO-OP:** ctxloom/pre1 already deleted the 5 duplicate leaf packages
(`gitutil`/`ptyrunner`/`textutil`/`tokens`/`upgrade`) and points at `shared/*` (byte-identical).
`paths`/`operations` are false-positive name collisions (disjoint domains; coexist). So the fold's
dedup = just the `shared/*`→`internal/shared/*` import rewrite. No semantic merges.

**Constraints to honor (from cross-plan consistency check):**
1. **Standalone-first:** `cmd/ltk`/`cmd/taskloom` must NOT import ctxloom internals (only
   `internal/shared`); dependency direction companions→shared only. Add an import-guard check in
   verification (and the `CGO_ENABLED=0` pure-Go build proof).
2. **Facet separation:** keep `SettingsWriter` separable from `Backend` (package-dependency rule now,
   not a module boundary).
3. **go 1.25+** floor on the merged go.mod (SDK requirement).
4. **Sequence:** don't run the `cmd`→`internal/cli` relocation concurrently with the in-flight
   `cli-restructure` work.
5. The multi-repo split was **packaging, not architecture** (`decoupling-shared-substrate`/`p0`:
   "extraction was always a later mechanical packaging step") — the monorepo + dedup *complete* the
   original intent rather than reversing it.

**Test baseline:** all 6 lean siblings green at their tips; ctxloom/pre1 green except the 2
`internal/projectroot` worktree-in-container artifacts (will pass in the monorepo / CI).

Driving rationale (settled earlier):

**Gate before mechanical fold:** run all module tests at current tips + read all plans + confirm
cross-module consistency (the green go.work workspace build with ctxloom in its `.Skills` state).

Driving rationale (settled earlier): nothing outside the workspace imports any family module;
they're co-developed in lockstep and released together. Go's linker dead-code elimination +
build-tagged CGO keep each binary lean regardless of one shared `go.mod`. So the multi-repo
split is all cost (the pin-cascade, host/CI skew, non-atomic cross-cutting changes) and no
benefit. One module makes cross-cutting changes atomic and deletes the friction.

---

## Current state (the mess)

### Module dependency graph (clean DAG, no cycles)
```
shared  ← antigravity, claude, codex (flat adapter libs, each depends only on shared)
        ← ctxloom   (root main.go)      → antigravity, claude, codex, shared
        ← taskloom  (cmd/taskloom)      → antigravity, claude, codex, shared
        ← ltk       (cmd/ltk)           → antigravity, claude, shared   (not codex)
```
Module paths: `shared`, `claude`, `codex`, `antigravity`, `taskloom`, and **`llm-tool-killer`**
(the `ltk` dir's module is *llm-tool-killer*, not `/ltk`). Target `ctxloom` path is unchanged.

### ctxloom branch/worktree topology (vs `main` = 7aedb7a)
| branch (worktree) | tip | rel. to pre1 | disposition |
|---|---|---|---|
| **v0.7.0-pre1** (pre1) | fd15641 | — (51 ahead of main) | **TARGET** |
| feat/vscode-companion (main) | 658f638 | fully contained in pre1 | retire; only dirty WT outside |
| multi-agent (multi-agent) | 0a486a6 | **1 commit ahead** of pre1 | **integrate** (the rename) |
| containerized-run (containerized-run) | 577a413 | 328 behind main | stale, delete |

- pre1 pins `shared@cc20d31` (has `plans`/`watch`, **pre**-rename) + `fsnotify`; uses `.Prompts`
  → internally consistent and already builds.
- `multi-agent`'s single commit *"rename prompt item-kind to skill; scope mcp/skills to selected
  profile"* (68 files, +989/−619) switches ctxloom to `.Skills` **and** bumps the pin to
  `shared@d657d9f` (post-rename HEAD). Merge brings code+pin together → consistent.
- The dirty working tree on the `main` worktree is: my `go.mod`/`go.sum` stopgap (pin `shared@4197aa2`
  — **redundant**, pre1 already pins newer) + untracked `.claude/commands/*.md` (ctxloom-generated,
  regenerate from bundles). Both discardable.

### Sibling repo source commits (each on its own feature branch)
shared `gemini-parity@d657d9f` (clean) · claude `gemini-parity@085501e` (**3 dirty**) ·
codex `gemini-parity@2768365` · antigravity `main@be44816` · ltk `harden-pwsh-flake@e72958a` ·
taskloom `feat/antigravity@57491dd`. **Confirm these are the blessed tips; commit/clean claude's
3 dirty files before the fold.**

### Investigator findings (de-risking)
- **No blocking dep conflicts.** All shared third-party deps reconcile upward via MVS (minor/patch).
  Cross-major cases are distinct `/vN` paths (`jsonschema` v5↔v6, `go-toml` v1↔v2) that coexist.
  Only smoke-test target: go-git stack bump (5.16.4→5.19.1, +billy/securejoin/sha1cd) touching
  ctxloom runtime.
- **CGO stays contained.** Only ctxloom has tree-sitter, fully behind `//go:build treesitter` in
  `internal/compression/`. ltk/taskloom default builds need no C toolchain. No onnx anywhere.
- **genproto pin can be RETIRED.** It exists only because ltk's `tool`-directive golangci-lint
  closure drags the pre-split genproto monolith into the go.work graph, colliding with ctxloom's
  gRPC need for the split `genproto/googleapis/rpc`. Fix: drop ltk's `tool` directives, lint via
  the devcontainer-installed golangci-lint (ctxloom's existing approach) → the monolith never
  enters the merged `go.mod` → `go mod tidy` drops the pin. (Fallback if lint tooling must stay in
  graph: keep the pin, harden with `exclude`/`replace`.)
- **Runtime wiring is SAFE** (binary names unchanged): every `.mcp.json` (`taskloom mcp`),
  `.claude/settings.json` hook (`ltk evaluate`, `ctxloom hook …`), `.ltk/config.yaml` keeps working.
- **Devcontainer Dockerfiles are byte-identical across all 7** → collapse to one (keep ctxloom's +
  `acceptance.Dockerfile`). `.versionator.yaml` also identical across all 7.
- **buf/proto isolated to ctxloom** and already targets the unchanged `ctxloom` module path → SAFE,
  move verbatim.

---

## Guiding decisions (with rationale)

1. **Keep ctxloom's binary at root `main.go`.** Do NOT move it to `cmd/ctxloom/`. Its cobra tree is
   `package cmd` at `github.com/ctxloom/ctxloom/cmd`; `cmd/ltk` and `cmd/taskloom` slot in as
   `package main` *subdirs* (exactly like the existing `cmd/gen-schemas`, `cmd/validate`). This
   avoids relocating the `cmd` package and keeps ctxloom's `-X …/cmd.Version` ldflags & goreleaser
   path **unchanged**. (Asymmetry is cosmetic.)
2. **Namespace folded code, don't flatten:** `internal/shared/…`, `internal/claude/…`,
   `internal/codex/…`, `internal/antigravity/…`, `internal/ltk/…`, `internal/taskloom/…`. Flatten
   would collide — ctxloom already has its own `internal/{gitutil,ptyrunner,textutil,tokens,upgrade}`
   that **duplicate** shared's. (Dedup is a deferred follow-up, not part of the mechanical move.)
3. **History-preserving merge via `git filter-repo`** (one `pip install git-filter-repo`).
   Fallback: `git subtree` (available) + restructure; last resort: snapshot (loses history).
4. **Lockstep single version.** One `VERSION` (= ctxloom's `v0.7.0`); ltk (0.0.4) and taskloom
   (0.0.1) adopt it. One `v*` tag drives one goreleaser with 3 builds. (Per-binary tag-prefix is
   the alternative — see decision points.)
5. **Land on `v0.7.0-pre1`.** Do the large fold on a short-lived branch off pre1
   (`consolidate/single-module`) and merge back, so an abort leaves pre1 intact.

---

## Phase 0 — ctxloom branch/worktree consolidation onto `v0.7.0-pre1`

*Prerequisite for everything: one coherent ctxloom line, and a green host `go.work` build.*

0.1 **Integrate `multi-agent` into pre1.** Cherry-pick the single commit (cleaner than a 3-way
   merge since we're retiring the branch):
   ```
   git -C .../ctxloom/pre1 cherry-pick 0a486a6
   ```
   **Verified conflict footprint (via `git merge-tree`): 12 files.** Resolve by keeping pre1's new
   behavior/fields AND applying the prompt→skill rename (take `.Skills`):
   - **10 content conflicts:** `cmd/item_helpers.go`, `cmd/run.go`, `internal/bundles/bundles.go`,
     `internal/bundles/loader_content.go`, `internal/config/config_bundles.go`,
     `internal/lm/backends/managed.go`, `internal/lm/backends/prompts.go`,
     `internal/operations/items.go`, `internal/operations/prompts.go`, + `go.mod`/`go.sum`.
   - **1 modify/delete:** `cmd/prompt_cmd.go` — multi-agent renamed it (→ skill command); pre1 edited
     it. Port pre1's edits onto the renamed file, drop the old one.
   - `go.mod`/`go.sum`: take `shared@d657d9f` then `go mod tidy`.
   (Merge is the alternative; cherry-pick gives one clean commit on pre1.) **OPERATOR step — judgment.**
0.2 The cherry-pick bumps the pin to `shared@d657d9f`. Run `go mod tidy`; this is where the genproto
   re-add normally happens — but it will be **retired in Phase 3**, so a temporary re-add is fine if
   tidy drops it. Build + test pre1 → green. pre1 now uses `.Skills`, matching all siblings.
0.3 **Retire `feat/vscode-companion`:** committed work is already in pre1; discard the redundant
   `go.mod` stopgap and the untracked `.claude/commands` (regenerate from bundles).
0.4 **Delete `containerized-run`** (stale, fully in main).
0.5 **Worktree cleanup:** `git worktree remove` the `main` (vscode-companion), `containerized-run`,
   `multi-agent` worktrees; delete those branches. Keep the `pre1` worktree as the working line.
0.6 **Re-point `go.work` `./ctxloom/main` → the pre1 checkout** (or run the fold from pre1) so the
   host workspace build is `.Skills` vs `shared@d657d9f` `.Skills` = **green**. This green go.work
   build is the safety net that proves the chosen sibling tips are mutually consistent before folding.
0.7 Loose ends to decide: `docs/website-rewrite` branch (a1f83e1, in-progress) and historical
   `release/*` branches — leave as history or prune (see decision points).

**Exit criteria:** pre1 is the only active ctxloom branch; `go build ./... && go test ./...` green
under go.work; all sibling tips confirmed.

---

## Phase 1 — Stabilize sibling modules

1.1 Commit or discard claude's 3 dirty files. Confirm the blessed tip per sibling (table above).
1.2 With Phase 0.6 green, the go.work build is the proof that
   `{shared@d657d9f, claude, codex, antigravity, ltk, taskloom}` at their chosen tips compile
   together with pre1. **This is the gate** — do not start the fold until it's green.

---

## Phase 2 — Repo merge mechanics (history-preserving)

Run on a fresh **clone** of each source repo (filter-repo rewrites history; never on the live
worktree). For each: (a) strip infra root files, then (b) path-rename remaining content.

**Exclude-list (strip before rename), every source:**
`go.mod go.sum .gitignore justfile VERSION .versionator.yaml .devcontainer LICENSE README.md
.golangci.yml .goreleaser.yaml lefthook.yml .mcp.json .github .claude .ctxloom .gemini .ltk`

**Path-rename rules:**
- shared/claude/codex/antigravity (root-package libs): `--to-subdirectory-filter internal/<name>`
  (carries `harp/*.txt` embeds and `claude/testdata/` automatically).
- ltk: keep `cmd/ltk/` (carries `sample.ltk.yaml`/`empty.ltk.yaml` embeds); `internal/→internal/ltk/`;
  `docs/→docs/ltk/`; `examples/→examples/ltk/`; `tests/→tests/ltk/`; decide `tools/` (→ `internal/ltk/tools`
  or top-level `tools/`).
- taskloom: keep `cmd/taskloom/`; `internal/→internal/taskloom/`; `tests/→tests/taskloom/`.

Then in the target repo (branch `consolidate/single-module` off pre1): add each rewritten clone as a
remote and `git merge --allow-unrelated-histories <remote>/<branch>`. No path collisions exist under
this mapping (verified). Manual reconciles: ltk's `tests/acceptance` (`package acceptance`) must NOT
land in ctxloom's `tests/acceptance` — the `tests/→tests/ltk/` rename handles this; verify.

---

## Phase 3 — Unify the module

3.1 **Import rewrites** across the whole merged tree (longest-match-first; `gofmt -r` or script):
   | from | to |
   |---|---|
   | `github.com/ctxloom/shared` | `…/ctxloom/internal/shared` |
   | `github.com/ctxloom/claude` | `…/ctxloom/internal/claude` |
   | `github.com/ctxloom/codex` | `…/ctxloom/internal/codex` |
   | `github.com/ctxloom/antigravity` | `…/ctxloom/internal/antigravity` |
   | `github.com/ctxloom/llm-tool-killer/cmd/ltk` | `…/ctxloom/cmd/ltk` |
   | `github.com/ctxloom/llm-tool-killer/internal` | `…/ctxloom/internal/ltk` |
   | `github.com/ctxloom/taskloom/cmd/taskloom` | `…/ctxloom/cmd/taskloom` |
   | `github.com/ctxloom/taskloom/internal` | `…/ctxloom/internal/taskloom` |
3.2 **go.mod:** drop the 4 family `require` pins (antigravity/claude/codex/shared); drop ltk's `tool`
   directives. `go mod tidy` → folds the external-dep union (MVS picks highest; `fsnotify` stays;
   go-git→5.19.1), and **drops the genproto monolith pin** (no longer in graph). Confirm gRPC still
   resolves `genproto/googleapis/rpc` (split). If an ambiguity reappears, fall back to keeping the
   pin (hardened with `exclude`).
3.3 Delete `go.work`, `go.work.sum`, `.gowork.repro.sum` at workspace root.

**Exit criteria:** `GOWORK=off go build ./... && go test ./...` green from the single module.

---

## Phase 4 — Reconcile collisions & build/release wiring

4.1 **justfile / justfile.container:** keep ctxloom recipes & `-X …/cmd.Version` as-is. Add `cmd/ltk`
   (`-X main.Version`, emit `bin/ltk` **and** install to `~/go/bin`) and `cmd/taskloom`
   (`-X main.version`) build/install targets. `install` installs all 3 binaries. `lint` keeps using
   the devcontainer golangci-lint (absorb ltk's `.golangci.yml` linter selections into ctxloom's).
4.2 **devcontainer:** delete the 6 duplicate Dockerfiles; keep ctxloom's (+ `acceptance.Dockerfile`,
   `devcontainer-lock.json`).
4.3 **CI:** merge the 3 workflow triples into ctxloom's set (build/test/lint/acceptance/mutation run
   inside the devcontainer). Drop ltk/taskloom workflows. `version-guard` on the single `VERSION`.
4.4 **goreleaser:** extend ctxloom's `.goreleaser.yml` with `ltk` (`main: ./cmd/ltk`) and `taskloom`
   (`main: ./cmd/taskloom`) builds + casks; one `v*` tag. Footers + builtin-bundle YAMLs
   (`resources/builtin_bundles/{ltk,taskloom}.yaml`): rewrite `go install
   github.com/ctxloom/{llm-tool-killer/cmd/ltk,taskloom/cmd/taskloom}@…` →
   `github.com/ctxloom/ctxloom/cmd/{ltk,taskloom}@…`.
4.5 **VERSION/versionator:** one `VERSION` (`v0.7.0`), one `.versionator.yaml`. Note the version
   jump for ltk (0.0.4→v0.7.0) and taskloom (0.0.1→v0.7.0).
4.6 **scripts/install.sh:** retire the separate-repo "companion" install path
   (`--no-taskloom`/`--no-ltk`); all 3 ship from one release.
4.7 **buf:** move `buf.yaml`/`buf.gen.yaml` verbatim (already target the unchanged ctxloom path).

---

## Phase 5 — Verify

- `just build` (ctxloom, `-tags treesitter`) + ltk + taskloom build targets.
- **Pure-Go proof:** `CGO_ENABLED=0 go build ./cmd/ltk ./cmd/taskloom` with no `treesitter` tag
  succeeds with no C toolchain.
- `GOWORK=off go build ./... && go test ./...`; acceptance (godog), integration, mutation as CI.
- go-git stack smoke test (clone/fetch paths in `internal/remote`).
- Smoke runtime: `ctxloom mcp`, `taskloom mcp`, `ltk evaluate`, ctxloom hooks
  (session-bind/stamp-plan/inject-context/hud), and the fault-tolerance invariant (`ctxloom` startup
  always ends in the LLM).

---

## Phase 6 — Decommission

- Archive the 6 source repos (read-only) once verified; keep until then for rollback.
- README/CLAUDE.md: document single repo, three binaries, no go.work.
- Update memories: `sibling-module-workspace`, `per-agent-config-delivery`,
  `taskloom-rename-and-companion-install`, `version-stamp-format`, `vscode-frontend-architecture`
  → reflect the monorepo (pin-dance and GOWORK=off divergence retired).

---

## Deferred follow-ups (separate PRs, not this migration)
- **Dedup parallel packages:** converge `internal/shared/{gitutil,ptyrunner,textutil,tokens,upgrade}`
  and `internal/shared/tasks/{paths,operations}` with ctxloom's own `internal/{…}`. (ctxloom imports
  shared 111×; this is the substrate-hoist tail.) Behavior-changing — do after the green merge.
- Optional: move ctxloom's binary to `cmd/ctxloom/` for symmetry (requires `cmd`-package relocation
  + ldflags repoint).

---

## Risks & rollback
- **Rollback:** fold on `consolidate/single-module` off pre1; abort = delete branch, pre1 intact.
  Keep source repos until Phase 5 passes.
- **genproto:** if lint tooling can't be cleanly excluded, the ambiguity recurs → keep the pin
  (hardened). Low-effort fallback, documented.
- **multi-agent integration conflicts** in `internal/lm` (Phase 0.1) — bounded, resolve toward
  `.Skills` + new client fields.
- **filter-repo rewrites SHAs** of merged content (one-time, expected).
- **Version reset** for ltk/taskloom (Homebrew users see a jump) — announce.

---

## Decision points needing your input
1. **Release versioning:** lockstep single version (recommended) vs per-binary tag prefixes.
2. **ctxloom binary location:** keep root `main.go` (recommended) vs move to `cmd/ctxloom/`.
3. **Merge tool:** `pip install git-filter-repo` (recommended) vs `git subtree` vs snapshot.
4. **Scope:** fold all 6 siblings at once (recommended, clean cut) vs stage (shared+ltk+taskloom
   first, adapters later).
5. **Loose branches:** prune `docs/website-rewrite` + old `release/*`, or keep as history.
6. **Dedup timing:** confirm parallel-package dedup is a deferred follow-up (recommended), not inline.

---

# Appendix: Parallel execution units (haiku-delegable)

Decomposed for delegation to **haiku** agents. Principles: every haiku unit is a single bounded
**mechanical** task with exact commands, disjoint file/dir scope within its round, and an explicit
**STOP-and-report** guard so haiku never improvises on a surprise. Judgment steps are pulled out as
**OPERATOR** gates (you or a stronger model) — not delegable.

Shape (serial spine, two big fan-out rounds):
```
Phase 0–1 (OPERATOR)  →  Round A ⇉ (6 haiku)  →  Gate A (A1,A2 haiku · A3 OPERATOR)
                       →  Round B ⇉ (5 haiku · B7 OPERATOR)  →  Round C ⇉ (haiku)  →  Final review (OPERATOR)
```
`⇉` = parallel. Run each round's haiku units concurrently (worktree isolation where they share the
target repo). Scratch dir for rewritten clones: `SCRATCH=/tmp/ctxloom-merge`.

### OPERATOR prerequisites (not haiku) — Phase 0 & 1
- **OP-0** Phase 0 branch consolidation incl. the `multi-agent` cherry-pick **conflict resolution**
  in `internal/lm/{grpc,backends}` (resolve toward `.Skills` + new client fields). Worktree cleanup.
- **OP-1** Phase 1: clean claude's 3 dirty files; confirm sibling tips; verify the **go.work build is
  green** (the gate that proves the chosen tips are mutually consistent). Create branch
  `consolidate/single-module` off `v0.7.0-pre1`.
- **OP-pre** Ensure `git filter-repo` is installed (`pip install git-filter-repo`) before Round A.

## Round A ⇉ — Source-repo rewrite prep (6 parallel haiku, fully independent)

Each unit clones one source repo, strips infra files, path-renames into the target layout, and
**reports its resulting top-level tree**. No two units touch the same path. Common exclude-list
(`$EXCL`) for all units:
`go.mod go.sum .gitignore justfile VERSION .versionator.yaml .devcontainer LICENSE README.md
.golangci.yml .goreleaser.yaml lefthook.yml .mcp.json .github .claude .ctxloom .gemini .ltk`

| Unit | repo @ ref | rename rule | DONE / verify |
|---|---|---|---|
| **A-shared** | `shared/main` @ d657d9f | `--to-subdirectory-filter internal/shared` | `git ls-files \| grep -vc '^internal/shared/'` ⇒ **0** |
| **A-claude** | `claude/main` @ 085501e | `--to-subdirectory-filter internal/claude` (carries `testdata/`) | all files under `internal/claude/` |
| **A-codex** | `codex/main` @ 2768365 | `--to-subdirectory-filter internal/codex` | all under `internal/codex/` |
| **A-antigravity** | `antigravity/main` @ be44816 | `--to-subdirectory-filter internal/antigravity` | all under `internal/antigravity/` |
| **A-ltk** | `ltk/main` @ e72958a | `--path-rename internal/:internal/ltk/ --path-rename docs/:docs/ltk/ --path-rename examples/:examples/ltk/ --path-rename tests/:tests/ltk/ --path-rename tools/:internal/ltk/tools/` (keep `cmd/ltk/`) | top-level dirs ⊆ {`cmd/ltk`,`internal/ltk`,`docs/ltk`,`examples/ltk`,`tests/ltk`} |
| **A-taskloom** | `taskloom/main` @ 57491dd | `--path-rename internal/:internal/taskloom/ --path-rename tests/:tests/taskloom/` (keep `cmd/taskloom/`) | top-level dirs ⊆ {`cmd/taskloom`,`internal/taskloom`,`tests/taskloom`} |

**Exact haiku recipe (parameterized — substitute SRC/REF/NAME/RENAME):**
```bash
set -e; SCRATCH=/tmp/ctxloom-merge; DEST=$SCRATCH/rw-<NAME>
rm -rf "$DEST"; git clone <SRC> "$DEST"; cd "$DEST"; git checkout <REF>
git filter-repo --force --invert-paths $(printf -- '--path %s ' <$EXCL items>)
git filter-repo --force <RENAME>
git ls-files > "$SCRATCH/tree-<NAME>.txt"   # report this listing
```
**STOP-and-report if:** clone/checkout fails; filter-repo errors; or the verify check is non-empty
(files landed outside the expected prefix). Output = path to `$DEST` + the `tree-<NAME>.txt` listing.

## Gate A — Converge (serial; A1/A2 haiku, A3 OPERATOR)

- **A1 (haiku, serial):** in `consolidate/single-module`, for each NAME in
  shared,claude,codex,antigravity,ltk,taskloom: `git remote add rw-<NAME> $SCRATCH/rw-<NAME> &&
  git fetch rw-<NAME> && git merge --allow-unrelated-histories --no-edit rw-<NAME>/<branch>`.
  **STOP-and-report on ANY merge conflict** (none expected — disjoint paths). Output: `git ls-files`
  top-level tree.
- **A2 (haiku):** apply the exact import-rewrite table (Phase 3.1) across `**/*.go` using
  `gofmt -r` per rule (longest path first), then `gofmt -l ./` must be empty. Delete `go.work`,
  `go.work.sum`, `.gowork.repro.sum`. **STOP-and-report** if any `github.com/ctxloom/{shared,claude,
  codex,antigravity,llm-tool-killer,taskloom}` import remains (`grep -rn` must be empty). Output: the
  grep result (should be empty).
- **A3 (OPERATOR):** in `go.mod` drop the 4 family `require` pins + ltk's `tool` directives; run
  `GOWORK=off go mod tidy`; confirm genproto monolith is dropped and gRPC still resolves (fall back
  to keeping/hardening the pin if ambiguity recurs); fix any compile errors. Gate: `GOWORK=off go
  build ./...` green. *(Judgment — keep with operator.)*

## Round B ⇉ — Build/release wiring (parallel haiku, worktree-isolated; B7 OPERATOR)

Each edits a **disjoint** file set on the consolidated branch (use `isolation: worktree`, then the
operator fast-forwards the commits in any order).

| Unit | files | task | DONE |
|---|---|---|---|
| **B-version** | `VERSION`, `.versionator.yaml` | set single `VERSION`=`v0.7.0`; keep one `.versionator.yaml` (ctxloom's) | files present, one each |
| **B-justfile** | `justfile`, `justfile.container` | add `cmd/ltk` (`-X main.Version`, emit `bin/ltk` + install `~/go/bin`) & `cmd/taskloom` (`-X main.version`) build/install targets; `install` does all 3; leave ctxloom `-X …/cmd.Version` untouched | `just build` + new targets parse |
| **B-goreleaser** | `.goreleaser.yml` | add `ltk` (`main: ./cmd/ltk`) + `taskloom` (`main: ./cmd/taskloom`) builds + casks under one `v*` tag | `goreleaser check` passes |
| **B-bundles** | `resources/builtin_bundles/{ltk,taskloom}.yaml` | sed `go install github.com/ctxloom/{llm-tool-killer/cmd/ltk,taskloom/cmd/taskloom}@…` → `github.com/ctxloom/ctxloom/cmd/{ltk,taskloom}@…` | old paths grep-empty |
| **B-installsh** | `scripts/install.sh` | remove separate-repo companion logic (`--no-taskloom`/`--no-ltk` / companion fetch) — exact lines provided by operator | flags gone; script lints |
| **B7-ci (OPERATOR)** | `.github/workflows/*` | merge ltk+taskloom CI triples into ctxloom's set; `version-guard` on single VERSION; drop sibling workflows | *judgment* |

**STOP-and-report if:** the named files don't exist as described, or a `check`/`parse` step fails.

## Round C ⇉ — Verification (parallel haiku; report pass/fail + last 20 lines)

| Unit | command | pass |
|---|---|---|
| **C-build-ctxloom** | `just build` (treesitter/CGO) | binary produced, exit 0 |
| **C-build-ltk** | `CGO_ENABLED=0 GOWORK=off go build -o /tmp/ltk ./cmd/ltk` | exit 0, **no C toolchain** |
| **C-build-taskloom** | `CGO_ENABLED=0 GOWORK=off go build -o /tmp/taskloom ./cmd/taskloom` | exit 0 |
| **C-test-core** | `GOWORK=off go test ./... ` (no tags) | all pass |
| **C-test-shared** | `GOWORK=off go test ./internal/shared/...` | all pass |
| **C-acceptance** | acceptance + integration as CI runs them | all pass |
| **C-smoke** | run `ctxloom mcp`/`taskloom mcp`/`ltk evaluate` help + ctxloom hooks; confirm startup ends in LLM | each responds |

**STOP-and-report** the failing command + output tail; do not attempt fixes (operator triages).

## Final review (OPERATOR)
Phase 6 decommission + memory updates; smoke-test the go-git stack bump; squash/curate history;
merge `consolidate/single-module` → `v0.7.0-pre1`.

### Running this as a Workflow (optional)
Maps directly to a `Workflow` pipeline with `model: 'haiku'` stages: Round A = `parallel([...6 thunks])`;
Gate A1/A2 = serial `agent(... model:'haiku')`; A3 = `agent(... )` at the session model; Round B =
`parallel([...])` with `isolation: 'worktree'`; Round C = `parallel([...])`. Operator gates stay
out of the workflow (run them yourself between phases). **Note:** Workflow is opt-in — say "use a
workflow" / "ultracode" to have it built and run.

