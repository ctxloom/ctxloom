# `internal/core/sessions` — the harp-keyed session store

**What it is.** The record of every ctxloom session, keyed by its harp name. There is no
central index: each session is a directory under the ctxloom home's sessions root, and that
directory's sidecar (`paths.SessionSidecarFileName`) is the session's identity record. The
package also owns the storage port (`Store`) over those records, the path composition for a
session's members (`Layout`), the reaper that reclaims them (`Reap`), and the environment codecs
a launched engine is handed.

**The contract it owns.** *A harp is minted before launch and is the stable identity everything
else keys on.* `ctxloom run` mints one pre-launch (`AssignHarp`) and records the session's
output dir (`RecordOutputDir`); the engine's SessionStart hook binds the backend session ID and
transcript (`BindSession`); readers resolve through `Find`, `FindBySessionID`,
`ListForProject` and `ListAll`.

---

## 1. Two roots

A session has two homes, and the split is the point of the layout.

- **The machine session dir** — `Layout.Dir(harp)` under the ctxloom home. Everything ctxloom
  and the engine need to run and resume the session: the sidecar, the spool, the transcripts,
  the engine's native history, the disposable engine home, worktrees and scratch. What lives
  there, and each member's lifetime, is the `paths.HarpMembers` table; `Layout.Member` is the
  one join that places a row under a harp. Do not restate the members elsewhere — read the
  table.
- **The output dir** — the session's human root, where its readable outputs go (essence, next
  step, plans, segment essences). Recorded ABSOLUTE in the sidecar at mint (`Entry.OutputDir`,
  written by `RecordOutputDir`), because its default derives from per-platform, per-user state
  and a configurable base: re-deriving it later could name a different folder than the one the
  session's files are in.

The project tree holds no session state.

### Resolving the output dir

- `OutputDir(harp)` reads the recorded path from the sidecar under the resolved home. No sidecar
  is `ErrNotFound`; a sidecar recording none is `ErrNoOutputDir`.
- `OutputDirIn(harp, getenv)` prefers `EnvOutputDir` — a containerized run serves exactly one
  session and mounts its output dir there — and otherwise falls back to `OutputDir`.
- `OutputDirOf(dir)` reads the same field from a session dir already in hand.
- `Distilled(dir)` asks the DISK whether that output dir holds an essence. It is what every
  destroyer of transcripts consults first: an undistilled session's transcript is its only record.

### Transcripts

- The engine's native history lands under the harp's native member, reached through the
  disposable engine home's link to it (`engine.HomeSpec.TranscriptStoreRel`).
- `BindSession` records the transcript path with symlinks resolved (`boundTranscriptPath`), so
  the binding stays true after the disposable home is deleted on close.
- `LocateTranscript(harp)` finds the transcript BY LOCATION — the newest transcript-shaped file
  under the native member, skipping in-harness subagent interiors. A containerized structured
  child never runs the SessionStart hook, so for it location IS the binding.

## 2. The store

`Store` is the port; the operations layer depends on it and nothing narrower. Its surface is
exactly what operations invokes; store-only helpers (`SetSourceEntries`) stay on the concrete
type.

- **`*Manager`** — the filesystem adapter over the sessions root. `readSidecar` / `writeSidecar`
  are its only persistence; every mutator goes through `update`, which takes the in-process
  mutex and then the harp's own cooperative flock (`lock`, at `paths.HarpSidecarLockPath`), reads,
  applies the mutation, and rewrites only when something changed. Writes are atomic and durable
  (`safefs.WriteFile` with `safefs.Durable`): the sidecar is the only record of the session's
  rotation lineage.
- **`*MemStore`** — an in-memory record set for tests and for demonstrating storage-agnosticism
  (ADR 0026). It is filesystem-free for the RECORDS only; its reads run the same enrichment as
  `Manager` and stat real HOME-rooted files (see its doc comment).

Listing (`enumerate`) walks the sessions root, admits only directories `IsSessionDir` accepts,
and skips — with a warning — a sidecar that does not parse, so one corrupt session cannot hide
the rest. `ListForProject` / `ListAll` order by `ActivityTime`, last WORKED, not created.

### `Entry`

The sidecar's schema. Persisted fields are those with a yaml tag; the rest are derived on read
by `enrich` and must never be written back:

- `HarpName` is the directory's name — never persisted, so a rename cannot desynchronise it.
- `Summary` / `Detail` come from the essence in the output dir (`fillFromEssence`); the essence
  is the record and a second copy would be a second thing to disagree.
- Transcript locations are filled by `fillTranscriptByLocation` and `fillCanonicalTranscript`.
- `SourceEntries` is the staleness fingerprint: the transcript's ENTRY COUNT at last
  distillation, not its byte size — see the field's doc comment for why that distinction is
  load-bearing.
- `Rotations` preserves every binding a `/clear` displaced (`Rotation`), which is what lets the
  canonical transcript be rebuilt across clears and `FindBySessionID` resolve a pre-clear ID.

## 3. Identity and environment

`Identity` is a session as a launched process carries it (harp, depth, run, one-shot);
`Seed` / `MintStamp` / `Origin` describe how it was minted and are stamped with `StampMint`.
The env codecs in `env.go` are the only place the launch environment's names are spelled:
`EncodeReach` / `DecodeReach` carry the coordinator endpoint, credential and run id —
`DecodeReach` takes the credential from `EnvCoordCred`, or else from the file
`EnvCoordCredFile` names, which is how a CONTAINER runner receives it so it is in neither the
container's environment nor the `run` client's; `EncodeHookReach` / `DecodeHookReach` carry the
hook endpoint; `HookEnv` / `DecodeHookEnv` carry the harp and project an engine forwards to its
hook subprocesses.

## 4. Reaping

`Reap` / `ReapSession` reclaim session members under a `ReapPolicy`. The policy's scope is a
`paths.Lifetime`; `ReapPolicy.Members` selects the `paths.HarpMembers` rows that scope takes.

- **It never removes a session directory.** The identity row stays under every scope, so the
  session still lists and resolves afterwards.
- **Liveness comes from the lock (`Locks`), never the sidecar.** Only a provably dead owner
  passes, and the lock is held across the removal so a resume waits instead of racing it.
- **The keep marker exempts a session entirely**, checked before the lock is probed.
- **The persistent scope takes persistent members from a DISTILLED session only.** An
  undistilled one keeps them and is reaped as under the default scope.
- **No symlink is ever followed.**
- A zero `Cutoff` is refused (`ErrNoAgeBound`): the one function that deletes must not be
  callable with no age bound stated.

## 5. Invariants

1. **One sidecar per session, mutated only under that session's lock.** Two sessions never
   contend; a reader never takes a lock.
2. **First bind wins.** A different session ID with no transcript path never displaces a live
   binding, and an empty ID never blanks one; `BindSession` with both empty is a no-op that takes
   no lock.
3. **A displaced binding is appended to `Rotations`, never discarded.**
4. **`Find` returns `(nil, nil)` for an absent harp**, and for a name `harp.Validate` rejects —
   absence is not an error. Mutators refuse an absent harp with `ErrNotFound`.
5. **Derived `Entry` fields are never persisted** (`yaml:"-"`).
6. **The recorded output dir is the answer** even if the configured base has changed since.
