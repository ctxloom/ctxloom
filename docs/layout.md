# The `.ctxloom` Directory

The normative account of what ctxloom keeps in your project, what a teammate's
clone gets, and what you may delete.

## Why you want to read this

A run's engine credential is never written into your project tree, and no
credential file is seeded into an engine home. Every agent authenticates with
the token you mint with `claude setup-token`, set in the engine's environment
by value; only your own session under the top-level `auth: login` shares your
credential storage in place (`loginStore`). While a session runs,
two places under your home can still hold a copy, both outside every project
tree:

- a **container** cell's credential, as an owner-only file in the run's secret
  dir (`isolation.materializeSecrets`) — on tmpfs under `$XDG_RUNTIME_DIR` where
  the session has one, otherwise in the session's `scratch/` directory
  (`isolation.secretParent`) — removed when the run ends, and reaped by the
  next container launch after a crash (`newOwnedScratch`);
- under `engine_home: session`, for YOUR OWN `auth: login` session only, a
  Console-key login's `primaryApiKey`, which that instance's generated
  `.claude.json` copies from your host file by name (`claude.ambientConfigKeys`),
  removed with the instance (below). No agent's instance ever holds it.

The whole account is
[isolation.md](architecture/engines/isolation.md), "Where a credential may and
may not be". A checkout an earlier ctxloom ran in can still hold a credential
file in an in-tree instance under `.ctxloom/state/`; the ignore rules for that
tier stay, and an architectural gate (`TestArch_SeededCredentialsAreGitignored`)
asserts by name that `git` will not see such a file.

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

`paths.Layout()` is the machine-readable form of that table: every path
ctxloom's own writers produce, each classified once. `ctxloom doctor` walks it
and reports any local-tier path that is missing (`doctorCheckLocalTierState`).

`ctxloom clean` walks the same table to empty `cache/`, and names each path's
rebuild command as it goes. It reports without `--yes`, and it takes only the
cache: `state/` is the tier nothing rebuilds, and `lock.yaml` is rebuildable
but committed, so deleting it would dirty your tree rather than free anything.

### `content/` — authored, committed

`.ctxloom/content/bundles` holds the bundles this project authors
(`paths.LocalBundlesPath`). It is the one on-disk home for authored content, and
a dedicated bundle repo lays out the same tree, so a publishing repo and a
consuming project are identical in shape.

Alongside it, and committed for the same reason, sit the root files and
directories: `config.yaml`, `remotes.yaml`, `lock.yaml`, `profiles/`,
`allowed_signers` and `distrusted_signers`.

`lock.yaml` is the deliberate oddity: it is **derived** (`ctxloom remote lock`
regenerates it) and **committed anyway**, because a lockfile whose job is to pin
versions for the next clone is worthless if the clone does not get it.
Rebuildability and commitment are independent questions.

### `cache/` — derived, and every entry names its rebuild command

| Path | Rebuild with |
|---|---|
| `cache/bundles` | `ctxloom deps pull` |
| `cache/repos` | `ctxloom deps pull` |
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
| `state/locks/` | advisory lock sidecars guarding project files (`paths.LocksPath`, named by `paths.ProjectPathFor`) | nothing |

Two more local-only paths live at the `.ctxloom` root rather than under
`state/`, and are gitignored individually:

