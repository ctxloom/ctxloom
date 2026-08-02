# Handoff — archaeology strip, round 2 (in flight)

**Written 2026-08-02.** Coordinator context exhausted mid-flight. Everything needed to
finish is here; nothing required is only in a chat log.

## State at handoff

- `release/0.7` = `55bd6fb3`, pushed, working tree clean, all gates green.
- **Nine agents in flight**, each in its own worktree, each on a disjoint file list.
- Nothing merged yet. `55bd6fb3` is a safe resting point — if you abandon the whole
  round, delete the nine branches and lose only comment edits.

## What round 2 is

Strip review-campaign archaeology from Go comments and doc strings, and fix comments that
contradict the code. **Round 1 already did `internal/cli`, `internal/operations`,
`internal/shared`** — 325 files, ~570 tags and ~119 codenames, merged in `55bd6fb3` and
earlier. Round 2 is the remaining 621 files.

### The rules (do not re-derive these — they were learned expensively)

1. **Strip the tag, keep the why.** Tags (`U###-F##`, `FINDING #n`, `Decision n`, `WS-n`,
   `Wave N`, `T13`/`R1`) are almost always a PREFIX on the most valuable comment in the
   file — the record of why the code resists an obvious simplification. Across 325 files
   in round 1, **not one tagged comment was bare**. Deleting them would have destroyed
   ~690 explanations.
2. **Harp-style codenames are archaeology too** (`tough-cloud`, `petty-green`,
   `oozy-plod`). Measured: **535 references tree-wide, 109 distinct harps, ALL pointing at
   CLOSED taskloom tasks, zero at open ones.**
3. **Comments and doc strings ONLY.** Never string literals — not test failure messages,
   not `t.Skip` reasons, not map values, not cobra `Short`/`Long`. Round 1 split on this
   and produced an inconsistency; round 2 forbids it outright. Report them instead.
4. **Keep**: tool sentinels (`reprise:accept-drift` — changes pre-commit behaviour), live
   `const` markers tests assert on (`DEPS-a1`, `DOCTOR-CHECK-HOOKS-TRUST-d4`), `ADR NNNN`
   / `spec §N.N` / `docs/*.md §N` citations (living documents), the codebase's own slice
   vocabulary (`ISO0`–`ISO4`, `S0`–`S8`, `B1`–`B6a`, `ST-B`–`ST-E`, `EffectiveTrust`'s
   "step 1–7", "Round 1/2"), vendor names that look like harps (`kiro-cli`, `agy`, `Nori`,
   `codex-acp`), journey vocabulary (`J1`–`J18`, `@live`/`@wip`/`@network`), and
   harp-SHAPED test fixtures (`"swift-amber-falcon"` — synthetic data, not pointers).
5. **Second job is the valuable one**: comments that no longer match the code. Round 1
   found help text advertising a deleted command, a doc naming a deleted function, and
   two contradictory paragraphs in one function.

## The nine slices

Worktrees: `/home/babbitt/workspace/worktrees/ctxloom--arch2-<slice>`
Branches: `chore/arch2-<slice>`, all cut from `55bd6fb3`
File lists: `/home/babbitt/.claude/jobs/8532d8ec/tmp/arch2/<slice>.txt`

| slice | files | scope |
|---|---|---|
| `s1-agentcoord` | 84 | `internal/agentcoord` |
| `s2-lm` | 67 | `internal/lm` |
| `s3-remote-trust` | 74 | `internal/{remote,signing,trust}` |
| `s4-ltk-config` | 80 | `internal/{ltk,config}` |
| `s5-transcript` | 57 | `internal/{transcript,sessions,memory,compression}` |
| `s6-tests` | 53 | `tests/`, `internal/{testsupport,acptest}` — `.go` ONLY |
| `s7-engines` | 67 | `internal/{mockengine,opencode,kiro,claude,antigravity,codex}` |
| `s8-acp` | 39 | `internal/{acp,acpagent,termui,vpio}` |
| `s9-rest` | 100 | long tail + all `cmd/` + `scripts/` |

**If the job dir is gone**, regenerate a file list with:
```
grep -rlE 'U[0-9]{3}-F[0-9]{2}|FINDING #[0-9]|\bDecision [0-9]+|\bWS-[0-9]|\b[TR][0-9]+\b[:,)]' \
  --include='*.go' <paths>
```

## If an agent was interrupted again

They died once already on `You've hit your weekly limit`. The recovery that worked:

1. **Preserve first.** In each dirty worktree: `git add -A && git commit --no-verify -m
   "wip(<slice>): partial archaeology strip, agent interrupted"`. Do this before anything
   else — 59 uncommitted files were at risk the first time.
2. **Probe before re-dispatching** — send one trivial agent (`reply AVAILABLE, no tools`)
   rather than firing nine into a wall.
3. **Resume, don't re-dispatch.** `SendMessage` to the existing agent keeps its file list
   and its per-package judgement. A fresh agent re-reads everything.
4. **Warn that WIP-commit files may be MID-EDIT** and must be re-verified against the file
   list, not assumed complete. An interrupted edit looks identical to a finished one.

## Merge procedure

The nine file lists are **disjoint** — verify before merging:
```
for b in <all nine>; do git diff --name-only 55bd6fb3..chore/arch2-$b; done | sort | uniq -d
```
Must print nothing. Then, per branch:
```
git -C <worktree> rebase release/0.7
git merge --ff-only chore/arch2-<slice>
```
Round 1 merged five this way with zero conflicts.

### Verify at the merge point (not per-branch)

```
just test-acceptance   # MUST be 245 scenarios / 1626 steps — a drop means code moved
just test-arch         # 0
just lint              # 0
just complexity-check  # 0
just gen-docs          # no diff on re-run
```

**Sanity check the diff is comment-only:**
```
git diff 55bd6fb3..HEAD -U0 | grep -E '^[+-]' | grep -v '^[+-][+-]' | grep -vE '^[+-]\s*//'
```
Round 1's combined diff was 325 files / 1639+ / 1668− — a near-1:1 line swap is the
signature of rewording in place. A large net deletion means someone deleted explanations.

Then reap: `git worktree remove <wt> && git branch -d chore/arch2-<slice>`, and push.

## Open items this round produced (all filed in taskloom)

- `ctxloom acp` advertises four flags it silently discards — `registerACPServerFlags(acpCmd)`
  still called in `init()` though `acpCmd` has no `RunE`. Verified: `ctxloom acp --agent foo`
  exits 0 with the value discarded.
- `diffusive-dazzler` — `run --session <unknown-harp>` panics (nil deref in
  `operations.RecordedSessionEntries`, `resume.go:22`).
- `pesky-buggy` — workspace torn down before the engine is killed, inverting the documented
  order; the comment asserts the opposite of what the code does.
- `init.feature` tests a deleted command, hidden behind `@network` so it never runs.
- Line-number-keyed allowlists (`exitcode_coverage_test.go`) needed re-pinning twice in one
  night for unrelated edits. Do not add new line-number pins.

## Not done, deliberately

- **Docs outside `.go`** were not touched this round. `docs/architecture/**` still carries
  pre-reorg spellings and stale `file:line` inventories; a mechanical sed makes them
  actively wrong, so they need real regeneration.
- **Historical records are intentionally NOT rewritten**: `docs/adr/*`,
  `docs/behaviour-changes-*`, `docs/architecture/findings-index.md`,
  `docs/cli-surface-recommendation.md`, `docs/journey-narrative-review.md`. A record that
  was true when written stays true as a record.
