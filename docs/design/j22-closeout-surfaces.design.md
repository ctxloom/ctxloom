# J22 close-out surfaces — design

Design for the fifteen `@wip` scenarios in
`tests/acceptance/features/j22_closeout.feature`. Nothing here is implemented.

Every claim below is labelled **[V]** (verified — I read the code named, at
`111d1b4e`) or **[I]** (inferred — a judgement, not a reading).

---

## 0. Bottom line

**Genuinely new code is small; the risk is concentrated in one place.**

| Area | New code | Character |
| --- | --- | --- |
| 1. Worktrees | ~250 LOC | Refactor: split classify from act inside `internal/lm/isolation`. No new safety logic. |
| 2. Purge | ~350 LOC | Genuinely new, but it is a file-classifier plus a walk. One persisted-shape change. |
| 3. Lessons | ~450 LOC | The only area with a real unsolved problem: transcript → N fragments has no existing producer. |
| 4. Doctor (3 checks) | ~180 LOC | Three functions in the existing hardcoded slice + two small exports. |
| 5. The routine | ~20 LOC + a decision | The code is trivial; the wiring decision is not. |

**The single biggest risk: area 3's model-output contract.** There is no
existing "extract N fragments from a transcript" path anywhere in the tree
**[V]** — `memory.Compactor` writes exactly one `essence.md` **[V]** and
`operations.Distiller` compresses one named blob to one string **[V]**. So the
lessons leg has to define, for the first time, *what shape a distill skill's
output must take* for ctxloom to parse it into fragments. The feature file and
`j22_closeout.doc.md` are **silent** on this — the fixture's own SKILL.md says
only "emit, for each decision reached, one candidate fragment" with no format
**[V]**. Get that contract wrong and every house lessons skill in the world
breaks; get it "flexible" and the parser becomes the silent-no-op the journey
exists to prevent. §3.4 proposes an answer and §6 flags it as the weakest part.