- `.ctxloom/project-id` — the key to this project's task log at
  `~/.ctxloom/tasks/<project-id>.jsonl` (ADR 0025;
  `tasks/paths.ProjectMarkerPath`). **Lose it and a fresh clone mints a new
  project id and starts an empty log**, while every task the team logged stays
  on disk under the old id, unreachable. *A move to `state/project-id`, with a
  read fallback to the root copy and a one-time migration, is decided but not
  yet implemented; the root path above is what the code resolves today.*

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
| `~/.ctxloom/sessions/` | every ctxloom session on this machine, across every project (`paths.HomeSessionsDir`): one directory per session holding its machine state, whose members are the rows of `paths.HarpMembers` | the session's raw history; nothing rebuilds it (its `home/`, `work/` and `scratch/` members are rebuilt, triaged or regenerated) |
| `~/.ctxloom/allowed_signers` | every signing key you personally trusted (`paths.HomeAllowedSignersPath`, `ctxloom signer trust`) | each key must be re-trusted by hand |
| `~/.ctxloom/distrusted_signers` | every embedded signing key you personally distrusted (`paths.HomeDistrustedSignersPath`, `ctxloom signer untrust`) | each suppression must be re-recorded by hand |
| `~/.ctxloom/cache/triggers/` | cached revive-trigger verdicts (`paths.TriggerCacheDir`) | nothing durable — the next trigger check recomputes them, just not for free |
| `~/.ctxloom/coord/` | coordinator state, one subdirectory per project (`paths.HomeCoordDir`) | a LIVE coordinator loses its lock and journal outright; a recent-but-exited one's history becomes unrecoverable |
| `~/.ctxloom/locks/` | cross-binary advisory lock sidecars for FOREIGN files (an engine's own settings.json/.mcp.json/config.toml) that more than one ctxloom-family binary — ctxloom, `ltk`, `taskloom` — may write (`paths.HomePathFor`) | harmless — a lock file carries no data and is recreated on next use; a write in flight when it disappears loses its mutual exclusion for that one operation |

Two more home-rooted paths exist and are deliberately **not** in the table
above: `~/.ctxloom/tasks/` is taskloom's own per-project task-log store
(`internal/shared/tasks/paths.HomeTasksDir`) — a sibling vocabulary that
shares the `.ctxloom` dot-dir without folding into `internal/core/paths`, the same
boundary [architecture/core/paths.md](architecture/core/paths.md) draws for
`IndexFileName` — and `~/.ctxloom/logs/` holds each binary's structured log
(`ctxloom.log` for ctxloom, `<binary>.log` for each companion), created only
once that binary has something to record: diagnostic output, not state whose
absence is ever worth a doctor warning.

## Engine homes: your real home, and the per-session instance

**Your real engine home (`~/.claude` for claude-code) is the durable truth,
and ctxloom never writes it.** That is the model's hardest invariant, and it is
pinned by a gate that hashes those trees before and after a real agent launch
and requires byte identity:
`TestArch_RealHostHomesAreByteIdenticalAfterAnInTreeAgentLaunch`. A path
assertion could only say where ctxloom *meant* to write.

An agent whose binding declares `engine_home: session` does not run against your
real home. It gets a throwaway **per-session instance** at
`~/.ctxloom/sessions/<harp>/home/<engine-leaf>` — the session's own `home`
member (`paths.HarpSessionEngineHomes`; each engine appends its own leaf, distinct by
construction so one instance root hosts every engine a session runs) — on
every isolation cell: a host cell tells the engine that path, a container cell
mounts it and tells the engine the mount target. No
binding, an undeclared `engine_home`, or an explicit `engine_home: host` all
mean the engine uses the home its runtime gives it **directly** — your real
home on the host, a fresh `$HOME` in a container — with no instance and no
copy-in (`agents.ParseHomeMode`).

Three classes of content live inside an instance:

1. **ctxloom-generated** — context, prompts, skills, managed config blocks.
   Regenerated at every launch.
2. **engine-generated** — the scaffolding an engine needs, written by that
   engine's own package (`engine.InstanceConfigWriter`): for claude, the
   `.claude.json` carrying its hardened keys and — only for a repository you
   trusted in your own claude — the workspace-trust answer for the run's
   working directory (see the trust model's "Engine workspace-trust prompts").
3. **ambient** — content whose origin is your real host home, **copied in one
   way** at instance time and never back (the engine's own allow-list — for
   claude, `ambientConfigKeys`, copied by `claudeInstanceConfig.applyAmbient`).

The ambient set is an **allow-list, never a deny-list**. Under a deny-list a
file the vendor adds tomorrow would be copied by default, and the default
direction of that mistake is a confidentiality leak: claude's `.claude.json`
carries your own `mcpServers` registrations. So only named keys and named files
cross — `claude.ambientConfigKeys` is the onboarding answers plus the account
half claude's own config seeding copies (`oauthAccount` for every run, and a
Console-key login's `primaryApiKey` for your own `login` session only), and
nothing else.
An engine whose credentials live in a global store no home variable relocates
declares its set **empty** rather than omitting it, so the absence is a
decision a reader can find.

**There is no sync-back, ever.** Two costs follow, and they are accepted
deliberately:

- an account or API key the engine rewrites in an instance's `.claude.json`
  never reaches your real home's;
- a trust or onboarding answer given inside an instance dies with the instance,
  and is asked again next session unless the engine's own answer already lives
  in your real home and rides the next copy-in.

**Instances are removed, and that is a security requirement, not hygiene** —
your own login session's can hold a copied credential (`primaryApiKey`, above). `EndSession` removes a session's instance at
graceful shutdown (`operations.removeSessionInstance`); an instance a crashed
session leaves behind is an ephemeral member of its session directory
(`paths.HarpMembers`) and goes with the session's own reaping.

Because an instance is rebuilt from scratch every session, no per-session path
gets a `paths.Layout()` row of its own: its absence is the normal case, not a
loss worth reporting (`TestArch_LayoutHasNoHarpKeyedRows`); the sessions store
row below covers the tree.

For the per-axis table (container / worktree / in-tree, per engine) and the
env-var mechanics, see
[architecture/engines/isolation.md](architecture/engines/isolation.md), "Engine
config homes".

## The gitignore contract

ctxloom does **not** append these rules to your project's root `.gitignore`. It
owns a nested one instead, and that file is meant to be **committed**:

```gitignore
# .ctxloom/.gitignore — generated; ctxloom rewrites it wholesale
/cache
/sessions
/project-id
/state
*.lock
```

The rules are `gitignore.PrivateStatePatterns` relativized to the directory they
sit in (`gitignore.NestedPatterns`), re-anchored with a leading `/` so stripping
the prefix cannot widen a rule — a pattern with no slash matches at *any* depth.

Two properties follow from where the file lives, and both are the point:

- **Tracking it is what makes it work.** The rules travel with the directory into
  every clone and, crucially, into every linked worktree — where a rule living
  only in the superproject's root `.gitignore` would not reach.
- **It is rewritten wholesale, never appended to.** The old root-append path
  emitted a fresh comment header above only the patterns still *missing*, so each
  time the list grew another header landed above the new entries. In ctxloom's own
  repo that header accumulated five times and ended up captioning an engine's
  credential file as "ctxloom private working state".
  A generated file with no user-authored lines cannot drift that way.

ctxloom still writes to your root `.gitignore` for two narrow reasons: engine
surfaces it generates *outside* `.ctxloom/` (under their own honest header,
`gitignore.TransientArtifactComment`), and retiring a blanket `.ctxloom/` rule —
git does not descend into an ignored directory, so a blanket rule would make the
nested file unreadable and every rule in it silently dead.

If an older ctxloom already appended these rules to your root file, they are now
redundant but harmless. `ctxloom manage gitignore install` **names** them so you
can delete them deliberately; it will not edit a file it does not own.

**Everything else under `.ctxloom/` is committed by omission** — `config.yaml`,
`remotes.yaml`, `lock.yaml`, `content/`, `profiles/`,
`allowed_signers`, `distrusted_signers`. That is intentional:
each is content, configuration, or trust state your project depends on.

Two notes on that list, because both look like mistakes and are not:

- `.ctxloom/*.lock` names a path current ctxloom does **not** write. Lock
  sidecars now live under `state/locks/`, covered by `.ctxloom/state/`; the
  pattern stays for projects an earlier version left a `.ctxloom/config.yaml.lock`
  at the root of.
- `.ctxloom/state/` is a blanket rule and covers an in-tree per-session
  instance a checkout from an earlier ctxloom may still hold, credential file
  included. The credential arch gate asserts such a path by name, because a
  blanket rule is one careless edit from narrowed.

## Per-agent worktree scratch stays in your home directory

The worktree isolation axis gives each agent a checkout and a toolchain
scratch dir, and those live at `~/.ctxloom/sessions/<harp>/work/` and
`~/.ctxloom/sessions/<harp>/scratch/` (`paths.HarpWorkDir`,
`paths.HarpScratchDir`), never in the project tree: a worktree
run's checkout is a *different directory* from the project, and consumers —
the orphan-worktree reaper, session purge — walk the home-rooted shape
directly, with no project in hand. The engine's config home is **not** part of
that scratch: it is the same per-session instance described above, decided
off the binding for every cell (`operations.ResolveInTreeAgentHome`), so a
worktree run and a live-tree run with the same binding share one answer.

When a run carries no usable harp, the per-agent scratch falls back to the OS
temp directory and says so — never to a shared project path.

## Settings that exist only at launch

An engine that reads hooks, MCP servers, prompts or skills **only** from its
home has no durable project copy of them for ctxloom to write. A backend in
that position declares it on its registry descriptor
(`launchOnlySettingsReason`) — a **declared absence**, not an oversight — so
that tools can report it instead of silently writing nowhere:
`ctxloom profile materialize --backend <name>` lists those surfaces as
not-carried with the reason (`backends.LaunchOnlySurfaces`) and still writes
whatever cwd-keyed surface the engine does have.

claude needs none of this: its static surfaces are cwd-keyed
(`CLAUDE.md`, `.claude/`), so it has durable project paths to write. The
mechanism stays exercised by the `mock-launch` backend, which declares a
launch-only reason for exactly that purpose.

## See also

- [architecture/core/paths.md](architecture/core/paths.md) — the
  `internal/core/paths` package: every constant and join, and the invariants over
  them.
- [architecture/engines/isolation.md](architecture/engines/isolation.md) — the
  isolation axes, `engine_home`, and the per-engine home variables.
- [trust-model.md](trust-model.md) — what `allowed_signers` means.
