# The `.ctxloom` Directory

The normative account of what ctxloom keeps in your project, what a teammate's
clone gets, and what you may delete.

## Why you want to read this

While an agent session is running, a **copy of your engine credential** can be
sitting inside your project tree, at `.ctxloom/state/<harp>/home/…`. It is
gitignored, it is deleted when the session ends, and an architectural gate
(`TestArch_SeededCredentialsAreGitignored`) asserts by name that `git` will not
see it. But that safety is a property of a few ignore lines, and one careless
edit to `.gitignore` — or one `git add -f` — turns it into a commit you cannot
take back.

That is the sharp end. The everyday end is simpler and comes up more often:

- **"Can I delete this?"** — yes for two of the three trees, and the cost is a
  command you can run, not work you have to redo.
- **"Why doesn't my teammate have it?"** — because it is local by design, and
  the page below says which parts and why.
- **"What did I just commit?"** — everything under `.ctxloom/` that no ignore
  pattern matches. The list is short and it is here.

## The three trees

Everything ctxloom writes into a project lands in one of three trees under
`.ctxloom/`. They are told apart by **what a fresh clone gets** — not by whether
they are gitignored, which two of the three are (`paths.Tier`).

| Tree | In git? | Delete it and you lose | Get it back by |
|---|---|---|---|
| `content/` | **committed** | your authored work | nothing — restore it from git, or re-author it |
| `cache/` | gitignored | nothing durable | re-running one named command (below) |
| `state/` | gitignored | answers and reviews you gave *on this machine* | re-answering / re-reviewing |
| `state/<harp>/` | gitignored | **nothing** | it is rebuilt at the start of the next session |

`paths.Layout()` is the machine-readable form of that table: every path
ctxloom's own writers produce, each classified once. `ctxloom doctor` walks it
and reports any local-tier path that is missing (`doctorCheckLocalTierState`).

### `content/` — authored, committed

`.ctxloom/content/bundles` holds the bundles this project authors
(`paths.LocalBundlesPath`). It is the one on-disk home for authored content, and
a dedicated bundle repo lays out the same tree, so a publishing repo and a
consuming project are identical in shape.

Alongside it, and committed for the same reason, sit the root files and
directories: `config.yaml`, `remotes.yaml`, `lock.yaml`, `profiles/`, `agents/`,
`allowed_signers`, `distrusted_signers` and `approvals/`.

`lock.yaml` is the deliberate oddity: it is **derived** (`ctxloom remote lock`
regenerates it) and **committed anyway**, because a lockfile whose job is to pin
versions for the next clone is worthless if the clone does not get it.
Rebuildability and commitment are independent questions.

### `cache/` — derived, and every entry names its rebuild command

| Path | Rebuild with |
|---|---|
| `cache/bundles` | `ctxloom deps pull` |
| `cache/repos` | `ctxloom deps pull` |
| `cache/refused_advances.yaml` | `ctxloom deps upgrade` |
| `cache/context` | `ctxloom manage hooks install` (the next `ctxloom run` also rewrites it) |

`cache/bundles` holds *pulled copies* of remote bundles and is **never** a
bundle search directory — authored YAML found there is a fatal migration
finding, not a bundle (`paths.CacheBundlesPath`). `cache/context` holds
assembled context files keyed by content hash (`agent.WriteContextFile`); it
stays in `cache/` on purpose, because a content-addressed file that two sessions
legitimately share is what a cache *is*.

Deleting `cache/` wholesale is safe and supported.

### `state/` — local to this checkout, and nothing rebuilds it

`state/` is the third tier (`paths.StateDir`): gitignored like `cache/`, but
nothing reconstructs it. A file here is a fact about *this checkout on this
machine* that a clone must never arrive carrying — somebody else's answer is not
your answer.

Fixed residents at the root of `state/`:

| Path | What it is | Losing it costs |
|---|---|---|
| `state/dirty_tree_commit_ack.yaml` | the record that a human authorized ctxloom to auto-commit a dirty tree here (`paths.DirtyTreeCommitAckPath`) | you are asked again |
| `state/trust/objects` | content-addressed copies of the bytes a human approved at review (`paths.TrustObjectsPath`) | update review degrades from a diff to a full-content dump; committed approval signatures still verify |
| `state/locks/` | advisory lock sidecars guarding project files (`paths.LocksPath`, named by `filelock.ProjectPathFor`) | nothing |

Two more local-only paths live at the `.ctxloom` root rather than under
`state/`, and are gitignored individually:

- `.ctxloom/project-id` — the key to this project's task log at
  `~/.ctxloom/tasks/<project-id>.jsonl` (ADR 0025;
  `tasks/paths.ProjectMarkerPath`). **Lose it and a fresh clone mints a new
  project id and starts an empty log**, while every task the team logged stays
  on disk under the old id, unreachable. *A move to `state/project-id`, with a
  read fallback to the root copy and a one-time migration, is decided but not
  yet implemented; the root path above is what the code resolves today.*