**Second-biggest risk, and much cheaper to fix:** purge interacts with an
existing *silent index pruner*. `operations.ListSessionsForProject` calls
`Manager.Reconcile(isUnrecoverable)` on every listing, and `isUnrecoverable`
drops any entry whose `TranscriptPath` no longer exists **[V]**
(`internal/operations/sessions.go:28-67`). So a purge that deletes a transcript
makes the next `session list` silently delete the index row — the exact outcome
`j22_closeout.doc.md` forbids ("a session that vanishes from the index is
indistinguishable from one that never existed"). The fixture happens to dodge
this because it seeds a `summary:` on every entry **[V]**, and `Summary != ""`
short-circuits the predicate **[V]**. A real undistilled session would not.
Fixed by §2.5.

---

## 1. The four areas, confirmed against the file

The file holds **exactly 15 `Scenario:` blocks and 15 `@wip` tags** **[V]**
(`grep -c`). The brief's grouping is right in substance and wrong in two
placements:

| # | Scenario | Brief said | Actually |
| --- | --- | --- | --- |
| 1 | The routine will not start against a repo that cannot commit its own content | Doctor | **Doctor** ✓ |
| 2 | Worktrees ctxloom did not create are reported, never touched | Worktrees | **Doctor** — it runs `ctxloom doctor`, not `session worktrees` **[V]** |
| 3 | Authored artifacts in the unclassified middle… | Doctor | **Doctor** ✓ |
| 4 | The scratch worktrees are listed before anything is removed | Worktrees | Worktrees ✓ |
| 5 | Reaping removes only what it can prove is safe… | Worktrees | Worktrees ✓ |
| 6 | Her own long-lived worktrees are not this verb's business | Worktrees | Worktrees ✓ |
| 7 | A finished session's lessons become fragments a team can read | Distill | Distill ✓ |
| 8 | Lessons that ship unsigned would be withheld… | Distill | Distill ✓ |
| 9 | A session with no lessons in it fails loudly… | Distill | Distill ✓ |
| 10 | A withheld distill skill stops the run… | Distill | Distill ✓ |
| 11 | Purge shows its work before it destroys anything | Purge | Purge ✓ |
| 12 | Purge destroys the bulk, keeps the meaning… | Purge | Purge ✓ |
| 13 | Forgetting a session nobody ever summarised… | Purge | Purge ✓ |
| 14 | Purging a session does not reap the worktrees living inside it | Worktrees | **Purge** — it runs `session purge`; the worktrees are the *assertion*, not the subject |
| 15 | The close-out is itself a piece of signed content | Doctor | **A fifth area** — the routine. Nothing to do with doctor. |

Corrected split: **doctor 3 · worktrees 3 · lessons 4 · purge 4 · routine 1**.

### 1.1 The constraint nobody mentioned: the step definitions already exist

`tests/acceptance/steps_j22_closeout.go` is written and compiled **[V]**. It
pins exact substrings that every design below must produce. This is the real
specification, tighter than the prose:

| Scenario | Must appear in combined stdout+stderr **[V]** | Must be true |
| --- | --- | --- |
| 1 | `.ctxloom`, `manage gitignore install` | — |
| 2 | `proj--stale-feature`, `unmerged`, `git worktree remove`, `git branch -d` | — |
| 3 | `.plan.md`, `persist` | — |
| 4 | `ctxloom-wt-clean`, `ctxloom-wt-wip`, `999999999` | — |
| 5 | `spared`, `skipped` | `…-clean` gone; `…-wip/-unknowable/-live` on disk; WIP bytes intact |
| 6 | foreign dir path must **NOT** appear | foreign dir on disk |
| 7 | — | `team-lessons` bundle exists **and contains `fragments:`** |
| 8 | — | `team-lessons.yaml.sig` (or `team-lessons/bundle.yaml.sig`) exists, non-empty |
| 9 | — | exit ≠ 0; no `fragments:` written |
| 10 | `withheld`, `review` | exit ≠ 0; no `fragments:` written |
| 11 | `transcript`, `essence` | every seeded transcript still on disk |
| 12 | `.plan.md` | bulk gone; `essence.md` bytes intact; every harp name still in `index.yaml`; plan bytes intact |
| 13 | `undistilled` | exit ≠ 0; every transcript still on disk |
| 14 | — | scratch worktrees on disk **and still in `git worktree list --porcelain`** |
| 15 | must **NOT** contain `not found` / `no such command` / `unknown command` | — |

`LastOutput()` is **combined stdout+stderr** **[V]**
(`testenv.TestEnvironment.LastOutput`), so refusal text on stderr counts.

`j22RanRealSurface` fails any negative assertion whose invocation printed
`unknown command` / `unknown flag` **[V]** — so a half-built surface cannot
score a green safety tick.

---

## 2. Reuse inventory (read before designing anything)

Everything in this table exists today and is cited by symbol.

| Need | Existing symbol | File | Note |
| --- | --- | --- | --- |
| Find ctxloom scratch worktrees | `isolation.findEphemeralWorktrees` | `internal/lm/isolation/worktree_reap.go:174` | unexported **[V]**. Scans `HomeSessionsDir/*/ephemeral/ctxloom-wt-*`. |
| Candidate prefix | `isolation.worktreeCandidatePrefix` = `"ctxloom-wt-"` | same:~27 | unexported **[V]** |
| Owner marker | `isolation.readWorktreeOwner(wtDir) (pid int, ok bool)` | same:59 | unexported **[V]**. Sibling file `<wtDir>.owner.pid`. |
| "Provably dead" | `pidalive.Probe(pid) State` / `State.MaybeAlive()` | `internal/shared/pidalive/` | exported **[V]**. `Dead/Alive/Unsure`. |
| Dirty / ignored-content check | `isolation.unsafeToRemove(ctx, g, dir) (bool, string)` | `internal/lm/isolation/worktree.go:772` | unexported **[V]**. Error ⇒ treated as dirty. |
| Safe removal | `isolation.teardownWorktree(ctx, g, repoDir, target)` | same:711 | unexported **[V]**. Never `--force`, nested-first, re-checks safety. |
| Whole sweep | `isolation.ReapOrphanedWorktrees(ctx, git.Git) WorktreeReapResult` | `worktree_reap.go:139` | exported **[V]**. Counts only. |
| Existing CLI→isolation precedent | `cli.sweepOrphanedWorktrees(ctx, io.Writer)` | `internal/cli/startup_helpers.go:178` | **[V]** |
| git primitives | `git.Git` iface: `WorktreeList`, `WorktreeRemove`, `WorktreePrune`, `IsDirty`, `HasIgnoredContent`, `CommonDir`, `CurrentBranch` | `internal/git/git.go` | **[V]**. `WorktreeRemove` deliberately has no force escape hatch **[V]**. |
| Harp paths | `paths.HarpDir/HarpPersistDir/HarpEphemeralDir/HarpEssencePath/HarpTranscriptStoreDir/HomeSessionsDir` | `internal/paths/paths.go` | **[V]**. `HarpDir` validates the harp — the traversal chokepoint **[V]**. |
| Names | `paths.EssenceFileName="essence.md"`, `CanonicalTranscriptFileName="transcript.jsonl"`, `PlanFileExt=".plan.md"`, `EphemeralDirName`, `PersistDirName`, `TranscriptStoreDirName` | same | **[V]** |
| Session index | `sessions.Store` iface + `*Manager` + `*MemStore` | `internal/sessions/{store,index,memstore}.go` | **[V]** |
| Session ops | `operations.GetSession/ForgetSession/ListSessionsForProject` | `internal/operations/sessions.go` | **[V]** |
| Trust-gated skill read | `operations.GetSkill(ctx, cfg, GetSkillRequest{Name}) (*GetSkillResult, error)` | `internal/operations/skills.go:136` | **[V]**. Already parses `bundle#skills/name` and already returns `errs.ErrSkillWithheld`. |
| Withheld sentinels | `errs.ErrSkillWithheld`, `errs.ErrCommandWithheld` | `internal/errs/errors.go:33,38` | **[V]** |
| Add a fragment | `operations.AddItem(ctx, cfg, AddItemRequest) (*AddItemResult, error)` | `internal/operations/items.go:121` | **[V]**. Add-only; `ErrItemExists` on collision. |
| Create a bundle | `operations.CreateBundle(ctx, cfg, CreateBundleRequest)` | `internal/operations/bundles.go:144` | **[V]** |
| Sign a bundle, in-process | `operations.SignBundleFile(cfg, SignBundleRequest) (*SignBundleResult, error)` | `internal/operations/sign.go:148` | **[V]**. Existing in-process caller: `cli.bundle_push_cli.go:155` **[V]**. |
| Discover a signing key | `agentkey.NewDiscoverer().Discover(ctx, explicit)` | `internal/signing/agentkey/agentkey.go` | **[V]** |
| Session→text plumbing | `cli.compactEntry`, `memory.NewCompactor`, `operations.HistoryForBackend` | `internal/cli/session_distill.go:138` | **[V]** |
| Doctor check shape | `doctorCheck{Marker,Status,Detail}`, `doctorReport{Checks}` | `internal/cli/doctor_cmd.go:110-119` | **[V]**. No interface, no registry — a func plus one line in a slice literal **[V]**. |
| Superseded-ignore detector | `gitignore.isSupersededBlanket(line) bool` | `internal/gitignore/gitignore.go:142` | unexported **[V]**, and every exported entry point mutates **[V]**. |
| The fix command | `ctxloom manage gitignore install` | `internal/cli/manage.go:690,695` | exists **[V]** |
| Machine output | `cli.emit(cmd, data, text)` + persistent `--format` | `internal/cli/format.go:47,140` | **[V]** |
| Confirmation | `cli.promptYesNo(prompt) (bool, error)` | `internal/cli/prompt.go:39` | **[V]**. Shared `bufio.Reader` is load-bearing **[V]**. |
| Row projection convention | `cli.SessionRow` with `json`/`label`/`col` tags | `internal/cli/session_row.go:42` | **[V]** |
| Exit codes | `exitCodeRefused = 2`, `exitCodeFatalFindings = 3` | `internal/cli/startup_helpers.go:54,60` | **[V]** |

### 2.1 Conventions this design is bound by

- **`--format`, not `--json`.** The flag is persistent on `rootCmd` with five
  values (`json/yaml/toml/text/markdown`) **[V]**. `cli-ux-principles.md` §4
  forbids a sibling spelling it differently. Every new listing command below
  renders through `emit()`.
- **`--format` is enforced, not optional.** `checkFormatWasHonored` is
  `rootCmd`'s `PersistentPostRunE` and turns "accepted `--format json` and
  rendered nothing" into a hard error **[V]**; `format_coverage_test.go`'s
  `formatDebtAllowlist` statically tracks every command that has not been
  wired **[V]**. `session rename`, `session delete` and `session distill` are
  all already in that debt list **[V]**. **Every new command here wires
  `emit()` on day one**, and `session distill` gets wired as part of area 3
  (it must be, to report the `--to-bundle` payload).
- **Layering.** ADR 0019: frontends do not touch storage; `internal/operations`
  is the seam **[V]**. Purge (touches the session index) therefore goes through
  operations. Worktrees is argued in §3.1.6.
- **Destructive-flag spelling.** Both `--yes/-y` (`trust signer create` **[V]**)
  and `--force/-f` (`bundle delete` **[V]**) exist today. The feature file uses
  `--yes` **[V]**, so `--yes` it is.

---

## 3. Area 1 — `ctxloom session worktrees`

Rows 4, 5, 6.

### 3.1.1 Why this is a refactor, not a second implementation

The brief's hard constraint is that this must not become a second worktree
implementation. It does not, but the reuse is not free, and the reason is
specific:

`reapOneWorktree` **entangles classification with removal** **[V]**. It probes
the owner, resolves the repo, calls `teardownWorktree` (which mutates), and
then *infers* the verdict afterwards by stat-ing the directory
(`worktreeRemoved`) **[V]**. And every "why" — the two reason strings from
`unsafeToRemove` — goes to `clidiag.Warn` on stderr and is **never returned**
**[V]**. `ReapOrphanedWorktrees` returns three integers and nothing else **[V]**.

So there is no read-only path and no reason data. Row 4 needs both (list
before removing), and row 5 needs the reasons ("says why it left the rest").

The design **splits the existing function in two along the seam it already
has**, and keeps `ReapOrphanedWorktrees`'s signature and behaviour byte-identical
so the startup sweep and its tests are untouched:

```
ClassifyOrphanedWorktrees  (read-only; returns candidates + reasons)
        │
        ├──► session worktrees            (render)
        │
ReapWorktrees(candidates)  (acts; re-checks safety per item)
        │
ReapOrphanedWorktrees = Classify → Reap → tally   (unchanged signature)
```

No safety rule is rewritten. `teardownWorktree` and `unsafeToRemove` stay
exactly as they are, and `ReapWorktrees` calls them — which also preserves the
existing TOCTOU guard: `teardownWorktree` re-runs `unsafeToRemove` at removal
time **[V]**, so a tree that went dirty between plan and apply is still spared.

### 3.1.2 Foreign worktrees are excluded for free

Row 6 asserts a foreign worktree is never listed. `findEphemeralWorktrees` only
ever reads `HomeSessionsDir()/<harp>/ephemeral/` **[V]**; the fixture puts the
foreign tree at `$HOME/workspace/worktrees/proj--stale-feature` **[V]**. The
exclusion is structural and costs zero code. **This design adds no flag, no
option and no code path that could ever widen the candidate set to foreign
trees** — that is the point of the scenario.

### 3.1.3 CLI shape

```
ctxloom session worktrees [--reap] [--yes] [--harp <name>]
```

- Args: `cobra.NoArgs`.
- `--reap` `bool` (default `false`) — remove the trees that can be *proven*
  safe. Without it the command is read-only.
- `--yes` / `-y` `bool` (default `false`) — proceed without the per-item
  prompt, **on exactly the plan this invocation printed**. Ignored without
  `--reap`. No config key may pre-answer it.
- `--harp <string>` (default `""`) — restrict to one harp's scratch trees.

Deliberately **absent**: `--force`, `--all`, any path argument naming a tree
outside the sessions root. `git.Git.WorktreeRemove` has no force escape hatch by
construction **[V]** and this design adds none.

No `--all` because there is no default filter to escape: the finder is global
across harps already **[V]**, so §3 of the UX principles ("defaults that hide
must say so") does not bite.

Interactive behaviour: with `--reap` on a TTY and no `--yes`, print the plan
then prompt per candidate whose verdict is *reap*, via `cli.promptYesNo` **[V]**.
With `--reap` and no `--yes` on a **non-TTY**, print the plan and **do not act**
— note this is the *opposite* of `confirmSignerAdd`, which auto-confirms when
`!isInteractiveTerminal()` **[V]**. Copying that precedent onto a destructive
verb would make an unattended `--reap` destroy without consent; it is
deliberately not copied.

### 3.1.4 Exit codes

| Outcome | Code | Why |
| --- | --- | --- |
| Listing (with or without candidates) | `0` | A listing that legitimately found nothing is not a no-op; it prints `no ctxloom-owned scratch worktrees` and succeeds. |
| `--reap --yes`, unfiltered sweep, any mix of reaped/spared/skipped | `0` | `--reap`'s contract is *remove what is provably safe*. Sparing **is** the delivered effect, not a refusal. |
| `--reap --yes --harp <h>` where every candidate under `<h>` was spared/skipped | `2` | The invocation named a target and ctxloom deliberately did not remove it. This is the ladder's `2` exactly. |
| `--harp <name>` names a harp with no directory | `1` | Ordinary error. |
| Sessions root unreadable | `1` | |

The first-vs-third rows are the one judgement call here; §6 attacks it.

### 3.1.5 Go signatures — `internal/lm/isolation`

```go
// WorktreeVerdict is one candidate's outcome, in the reaper's established
// vocabulary. The unexported worktreeReapOutcome enum is replaced by this.
type WorktreeVerdict string

const (
	// VerdictReapable: orphaned AND clean. Removable; not yet removed.
	VerdictReapable WorktreeVerdict = "reapable"
	// VerdictReaped: removed by this invocation.
	VerdictReaped WorktreeVerdict = "reaped"
	// VerdictSpared: orphaned but carrying real (or unknowable) work.
	VerdictSpared WorktreeVerdict = "spared"
	// VerdictSkipped: owner alive, or owner indeterminate.
	VerdictSkipped WorktreeVerdict = "skipped"
)

// WorktreeCandidate is one ctxloom-owned scratch worktree and everything the
// reaper decided about it. Every field is a READ FACT except Verdict/Reason,
// which are the decision.
type WorktreeCandidate struct {
	Path       string          // absolute checkout dir
	Harp       string          // owning harp, from the path's own layout
	RepoDir    string          // owning repo, via git.CommonDir; "" when unresolvable
	OwnerPID   int             // 0 == no marker was written
	OwnerState pidalive.State  // Dead / Alive / Unsure
	Dirty      bool            // unsafeToRemove said so (error counts as dirty)
	Verdict    WorktreeVerdict
	Reason     string          // human-readable "why"; never empty for spared/skipped
}

// ClassifyOrphanedWorktrees inspects every ctxloom-owned scratch worktree under
// the sessions root and returns what the reaper WOULD do, mutating nothing.
// harp != "" restricts the scan to that harp's ephemeral dir.
func ClassifyOrphanedWorktrees(ctx context.Context, g git.Git, harp string) ([]WorktreeCandidate, error)

// ReapWorktrees removes exactly those candidates whose Verdict is
// VerdictReapable, RE-CHECKING each one's safety at removal time (a tree that
// went dirty between classification and this call is spared). It returns the
// candidates with Verdict updated to the outcome that actually occurred.
func ReapWorktrees(ctx context.Context, g git.Git, candidates []WorktreeCandidate) []WorktreeCandidate

// ReapOrphanedWorktrees is unchanged: Classify → Reap → tally. Signature and
// behaviour are byte-identical to today's so the startup sweep is untouched.
func ReapOrphanedWorktrees(ctx context.Context, g git.Git) WorktreeReapResult
```

`WorktreeReapResult{Reaped, Spared, Skipped int}` stays as-is **[V]**.

### 3.1.6 Go signatures — `internal/cli`

ADR 0019 says frontends go through `internal/operations`. But `internal/cli`
already calls `isolation.ReapOrphanedWorktrees` directly from
`sweepOrphanedWorktrees` **[V]**, and an operations wrapper here would be a
pure re-projection with no storage access to mediate. **Recommendation: call
`isolation` directly, matching the existing precedent, and add no operations
wrapper.** This is the least-new-code answer; §5 raises it as an open decision
because it is a layering call, not a mechanical one.

```go
// sessionWorktreeRow is the rendering projection — never the domain type,
// matching cli.SessionRow's convention.
type sessionWorktreeRow struct {
	Harp       string `json:"harp"        label:"Harp"     col:"HARP"`
	Name       string `json:"name"        label:"Worktree" col:"WORKTREE"`
	Path       string `json:"path"        label:"Path"     col:"PATH"`
	OwnerPID   int    `json:"owner_pid,omitempty" label:"Owner PID" col:"OWNER"`
	OwnerState string `json:"owner_state" label:"Owner"    col:"OWNER STATE"`
	Verdict    string `json:"verdict"     label:"Verdict"  col:"VERDICT"`
	Reason     string `json:"reason,omitempty" label:"Reason" col:"REASON"`
}

// sessionWorktreeReport is `session worktrees`'s --format json payload.
type sessionWorktreeReport struct {
	Worktrees []sessionWorktreeRow `json:"worktrees"`
	Reaped    int                  `json:"reaped"`
	Spared    int                  `json:"spared"`
	Skipped   int                  `json:"skipped"`
	Applied   bool                 `json:"applied"`
}

var sessionWorktreesCmd *cobra.Command
func runSessionWorktrees(cmd *cobra.Command, _ []string) error
func newSessionWorktreeRow(c isolation.WorktreeCandidate) sessionWorktreeRow
func renderSessionWorktrees(w io.Writer, rep sessionWorktreeReport) error
```

`Worktrees` is normalized `nil → []` so JSON renders `[]` not `null` — the same
reason `loadSessionEntries` does it **[V]**.

Registered in `session_cmd.go`'s `init()` alongside the existing six leaves **[V]**.

### 3.1.7 Meeting the pinned assertions

- Row 4 needs `ctxloom-wt-clean`, `ctxloom-wt-wip`, `999999999` **[V]**. The
  `Name` and `OwnerPID` columns carry all three. The dead-pid fixture writes
  `999999999` into `.owner.pid` **[V]**, `readWorktreeOwner` parses it **[V]**,
  and it renders verbatim. ✔
- Row 5 needs the literals `spared` and `skipped` **[V]** — they are the
  `WorktreeVerdict` values, rendered in the `VERDICT` column. ✔
- Row 5 needs the WIP tree's `in-flight.go` bytes intact **[V]**. The `-wip`
  fixture has a *dead* owner and an untracked file **[V]**, so it is orphaned
  but `unsafeToRemove` reports dirty ⇒ `VerdictSpared`, never removed. ✔
- Row 5 needs `-unknowable` (no marker) and `-live` (this pid) untouched.
  `readWorktreeOwner` returns `ok=false` with no marker **[V]** and the reaper
  treats that identically to alive **[V]** ⇒ `VerdictSkipped`. ✔
- Row 6 needs the foreign path absent from output — §3.1.2. ✔

---

## 4. Area 2 — `ctxloom session purge`

Rows 11, 12, 13, 14.

### 4.1 Classification is an ALLOWLIST, not a denylist

This is the safety spine. A denylist ("delete everything except these") means
every unrecognised file is destroyed by default — which is how a cleanup eats a
design note. So:

**A file is destroyed only if it matches an explicitly enumerated machine-bulk
rule. Everything else is kept.**

| Class | Rule | Purge (default) | `--everything` |
| --- | --- | --- | --- |
| **Machine** | a file named `transcript.jsonl` or `transcript.acp.jsonl` at the harp top level or under `persist/`; **everything** under `persist/transcripts/`; the file `entry.TranscriptPath` points at, *only when it resolves inside this harp's directory* | destroy | destroy |
| **Derived** | `essence.md` (`paths.EssenceFileName`) | keep | destroy |
| **Index** | the `sessions.Entry` row | keep, marked purged | keep, marked purged |
| **Authored** | any other regular file anywhere under the harp dir except `ephemeral/` | **keep and NAME in the report** | **keep and NAME; exit 2** |
| **Scratch** | everything under `ephemeral/` | **never even walked** | **never even walked** |

Notes:

- `paths.HarpCanonicalTranscriptPath` is `<harp>/persist/transcript.jsonl`
  **[V]**, but the J22 fixture plants bulk at `<harp>/transcript.jsonl` (top
  level) and points `transcript_path` there **[V]**. Matching by *name at either
  location* covers both without guessing, and `transcript.acp.jsonl` is the
  documented legacy name **[V]**.
- The `entry.TranscriptPath` rule is deliberately fenced to paths inside the
  harp dir. A vendor transcript under `~/.claude/projects/…` is **not ours to
  delete** — that is the "no touching vendor stores" line of the refusal list.
- `ephemeral/` is skipped by the *walk itself*, not filtered afterwards. Row 14
  asserts the scratch worktrees stay on disk **and stay registered with git**
  **[V]**; a recursive delete would deregister them. Purge runs **zero git
  commands**.

### 4.2 CLI shape

```
ctxloom session purge <harp-name> [--yes] [--everything] [--undistilled]
```

- Args: `cobra.ExactArgs(1)`.
- `--yes` / `-y` `bool` (default `false`) — apply the plan this invocation
  printed. Without it the command **only reports**.
- `--everything` `bool` (default `false`) — also destroy the derived essence.
- `--undistilled` `bool` (default `false`) — the extra deliberate flag that
  permits `--everything` against a session with no essence. Meaningless (and a
  usage error, exit `1`) without `--everything`.

Deliberately absent: `--force`, any flag that destroys authored files, any
multi-harp/glob form, any age-based retention policy. All deferred (§7).

**Read-only default, on a TTY too.** Bare `session purge <harp>` prints the plan
and exits — this is row 11's whole point, and the assertion is that *every byte
of every session* is still on disk **[V]**. On a TTY with `--yes` absent the
command still only reports; per-item confirmation applies when the user then
re-runs with `--yes`, or, on a TTY, via a prompt per destroy-item after the plan
is rendered. `--yes` batches only what this invocation's plan showed.

### 4.3 Exit codes

The rule: **`2` when the invocation asked for the thing ctxloom withheld;
`0` when withholding is the verb's own contract.**

| Outcome | Code |
| --- | --- |
| Plan printed (no `--yes`) | `0` — the plan is the delivered effect |
| `--yes`: bulk destroyed, essence + authored kept and named | `0` — keeping them is plain purge's contract |
| `--yes`: nothing matched the machine-bulk rules (nothing to free) | `0`, with `freed 0 bytes; nothing matched` — see §6 |
| `--everything --yes` against a session with no essence, no `--undistilled` | **`2`**, message names the harp and the word `undistilled`; nothing touched |
| `--everything --yes --undistilled` with authored files present | **`2`** — `--everything` literally asked for everything, and authored work was deliberately withheld; the bulk and essence *are* destroyed and the authored files are named |
| session is live (`EndedAt == nil`) | **`2`**; nothing touched |
| harp not in the index | `1` |
| `--undistilled` without `--everything` | `1` (usage) |
| I/O failure mid-destroy | `1`, reporting what was already freed |

Row 13 asserts exit ≠ 0 and the literal `undistilled` **[V]**. ✔

### 4.4 Go signatures — `internal/operations`

```go
// PurgeClass names one content class in a harp directory (j22_closeout.doc.md
// §"three content classes"). The class decides the ACTION; the action is never
// decided per-file at the call site.
type PurgeClass string

const (
	PurgeClassMachine  PurgeClass = "machine"
	PurgeClassDerived  PurgeClass = "derived"
	PurgeClassAuthored PurgeClass = "authored"
)

// PurgeItem is one classified path in the plan. Action is "destroy" or "keep";
// Reason is always populated for "keep" so a kept file is never silently kept.
type PurgeItem struct {
	Path   string     `json:"path"`
	Rel    string     `json:"rel"`
	Class  PurgeClass `json:"class"`
	Bytes  int64      `json:"bytes"`
	Action string     `json:"action"`
	Reason string     `json:"reason,omitempty"`
}

type PurgeSessionRequest struct {
	Harp        string
	Everything  bool
	Undistilled bool
	// Apply is the plan/act switch. False walks and classifies only.
	Apply bool
	// Confirm, when non-nil and Apply is true, is consulted per destroy-item.
	// Returning false keeps that item. nil means "batch" (the --yes path).
	Confirm func(PurgeItem) bool
}

type PurgeSessionResult struct {
	Harp       string      `json:"harp"`
	Applied    bool        `json:"applied"`
	Destroy    []PurgeItem `json:"destroy"`
	Keep       []PurgeItem `json:"keep"`
	BytesFreed int64       `json:"bytes_freed"`
	// Withheld names what the invocation ASKED for and ctxloom refused. Non-empty
	// is exactly the exit-2 condition; the CLI never re-derives it.
	Withheld []string   `json:"withheld,omitempty"`
	PurgedAt *time.Time `json:"purged_at,omitempty"`
}

// PurgeSession classifies a harp's contents and, when req.Apply, destroys
// exactly the machine-written bulk (plus the derived essence under
// --everything), never descending into ephemeral/ and never running git.
//
// It returns ErrPurgeLiveSession / ErrPurgeUndistilled rather than acting when
// a refusal applies; the plan is still returned alongside so the caller can
// show its work with the refusal.
func PurgeSession(ctx context.Context, cfg *config.Config, req PurgeSessionRequest) (*PurgeSessionResult, error)

// MarkSessionPurged stamps the index entry so the row survives its transcript.
func MarkSessionPurged(harp string, at time.Time) error

var (
	ErrPurgeLiveSession = errors.New("session is still live")
	ErrPurgeUndistilled = errors.New("session was never distilled")
)
```

### 4.5 The index-pruning interaction (must-fix)

`isUnrecoverable` drops any entry whose bound transcript is gone, unless it has a
`Summary`, a `Detail`, or a `CanonicalTranscriptPath` **[V]**. Purge deletes
transcripts. Therefore, without a change, `session purge` followed by
`session list` **silently deletes the index row** of any undistilled session.

Fix — three lines, one persisted-shape change:

```go
// internal/sessions/index.go — sessions.Entry gains:
	// PurgedAt records when `ctxloom session purge` destroyed this session's
	// machine-written bulk. It is why the row survives its own transcript:
	// a purged session must stay visible, marked, rather than vanish.
	PurgedAt *time.Time `yaml:"purged_at,omitempty" json:"purged_at,omitempty"`

// internal/sessions/store.go — Store gains:
	MarkPurged(harpName string, at time.Time) error

// internal/sessions/index.go and memstore.go each implement it.

// internal/operations/sessions.go — isUnrecoverable gains, first:
	if e.PurgedAt != nil {
		return false // purged on purpose: the row is the record now
	}
```

Additive + `omitempty`, so absent reads as zero and **no index schema upgrade is
required** **[V]** (`indexUpgrades` pipeline exists but is not needed). It is
still a change to a persisted file shape ⇒ **OPEN DECISION** (§5.2).

`session list`/`SessionRow` should render a `purged` marker so the row is
legible; that is one added column and is in scope for the same change.

### 4.6 `purge` vs the existing `delete`

`ctxloom session delete <harp>` already exists and drops **only the index row**,
leaving files on disk **[V]**. `purge` is its exact complement: destroy bytes,
keep the row. They compose — `purge --everything --yes` then `delete` is total
erasure — so `--everything` does **not** need to remove the index row, and the
doc's "stays in the index marked purged" holds for both.

`docs/cli-surface-recommendation.md:401` anticipated a `delete --purge` instead
**[V]**; the feature file settled on a separate verb **[V]** and this design
follows the feature file. ADR 0002 (`skip-ctxloom-gc`, status Deferred) declines
a *cache* GC verb **[V]** and does not conflict, but should be cross-referenced.

### 4.7 Go signatures — `internal/cli`

```go
var sessionPurgeCmd *cobra.Command
func runSessionPurge(cmd *cobra.Command, args []string) error

type sessionPurgeRow struct {
	Rel    string `json:"rel"    label:"Path"   col:"PATH"`
	Class  string `json:"class"  label:"Class"  col:"CLASS"`
	Action string `json:"action" label:"Action" col:"ACTION"`
	Bytes  int64  `json:"bytes"  label:"Bytes"  col:"BYTES"`
	Reason string `json:"reason,omitempty" label:"Reason" col:"REASON"`
}

func renderSessionPurgePlan(w io.Writer, res *operations.PurgeSessionResult) error
```

Row 11 needs the literals `transcript` and `essence` in the plan **[V]** —
both appear as `PATH` values. Row 12 needs `.plan.md` in the output **[V]** — it
is a `keep`/`authored` row, and the design's rule that kept items are *named* is
what puts it there.

---

## 5. Area 3 — `session distill --skill --to-bundle`

Rows 7, 8, 9, 10. The largest and least-certain area.

### 5.1 What is missing

- `session distill` is `ExactArgs(1)` with **zero flags** **[V]**.
- Its output is hard-wired to `essence.md` inside `memory.Compactor.saveEssence`
  **[V]**.
- `cli.loadDistillPrompt` — the function the feature file blames — is **not on
  the `session distill` path at all** **[V]**. Its only caller is
  `newLLMDistillerForLabel`, which serves `bundle distill` / `fragment distill`
  / item edits **[V]**. `session distill` goes `compactEntry → memory.NewCompactor`,
  whose prompt is internal to `internal/memory` **[V]**.

  **This is a factual correction to the specification prose.** The defect
  `loadDistillPrompt` embodies is real and exactly as described — it discards
  every `GetCommand` error, `ErrCommandWithheld` included, and silently falls
  back to `defaultDistillPrompt` **[V]** — but it is the *bundle* distiller's
  bug, not the session distiller's. Row 10 is nonetheless satisfiable, and more
  cleanly: the new `--skill` path resolves through `operations.GetSkill`, which
  **already** returns `errs.ErrSkillWithheld` **[V]**. The work is to *not*
  swallow it. Fixing `loadDistillPrompt` itself is a separate, worthwhile change
  that row 10 does not require — see §7.
- Nothing anywhere produces N fragments from a transcript **[V]**.
- No `--degraded` flag exists anywhere in the CLI **[V]**, despite the feature
  file naming it as the only escape hatch **[V]**.

### 5.2 CLI shape

```
ctxloom session distill <harp-name> [--skill <ref> --to-bundle <name>] [--degraded] [--llm <label>]
```

- Args: `cobra.ExactArgs(1)` (unchanged).
- `--skill <string>` (default `""`) — a skill ref in the existing
  `<bundle>#skills/<name>` grammar **[V]**, resolved trust-gated.
- `--to-bundle <string>` (default `""`) — the local bundle extracted fragments
  land in. Created if absent.
- **`--skill` and `--to-bundle` must be given together.** Either alone is a
  usage error (exit `1`) naming the other. Every J22 scenario passes both
  **[V]**, and requiring the pair halves the surface: there is no second mode
  where a skill silently changes what `essence.md` says.
- `--degraded` `bool` (default `false`) — proceed with the built-in prompt when
  the named skill is withheld. Warns loudly on stderr. Meaningless without
  `--skill` (usage error).
- `--llm <string>` (default `""`) — model label override, mirroring
  `bundle distill --llm` **[V]**.

With neither `--skill` nor `--to-bundle`, behaviour is **byte-identical to
today** — this is purely additive (UX principle §10).

### 5.3 Order of operations, and the signature trap

**Write, then sign. Never sign, then write.** `bundles.fsStore.Save`
**deletes an outdated detached signature** as part of every bundle mutation
**[V]** (`invalidateStaleSignature`). A sign-then-write ordering silently
produces an unsigned bundle — manufacturing exactly the defect row 8 exists to
prevent. Sequence:

1. Resolve the skill (trust gate). Withheld ⇒ refuse, unless `--degraded`.
2. Load the session's text.
3. Run extraction. **Zero fragments ⇒ refuse. Nothing is written. Stop.**
4. `CreateBundle` (if absent) then `AddItem` per fragment.
5. `SignBundleFile` over the now-final bundle.
6. Report counts.

Step 3 before step 4 is what makes row 9's "nothing was written into the
`team-lessons` bundle" **[V]** true rather than merely likely.

### 5.4 The model-output contract (the weak point)

Nothing in the spec says what a lessons skill emits. The design's answer:

**ctxloom owns the SHAPE; the skill owns the STYLE.** ctxloom appends its own
fixed output-contract block to the skill body before sending it to the model:

```
<skill body, verbatim>

--- (ctxloom output contract; not part of the skill) ---
Reply with a single fenced ```json block holding an array of objects:
  [{"name": "<kebab-case-slug>", "content": "<the fragment body, markdown>",
    "tags": ["..."]}]
Reply with [] if the session contains no lessons worth keeping.
```

Consequences, stated rather than hidden:

- A house skill cannot change the output shape — only the selection criteria and
  the prose voice. That is a real constraint on what "a team ships its house
  extraction style" can mean.
- An unparsable reply is **exit `1`**, with the raw reply written to
  `<harp>/lessons-raw.txt` (an *authored*-class file, so a later purge keeps it)
  and named in the error. It is never a silent zero.
- `[]` is a legitimate model answer and lands on the row-9 refusal path.

This is the part of the design I would attack first (§6).

### 5.5 Exit codes

| Outcome | Code |
| --- | --- |
| N ≥ 1 fragments written and the bundle signed | `0` |
| Named skill is withheld, no `--degraded` | **`2`** — refused; message contains `withheld` and `run \`ctxloom review\`` |
| `--degraded` with a withheld skill | `0`, loud stderr warning naming the skill that was *not* used |
| Extraction produced zero fragments | **`2`** — completed, and deliberately did not write the empty bundle it was asked to write. This is §7's own argument for `2`: exit `0` would make an unattended run that refused indistinguishable from one with nothing to do. |
| Model reply unparsable | `1` |
| No LLM label resolves | `1` — it must **not** degrade to "stored raw", which is what `newLLMDistillerForLabel` does today **[V]** |
| Write succeeded, signing failed (no key, agent down) | `1`, naming `ctxloom bundle sign <name>`; the fragments are kept |
| Skill ref not found | `1` |
| `--skill` without `--to-bundle` (or vice versa), `--degraded` alone | `1` (usage) |

Rows 9 and 10 assert only "exit ≠ 0" **[V]**, so `2` is safe; it is also the
categorically correct answer.

The zero-fragment case is the one a reviewer will push on — a plausible
alternative reading is `1` ("the extraction failed to deliver"). I chose `2`
because ctxloom *completed the extraction* and then *deliberately declined the
write*; the deliberate declining is what `2` names.

### 5.6 Go signatures — `internal/memory`

The session text loader is inside `Compactor.loadSessionToCompact` and the
text builder is `appendEntryText`, both unexported **[V]**. A narrow export
reuses all of the source resolution (canonical fallback, preloaded session,
retired-scraper handling) instead of re-deriving it:

```go
// SessionText loads the session named by cfg and renders it as the SAME
// markdown text Compact() feeds the model — without compacting, without an LLM
// call, and without writing anything. It is the read half of Compact, exported
// so a caller that wants a different transformation over the same input does not
// re-implement session resolution.
func SessionText(ctx context.Context, cfg CompactionConfig) (string, error)
```

### 5.7 Go signatures — `internal/operations`

```go
// LessonFragment is one candidate fragment an extraction produced.
type LessonFragment struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
}

// LessonExtractor is the model seam, mirroring operations.Distiller: the
// operations layer owns the flow, the CLI owns the LLM wiring.
type LessonExtractor interface {
	Extract(ctx context.Context, req ExtractLessonsRequest) (ExtractLessonsResult, error)
}

type ExtractLessonsRequest struct {
	Harp        string
	SessionText string
	// Prompt is the resolved skill body plus ctxloom's output contract (§5.4).
	Prompt string
}

type ExtractLessonsResult struct {
	Fragments []LessonFragment
	ModelID   string
	// Raw is the model's unmodified reply, kept so an unparsable answer can be
	// preserved rather than discarded.
	Raw string
}

type DistillToBundleRequest struct {
	Harp      string
	SkillRef  string
	Bundle    string
	Degraded  bool
	Extractor LessonExtractor
	Signer    ssh.Signer
	Store     bundles.Store
}

type DistillToBundleResult struct {
	Harp      string   `json:"harp"`
	Bundle    string   `json:"bundle"`
	SkillRef  string   `json:"skill_ref"`
	SkillUsed string   `json:"skill_used"` // "" when --degraded fell back
	Written   []string `json:"written"`
	Skipped   []string `json:"skipped,omitempty"` // names that already existed
	Signed    bool     `json:"signed"`
	SigPath   string   `json:"sig_path,omitempty"`
	ModelID   string   `json:"model_id,omitempty"`
}

// DistillSessionToBundle resolves the skill through the trust gate, extracts
// lessons from the session, writes them as fragments, and signs the bundle — in
// that order, because bundles.fsStore.Save invalidates a stale signature.
//
// It returns ErrNoLessonsExtracted BEFORE writing anything when the extraction
// yields zero fragments: an empty bundle is never written.
func DistillSessionToBundle(ctx context.Context, cfg *config.Config, req DistillToBundleRequest) (*DistillToBundleResult, error)

var (
	ErrNoLessonsExtracted = errors.New("the session produced no lessons")
	ErrLessonsUnparsable  = errors.New("the extraction reply could not be parsed")
)
```

### 5.8 Go signatures — `internal/cli`

```go
var (
	sessionDistillSkill    string
	sessionDistillToBundle string
	sessionDistillDegraded bool
	sessionDistillLLM      string
)

// runSessionDistill gains a branch: with --skill/--to-bundle it runs the
// lessons path, otherwise it is unchanged. It is wired to emit() as part of
// this change, clearing its formatDebtAllowlist entry.
func runSessionDistill(cmd *cobra.Command, args []string) error

// resolveDistillSkillPrompt resolves ref through operations.GetSkill and
// returns the skill body plus ctxloom's output contract. Unlike
// loadDistillPrompt it returns its error: errs.ErrSkillWithheld reaches the
// caller intact so a trust decision can never silently change which prompt ran.
func resolveDistillSkillPrompt(ctx context.Context, cfg *config.Config, ref string, degraded bool) (prompt string, used string, err error)

// newLessonExtractor builds the LLM-backed extractor, reusing distillWithModel.
func newLessonExtractor(cfg *config.Config, label string) operations.LessonExtractor
```

`newLessonExtractor` reuses `cli.distillWithModel(ctx, llmName, llmLabel, model,
env, name, content, distillPrompt, siblingCtx)` **[V]** — the existing LLM call
path — rather than opening a new one.

### 5.9 Meeting the pinned assertions

- Row 7 wants `.ctxloom/content/bundles/team-lessons.yaml` (or the directory
  form) carrying a `fragments:` section **[V]**. `CreateBundle` writes the
  single-file form to `paths.LocalBundlesPath` **[V]**; `AddItem` adds fragments
  **[V]**. ✔
- Row 8 wants `team-lessons.yaml.sig` non-empty **[V]**. `SignBundleFile` on the
  single-file form writes `<path>.yaml.sig` **[V]**, and the fixture starts a
  hermetic ssh-agent with a live key **[V]**. ✔
- Row 10 wants `withheld` and `review` in the output **[V]**. The refusal message
  is modelled on `bundles.Reason.Explain`'s existing wording — "awaiting review
  — run `ctxloom review`" **[V]** — with "withheld" added. Note
  `reviewCmd` is `cobra.NoArgs` **[V]**, so the message must say `ctxloom
  review` bare, never `ctxloom review <bundle>` (which `skill_cmd.go:383`
  currently prints and the CLI would reject **[V]** — a small pre-existing bug
  worth filing).

---

## 6. Area 4 — three doctor checks

Rows 1, 2, 3.

### 6.1 Shape and exit code

Doctor has **no check interface and no registry**: a check is a package-level
func returning `doctorCheck`, added to one hardcoded slice in `runDoctorCmd`
**[V]**. Adding three checks is three functions plus three lines.

**Doctor's exit code does not change.** It is documented as never failing on a
check outcome **[V]**, and `TestDoctorCmd_AlwaysExitsCleanEvenWhenMisconfigured`
pins it **[V]**. Severity is `ok`/`warn`/`info` with **no `fail`** **[V]**;
`warn` *is* the fail-loud signal.

This matters for row 1, whose title says the routine "will not start". The
*routine* stops — that is the LLM-driven `cleanup` command's job, reading
doctor's report. `doctor` itself still exits `0`. **No `exitCodeFatalFindings`
(`3`) is introduced here**; `3` is reserved for strict-mode startup aborts
**[V]** and doctor is not a startup path. All three scenarios assert output
content only **[V]**, so this is consistent with the spec.

### 6.2 Check 1 — gitignore posture

```go
// doctorCheckGitignorePosture reports a superseded blanket `.ctxloom` ignore
// rule: under it, .ctxloom/content can never be committed at all, so a project
// cannot ship its own authored context.
func doctorCheckGitignorePosture(cfg *config.Config, cfgErr error) doctorCheck
// marker: DOCTOR-CHECK-GITIGNORE-q7
```

The detector `gitignore.isSupersededBlanket` is unexported, and every exported
entry point **mutates the file** **[V]**. A doctor check must not write. New
read-only export:

```go
// package gitignore

// SupersededBlanketLines returns the blanket `.ctxloom` ignore lines present in
// the file at path, reading only. Empty means the posture is fine; a missing
// file is empty, not an error.
func SupersededBlanketLines(path string) ([]string, error)
```

Detail on warn, containing both pinned substrings **[V]**:

```
.gitignore carries a blanket `.ctxloom/*` rule; .ctxloom/content can never be
committed under it (run `ctxloom manage gitignore install` to retire it)
```

### 6.3 Check 2 — foreign worktrees (report only, forever)

```go
// doctorCheckForeignWorktrees reports long-lived worktrees this repository has
// that ctxloom did NOT create — everything outside the sessions root. Report
// only: ctxloom removes no worktree it did not create, so the report carries the
// exact commands a human runs instead.
func doctorCheckForeignWorktrees(ctx context.Context, g git.Git, workDir string) doctorCheck
// marker: DOCTOR-CHECK-FOREIGN-WORKTREES-r8
```

Algorithm: `g.WorktreeList(ctx, repoDir)` **[V]** → drop the main worktree and
anything under `paths.HomeSessionsDir()` → per remaining tree report
`filepath.Base(path)`, its branch, `g.IsDirty` **[V]**, whether its branch is
merged, and the two commands.

**A merged-ness primitive does not exist** **[V]** — no `merge-base`,
`branch --merged`, or `IsMerged` anywhere in `internal/git` or
`internal/shared/gitutil`. Row 2 asserts the literal `unmerged` **[V]**, and
printing it unconditionally would be a lie. One new method on the existing
interface (no new dependency — `git` is already shelled out **[V]**):

```go
// package git — added to the Git interface, execGit, and Fake.

// MergedBranches returns the branch names already merged into ref.
func MergedBranches(ctx context.Context, repoDir, ref string) ([]string, error)
// exec form: git branch --merged <ref> --format=%(refname:short)
```

`ref` defaults to the repository's current HEAD branch. Adding to the `Git`
interface obliges a matching `git.Fake` method **[V]**.

Detail on warn (all four pinned substrings **[V]**):

```
1 worktree ctxloom did not create: proj--stale-feature (branch stale-feature,
unmerged, dirty) — ctxloom will not remove it; run
`git worktree remove <path>` then `git branch -d stale-feature`
```

`git branch -d` (safe) never `-D` — the refusal list forbids `-D` **[V]**, and
a report that hands a human `-D` has force-removed by proxy.

### 6.4 Check 3 — the unclassified middle (B13)

```go
// doctorCheckHarpDurability warns about authored artifacts sitting at a harp
// directory's TOP LEVEL — neither persist/ (mounted into containers) nor
// ephemeral/ (deliberately excluded). A containerized agent writing a design
// note there writes into container-ephemeral space and loses it on exit.
func doctorCheckHarpDurability() doctorCheck
// marker: DOCTOR-CHECK-HARP-DURABILITY-s9
```

Walks `paths.HomeSessionsDir()/<harp>/` one level, ignores `essence.md`,
`transcript.jsonl`, `index.yaml` and the `persist`/`ephemeral` dirs, and names
whatever is left (capped at ~5 with a count). Detail contains both pinned
substrings **[V]**:

```
3 authored file(s) sit in a harp directory's unclassified top level, which is
neither persist/ (durable, mounted into containers) nor ephemeral/:
amber-quiet-heron/amber-quiet-heron.plan.md, … — move them under persist/
```

Deliberately does **not** fix anything: the doc recommends extending the
plan-stamping convention rather than widening the mount **[V]**, which is a
separate change.

### 6.5 Registration

Three lines added to the full-report slice in `runDoctorCmd` **[V]**; none added
to the `--deps` subset (they are project-state questions, not machine-capability
probes). The `--format json` shape is unchanged: the same
`{"checks":[{marker,status,detail}]}` **[V]**.

---

## 7. Area 5 — the routine (`ctxloom run -r cleanup -n`)

Row 15. **This is not a matter of dropping a markdown file, and that is the
finding.**

`run -r <name>` resolves through `operations.GetCommand` **[V]**
(`internal/cli/run.go:528`), which uses `Config.BundleLoader()` **[V]**.
`BundleLoader` composes **project reader + remote readers + companion reader**
**[V]** (`internal/config/config.go:2054-2057`). It does **not** include
`bundles.NewBuiltinReader()` — whose only caller in the entire tree is
`config_bundles.go:832` **[V]**, a narrow kind-specific resolver.

So neither of the obvious moves works:

- `resources/commands/cleanup.md` — those are exported as *engine slash
  commands* via `internal/lm/backends.builtinCommands` **[V]** and are invisible
  to `run -r`.
- `resources/builtin_bundles/cleanup.yaml` — invisible to `run -r` until the
  builtin reader is added to `BundleLoader`.

And the feature's phrase "shipped, **signed** bundle command" cannot be taken
literally: `NewBuiltinReader` documents builtins as **deliberately unsigned**,
because "signing bytes with a key embedded in the binary that verifies them is
circular" **[V]**. They are trusted as `trust.SourceBuiltin` — authenticated by
the binary — and remain rejectable **[V]**. The honest reading of "signed" here
is "first-party authenticated", and the design says so rather than papering over
it. The step assertion only checks that resolution does not print
`not found`/`unknown command` **[V]**, so nothing in the test depends on a
literal signature.

Recommended shape (pending §8.4):

```yaml
# resources/builtin_bundles/cleanup.yaml
version: 1.0.0
author: ctxloom
description: The close-out routine — preconditions, worktrees, lessons, retention.
commands:
  cleanup:
    content: |
      <the four-leaf routine, in check-triggers' style>
```

plus one line in `Config.BundleLoader`:

```go
readers := []bundles.Reader{bundles.NewBuiltinReader()}   // FIRST = lowest precedence
readers = append(readers, bundles.NewProjectReader(...))
// … unchanged
```

Builtin goes *first* so a project or remote bundle of the same name shadows it
**[I]** — precedence is documented as "a later reader wins a name collision"
**[V]**.

---

## 8. Open decisions for the human

### 8.1 `sessions.Entry` gains `PurgedAt *time.Time` — a persisted-shape change

- **Recommendation: yes.**
- **Trade-off:** it changes `~/.ctxloom/sessions/index.yaml`'s shape. It is
  additive and `omitempty`, so an older binary ignores it and no schema upgrade
  is needed **[V]** — but the field is also written into the `--format json` wire
  read by the VSCode companion. **Without it, purge + list silently deletes the
  index row** of any undistilled session (§4.5), which contradicts the doc
  directly. The alternative (clearing `TranscriptPath` on purge) reuses the
  "pending/unbound: still in progress" branch of `isUnrecoverable` **[V]** and
  would make purged sessions render as *pending* — a worse lie.

### 8.2 `git.Git` gains `MergedBranches`

- **Recommendation: yes.** No new module dependency; `git` is already exec'd
  **[V]**.
- **Trade-off:** the `Git` interface is a broad seam — adding a method obliges
  `git.Fake` and every other implementation **[V]**. The alternative is printing
  "unmerged" without checking, which is a fabricated claim in a report whose
  whole purpose is telling a human what is safe to delete.

### 8.3 `gitignore` gains a read-only `SupersededBlanketLines`

- **Recommendation: yes.** Small, and a doctor check must not mutate.
- **Trade-off:** it widens `gitignore`'s exported surface with a second way to
  ask about the same condition, next to the mutating `RetireSupersededFile`.
  Mitigated by having `RetireSupersededFile` call it, so there is one detector.

### 8.4 Add `bundles.NewBuiltinReader()` to `Config.BundleLoader`

- **Recommendation: yes**, at lowest precedence — it is the only way `run -r
  cleanup` resolves without a network fetch (§7).
- **Trade-off, and it is the real one:** this makes **every** builtin-bundle
  item visible to **every** content surface — `fragment list`, `command list`,
  `skill list`, MCP resources. Today `resources/builtin_bundles/isolation.yaml`'s
  fragment is *injected* into assembly and never listed **[V]**; after this it
  becomes listable and selectable. That is a trust-adjacent behaviour change
  across many surfaces. The narrower alternative — a builtin reader only inside
  `GetCommand` — has smaller blast radius but ships a command that `run -r`
  resolves and `command list` cannot show, violating UX principle §5
  (discoverability). **This one genuinely needs a human.**

### 8.5 Does `--everything` destroy human-authored files?

- **The spec is silent.** The doc's three-class list says authored work is
  "never negotiable" without qualification **[V]**; the `--everything` prose says
  "the offboarding, subpoena, leaked-secret day" **[V]**, which implies otherwise.
- **Recommendation:** spare them, name them, exit `2`. Ship **no** flag that
  destroys authored files in this change.
- **Trade-off:** a genuine leaked-secret in a `.plan.md` is not removable by
  ctxloom, and the user must `rm` it. That is the correct failure direction for
  a first version, and a later `--including-authored` can be added additively.

### 8.6 `session worktrees` calls `isolation` directly, with no operations wrapper

- **Recommendation: direct.** Precedent exists (`cli.sweepOrphanedWorktrees`
  **[V]**), and a wrapper would mediate no storage.
- **Trade-off:** ADR 0019's letter says frontends go through operations, and
  skipping it means an MCP surface for worktrees later has to add the wrapper
  then. ~40 lines either way.

### 8.7 The model-output contract in §5.4

- **Recommendation:** ctxloom appends a fixed JSON output contract; the skill
  owns style only.
- **Trade-off:** it caps what a "house extraction style" can be, which is
  precisely the property the doc calls the strongest argument for the supply
  chain **[V]**. The alternative — letting the skill define its own shape —
  needs a declared schema in the skill's frontmatter and a schema-driven parser,
  which is a much larger change and a new trust surface (a skill that declares
  its own parser). **This is the decision I am least confident in.**

### 8.8 Exit `0` vs `2` for an unfiltered `--reap` sweep that spared something

- **Recommendation: `0`** (§3.1.4).
- **Trade-off:** a script cannot tell "swept clean" from "swept, and there is
  orphaned WIP a human must look at" by exit code alone; it must read
  `--format json`. The alternative (`2` whenever anything was spared) makes a
  routine sweep exit `2` on any machine with one dirty worktree, which trains
  people to ignore the code.

---

## 9. Where this design is weakest

Ordered by how hard I would hit it as the reviewer.

1. **§5.4, the output contract, is the soft centre of the whole design.** It is
   invented — the spec says nothing about it, and the fixture's SKILL.md
   specifies no format **[V]**. Everything downstream (fragment names, the
   zero-fragment refusal, the parse-failure path) rests on it. If the human
   rejects the "ctxloom owns the shape" answer, §5.7's signatures survive but
   §5.4 and the parser are re-designed. **I would attack this first and I would
   be right to.**

2. **The lessons leg is unverifiable in the acceptance harness as designed.**
   Rows 7/8/9 need a real LLM to produce fragments. The J22 fixture configures
   `claude-code` **[V]** but the suite is hermetic. Either the extraction seam
   gets a deterministic test double wired through the CLI (an env-var-selected
   `LessonExtractor`, which is new surface area nobody asked for), or rows 7 and
   8 stay red on a machine with no engine. **I did not solve this**, and it is
   the most likely reason implementers get stuck. See §11.

3. **Zero-fragment ⇒ exit `2` is arguable.** A reviewer can reasonably read
   "extraction produced nothing" as `1`. I argued `2` from §7's own wording, but
   I am ~70% confident, not 95%.

4. **The purge allowlist may be too narrow to be useful.** It destroys
   transcripts and the transcript store and nothing else. On a real harp
   directory that may free far less than a user expects from a verb called
   "purge", and the temptation will be to widen it — which is exactly how a
   design note gets eaten. I chose narrow-and-honest, but "purge freed 4 KB"
   will read as a broken feature to someone with a 2 GB sessions dir.

5. **`--reap`'s per-item confirmation is under-specified for the TTY case.**
   The doc says "destruction confirms per item, against evidence rendered in the
   same invocation" **[V]**. I specified the batch (`--yes`) path precisely and
   the interactive path loosely. A reviewer will notice the asymmetry.

6. **Splitting `reapOneWorktree` touches the one function this project has lost
   work to before.** The refactor is behaviour-preserving *by construction*
   (`teardownWorktree` and `unsafeToRemove` are untouched, and
   `ReapOrphanedWorktrees` keeps its signature), but "behaviour-preserving by
   construction" is a claim, not a measurement. It needs the existing
   `worktree_reap_test.go` suite green **plus** a new test that
   `ClassifyOrphanedWorktrees` followed by `ReapWorktrees` produces the same
   tallies as `ReapOrphanedWorktrees` on identical fixtures.

7. **§8.4's blast radius may be larger than I measured.** I verified that
   `NewBuiltinReader` has exactly one caller **[V]** and that `BundleLoader`
   omits it **[V]**. I did **not** enumerate every surface that would newly show
   builtin items, nor check whether any test asserts a listing count that would
   change.

---

## 10. What I could not establish

1. **Whether the acceptance harness can drive an LLM at all.** I read the J22
   fixture and the CLI, not the harness's engine strategy for other journeys
   (`live_engine_registry.go`, `mockengine`). Resolving it needs a read of how
   `distill_live.feature` and `llm.feature` obtain a model.
2. **Whether `--format json` on a *destructive* command is coherent.** Every new
   command here renders through `emit()`, but a per-item confirmation prompt in
   the middle of a JSON render is not obviously well-defined. `reviewWantsListing`
   solves the analogous problem for `ctxloom review` by refusing the interactive
   walk under a machine format **[V]**; purge/reap probably want the same rule,
   but I did not design it.
3. **Whether any existing test pins the exact `session list` JSON shape**, which
   §8.1 would extend with `purged_at`.
4. **What "the routine" should actually say.** §7 designs the *wiring*; the
   prose of `cleanup.md` is content authoring, not surface design, and should
   follow `check-triggers.md`'s worked structure **[V]**.
5. **Whether ADR 0002 (`skip-ctxloom-gc`, Deferred **[V]**) needs superseding.**
   I read its title and status, not its full argument.

---

## 11. Deferrals — stated explicitly

Deferred deliberately; none of it is required by the fifteen rows.

1. **A deterministic test double for `LessonExtractor`.** Named as risk #2, not
   designed. This is the largest deferral and the one most likely to block.
2. **Fixing `cli.loadDistillPrompt` itself.** The verified silent-degrade in the
   *bundle* distiller **[V]**. Row 10 does not require it (§5.1), and fixing it
   changes `bundle distill`/`fragment distill` behaviour — a separate change with
   its own blast radius. **It should be filed, not forgotten.**
3. **`ctxloom review <bundle>` printed by `skill_cmd.go:383` against a
   `NoArgs` command **[V]**.** A real, small, pre-existing bug found in passing.
   Not in scope; should be filed.
4. **Multi-harp / policy-driven purge** (`--older-than`, `--all`, glob harps).
   No row asks for it, and retention policy is a much larger decision.
5. **A reaping verb for foreign worktrees.** Its absence is the point of row 6.
6. **Widening the container mount to cover the harp top level.** The doc
   recommends extending the plan-stamping convention instead **[V]**; check 3
   only reports.
7. **Wiring `session rename` / `session delete` to `emit()`** to clear their
   format debt **[V]**. Adjacent, not required.
8. **`--degraded` on anything other than `session distill`.** The feature file
   names it only there **[V]**.
9. **The `purged` column in `session list`'s text output.** Mentioned in §4.5 as
   in-scope for the same change; not specified here.
10. **Any change to `ReapOrphanedWorktrees`'s startup-sweep reporting.** It stays
    silent about spared/skipped **[V]**. Making the sweep chatty is a separate UX
    call.