- `.ctxloom/sessions/` — this machine's distilled session records
  (`paths.ProjectSessionsDir`).

## The home tree: `~/.ctxloom`

Everything above lives under one project's `.ctxloom/`. A second, smaller set
of stores lives under **your home directory** instead, because each is a fact
about *you* or about *this machine*, not about any one project: which signing
keys you trust, which companion binaries you let ctxloom execute, and the
session/coordinator/trigger state that spans every project you use ctxloom in.

`paths.Layout()` carries a row for each of these (a home-rooted row, same
mechanism as the project rows above), and `ctxloom doctor` reports what it
finds there — but it will **never** warn that one is missing. A fresh
install, or a machine that has simply never exercised the feature behind one
of these stores, has none of it yet, and that absence is not a loss; only
what actually exists is worth telling you about.

| Path | What it is | Losing it costs |
|---|---|---|
| `~/.ctxloom/sessions/` | this machine's distilled record of every ctxloom session, across every project (`paths.HomeSessionsDir`) | the session history; nothing rebuilds it |
| `~/.ctxloom/approvals/` | your personal countersignature store (`paths.HomeApprovalsPath`) | update review degrades from a diff to a full-content dump for approvals only this store held; committed approval signatures still verify |
| `~/.ctxloom/allowed_signers` | every signing key you personally trusted (`paths.HomeAllowedSignersPath`, `ctxloom signer trust`) | each key must be re-trusted by hand |
| `~/.ctxloom/distrusted_signers` | every embedded signing key you personally distrusted (`paths.HomeDistrustedSignersPath`, `ctxloom signer untrust`) | each suppression must be re-recorded by hand |
| `~/.ctxloom/cache/triggers/` | cached revive-trigger verdicts (`paths.TriggerCacheDir`) | nothing durable — the next trigger check recomputes them, just not for free |
| `~/.ctxloom/coord/` | coordinator state, one subdirectory per project (`paths.HomeCoordDir`) | a LIVE coordinator loses its lock and journal outright; a recent-but-exited one's history becomes unrecoverable |
| `~/.ctxloom/companion_consent.yaml` | which companion binaries you agreed ctxloom may execute (`paths.HomeCompanionConsentPath`) | you are asked again |

Two more home-rooted paths exist and are deliberately **not** in the table
above: `~/.ctxloom/tasks/` is taskloom's own per-project task-log store
(`internal/shared/tasks/paths.HomeTasksDir`) — a sibling vocabulary that
shares the `.ctxloom` dot-dir without folding into `internal/paths`, the same
boundary [architecture/core/paths.md](architecture/core/paths.md) draws for
`IndexFileName` — and `~/.ctxloom/logs/ctxloom.log` is the structured log
every ctxloom process writes at startup: diagnostic output, not state whose
absence is ever worth a doctor warning.

## Engine homes: your real home, and the per-session instance

**Your real `~/.claude`, `~/.codex` and `~/.kiro` are the durable truth, and
ctxloom never writes them.** That is the model's hardest invariant, and it is
pinned by a gate that hashes those trees before and after a real agent launch
and requires byte identity:
`TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch`. A path
assertion could only say where ctxloom *meant* to write.

An agent whose binding declares `config_home: project` does not run against your
real home. It gets a throwaway **per-session instance** at
`.ctxloom/state/<harp>/home/<engine-leaf>` (`paths.SessionHomePath`; the leaves
are `.codex`, `claude`, `kiro`, pairwise distinct so one instance root hosts
every engine a session runs). No binding, an undeclared `config_home`, or an
explicit `config_home: host` all mean the engine uses its **real home directly**
— no instance, no copy-in (`operations.ResolveConfigHome`).

Three classes of content live inside an instance:

1. **ctxloom-generated** — context, prompts, skills, managed config blocks.
   Regenerated at every launch.
2. **engine-generated** — the scaffolding an engine needs, written by that
   engine's own package (`agent.InstanceConfigWriter`): codex's `config.toml`
   tables and its `[projects."<abs workdir>"]` trust pre-seed, claude's
   `.claude.json` onboarding keys.
3. **ambient** — content whose origin is your real host home, **copied in one
   way** at instance time and never back (`isolation.CopyAmbient`, over the
   per-engine allow-list `isolation.AmbientSet`).

The ambient set is an **allow-list, never a deny-list**. Under a deny-list a
file the vendor adds tomorrow would be copied by default, and the default
direction of that mistake is a confidentiality leak: claude's `.claude.json`
carries your own `mcpServers` registrations, codex's `config.toml` carries
yours. So only named keys and named files cross — `claude.ambientConfigKeys` is
the onboarding answers and nothing else, and codex's copy elides
`[mcp_servers]` and `[hooks]` (`codex.elidedHostSections`). kiro's set is
**declared empty**, not omitted: its credentials live in a global store no home
variable relocates.

**There is no sync-back, ever.** Two costs follow, and they are accepted
deliberately:

- a credential the engine refreshes *inside* an instance never reaches your real
  home;
- a trust or onboarding answer given inside an instance dies with the instance,
  and is asked again next session unless the engine's own answer already lives
  in your real home and rides the next copy-in.

**Instances are removed, and that is a security requirement, not hygiene** —
each one holds a copied credential. `EndSession` removes a session's instance at
graceful shutdown (`operations.removeSessionInstance`), and a startup sweep
collects the ungraceful ones (`operations.ReapOrphanedSessionHomes`), skipping
any harp the session index still reports as live.

Because an instance is rebuilt from scratch every session, `state/<harp>/` gets
no `paths.Layout()` row at all: its absence is the normal case, not a loss worth
reporting (`TestArch_LayoutHasNoHarpKeyedRows`).

For the per-axis table (container / worktree / in-tree, per engine) and the
env-var mechanics, see
[architecture/engines/isolation.md](architecture/engines/isolation.md), "Engine
config homes".

## The gitignore contract

These are the only patterns ctxloom writes for the `.ctxloom` tree
(`gitignore.PrivateStatePatterns`; `gitignore.Ensure` appends them once and
never removes a user's own lines):

```gitignore
# ctxloom private working state (rebuildable/local — cache, sessions, project id, local state)
.ctxloom/cache/
.ctxloom/sessions/
.ctxloom/project-id
.ctxloom/state/
.ctxloom/*.lock
```

**Everything else under `.ctxloom/` is committed by omission** — `config.yaml`,
`remotes.yaml`, `lock.yaml`, `content/`, `profiles/`, `agents/`,
`allowed_signers`, `distrusted_signers`, `approvals/`. That is intentional:
each is content, configuration, or trust state your project depends on.

Two notes on that list, because both look like mistakes and are not:

- `.ctxloom/*.lock` names a path current ctxloom does **not** write. Lock
  sidecars now live under `state/locks/`, covered by `.ctxloom/state/`; the
  pattern stays for projects an earlier version left a `.ctxloom/config.yaml.lock`
  at the root of.
- `.ctxloom/state/` is a blanket rule and covers every per-session instance,
  credential included. The credential arch gate still asserts specific instance
  paths by name, because a blanket rule is one careless edit from narrowed.

## Per-agent worktree homes stay in your home directory

The worktree isolation axis also gives each agent a config home, and it is the
same *shape* as an in-tree instance — ephemeral, one-way copied from your real
home, torn down at cleanup. It shares the same mechanism (`isolation.CopyAmbient`
serves both axes). It does **not** share the location: per-agent worktree homes
live at `~/.ctxloom/sessions/<harp>/ephemeral/ctxloom-cfg-<agent>`
(`isolation.Worktree.provisionConfigHome`, rooted at `paths.HarpEphemeralDir`),
never in the project tree.

Three reasons, and all three are about the axis, not about tidiness:

1. The worktree home is per-**agent**, not per-session — a fan-out has several
   concurrent members, and `state/<harp>/` is keyed by session.
2. A worktree run's checkout is a *different directory* from the project, so
   "the project's `state/`" is ambiguous — and importing engine state back into
   the tree is precisely what the worktree axis exists to avoid.
3. Consumers walk the home-rooted shape directly, with no project in hand:
   the orphan-worktree reaper, session purge, and kiro's vendor transcript
   reader all enumerate `~/.ctxloom/sessions/*/ephemeral/`.

When a run carries no usable harp, the per-agent scratch falls back to the OS
temp directory and says so — never to a shared project path.

## codex: settings that exist only at launch

codex reads hooks, MCP servers, prompts and skills **only** from `$CODEX_HOME`,
and ctxloom writes no durable project copy of them. This is a **declared
absence** (`codex.LaunchOnlySettingsReason`), not an oversight, and it is
declared so that tools can report it instead of silently writing nowhere.

What follows:

- `ctxloom profile materialize --backend codex` does not write codex's
  hooks/MCP/prompts/skills and **says so**, listing them as not-carried with the
  reason (`backends.LaunchOnlySurfaces`). It still writes codex's genuinely
  cwd-keyed surface, the project-root `AGENTS.md`.
- `ctxloom doctor` answers the question that absence creates — "so where *are*
  my hooks?" — by reporting **both** homes: your real `$CODEX_HOME` (or
  `~/.codex`), which is what any agent with no binding or `config_home: host`
  actually uses, and the most recent per-session instance if one is on disk,
  labelled with its harp, its age, and a note that it is not live configuration
  (`cli.doctorCheckCodexHome`).

claude and kiro need none of this: their static surfaces are cwd-keyed
(`CLAUDE.md`, `.claude/`, `.kiro/`), so they have durable project paths to
write.

## See also

- [architecture/core/paths.md](architecture/core/paths.md) — the
  `internal/paths` package: every constant and join, and the invariants over
  them.
- [architecture/engines/isolation.md](architecture/engines/isolation.md) — the
  isolation axes, `config_home`, and the per-engine home variables.
- [trust-model.md](trust-model.md) — what `approvals/`, `allowed_signers` and
  the review snapshots under `state/trust/objects` mean.
