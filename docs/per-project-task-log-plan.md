# Implementation plan — per-project task log (ADR 0025)

Living implementation doc for [ADR 0025](adr/0025-per-project-task-log.md). The ADR holds the *why* and the decisions; this holds the *how* and the sequencing. Keep it in sync as work lands.

> **2026-06: extracted.** The task subsystem described here (store, log,
> project identity, CLI, MCP tools) now lives in the standalone
> [ctxloom/taskloom](https://github.com/ctxloom/taskloom) repo as the `taskloom`
> binary; ctxloom keeps only the `run --seed-task` integration and the
> CTXLOOM_PROJECT_ID export. The legacy markdown store and the pre-ADR-0025
> migration were dropped in the extraction. File paths below refer to the
> pre-extraction ctxloom tree.

## What we're building

Replace the three current task-storage paths (legacy `<projectDir>/.ctxloom/tasks.md`, per-session `~/.ctxloom/sessions/<harp>/tasks.md`, and the move-on-resume migration) with:

- a **stable project identity** — a gitignored in-tree `.ctxloom/project-id` marker plus a home registry (`~/.ctxloom/projects/`) mapping `project-id ↔ path`, resolved path-first / marker-fallback / self-heal, with a move-vs-copy probe that re-points only on a proven move and forks otherwise;
- a **single append-only JSONL task log per project** at `~/.ctxloom/tasks/<project-id>.jsonl`, folded to current state, with task identity = a project-log-scoped harp and a direct origin-session reference on every event.

Deferred (ADR revive triggers): the movement layer — `ctxloom project repoint`, `ctxloom tasks dump/load`, and copy-fork log seeding.

## Status

Phases 0–3 are implemented and green (`just test-verbose`). The core store is live: identity resolution at startup, the append-only log backend behind the existing `*Store` surface, project-id keying with origin-session provenance, and one-time migration of legacy markdown. The `tasks run` picker and `--seed-task` now read/write the project log; the legacy `OpenSession`/`MoveTask`/`CollectForSessions`/`moveStore` machinery is retired.

Carried forward, not yet done:
- **`internal/operations/tasks.go` (ADR 0019)** — the MCP/CLI handlers still call `internal/tasks` directly through `openSessionTaskStore`; the operations-layer extraction is deferred (optional cleanup, not a blocker).
- **Migration leaves legacy markdown on disk** — import is non-destructive; the old `tasks.md` files are orphaned but not deleted. A later sweep can remove them once the log is trusted.
- **Phase 4 movement layer** — deferred per the ADR (see revive triggers).

## Current seams (verified)

| Concern | Location | Notes |
|---|---|---|
| Store surface | `internal/tasks/store.go` | `Open`, `OpenPath`, `List(statuses, term)`, `Add(text, status)`, `SetStatus(harpID, status)`, `Remove(harpID)`, `Snapshot`, `Summarize`, `Path`; disk seam = `parseFile`/`renderFile`/`write` |
| Session resolution | `internal/tasks/session.go` | `SessionConfig{Harp, ResumedFrom, RestoreTasks, SessionsRoot, ProjectDir}`, `OpenSession`, `migrationSource`/`moveStore`, `CollectForSessions` |
| Single integration point | `cmd/tasks_cmd.go:374` | `openSessionTaskStore()` — the one function both MCP and CLI go through |
| MCP handlers | `cmd/mcp_tools_tasks.go:84` | `handleTaskList/Add/SetStatus` → `openSessionTaskStore()` (no operations layer; ADR 0019 gap) |
| CLI commands | `cmd/tasks_cmd.go:40` | `tasksList/Add/Status/Summary` → `openSessionTaskStore()` |
| Identity assignment | `cmd/run.go:478` | `sessMgr.AssignHarp(workDir, llmName)`, then `runEnv["CTXLOOM_SESSION_HARP"]` at ~482 |
| Harp allocator | `internal/sessions/index.go:427` | `generateUniqueHarp(used)` — mint-with-check, file-locked; reuse pattern for project-id and task harps |
| Paths | `internal/paths/paths.go:62` | `HomeSessionsDir`, `SessionIndexPath`, `HarpDir`; constants `AppDirName`, `SessionsDir`, `IndexFileName` |
| Gitignore | `cmd/init.go:67` | `ensureGitignoreEntry` appends `.ctxloom/ephemeral/`; idempotent check+append |
| Prior art | — | No existing project-id/registry/marker. `internal/harpmarker` is the session-transcript self-ID, unrelated |

## Data model

**Marker** — `<projectDir>/.ctxloom/project-id`, plain text, one harp:

```
swift-amber-falcon
```

**Registry** — `~/.ctxloom/projects/index.yaml`, mirroring `sessions.Manager` (filelock + `loadLocked`):

```yaml
projects:
  - project_id: swift-amber-falcon
    path: /home/ben/work/foo
    created_at: 2026-06-02T...
    last_seen_at: 2026-06-02T...
```

**Task log** — `~/.ctxloom/tasks/<project-id>.jsonl`, one event per line. Current state is the fold; last-write-wins per task harp; provenance is the immutable `add` event's `session`.

```jsonl
{"op":"add","task":"quiet-silver-meadow","text":"write storage layer","status":"To Do","session":"swift-amber-falcon","ts":"..."}
{"op":"status","task":"quiet-silver-meadow","status":"In Progress","session":"misty-golden-river","ts":"..."}
{"op":"edit","task":"quiet-silver-meadow","text":"write the log storage layer","ts":"..."}
{"op":"rekey","from":"quiet-silver-meadow","to":"brave-coral-dawn","ts":"..."}
```

`Task` keeps `HarpID`, `Text`, `Status`, `Checked`, `TextHash` and gains `OriginSession string` (the `add` event's `session`).

## Phases

Each phase is independently buildable and leaves the tree green. Tests run offline: `just test-verbose` (no devcontainer/network/-race; skips treesitter-tagged code).

### Phase 0 — Project identity layer (`internal/projectid`)

New package, no task behavior change yet. Build and test identity resolution in isolation, then wire a fault-tolerant resolution call at startup that only records the result.

- [ ] `paths.go`: add `ProjectsRegistryPath()` → `~/.ctxloom/projects/index.yaml`, `TasksLogDir()`/`TasksLogPath(projectID)` → `~/.ctxloom/tasks/<id>.jsonl`, and `ProjectMarkerPath(projectDir)` → `<projectDir>/.ctxloom/project-id`. Add constants.
- [ ] `internal/projectid/registry.go`: `Manager` over the registry file (filelock + `loadLocked`, copying `sessions.Manager`). `ResolveByPath(path)`, `ResolveByID(id)`, `Mint(path)` (harp via `harp.GenerateName`, checked against the registry — reuse the `generateUniqueHarp` idiom), `Repoint(id, newPath)`.
- [ ] `internal/projectid/marker.go`: `ReadMarker(projectDir)`, `WriteMarker(projectDir, id)` (`MkdirAll` the `.ctxloom/`, write `id\n`).
- [ ] `internal/projectid/resolve.go`: `Resolve(projectDir) (Resolution, error)` implementing path-first → marker-fallback → probe:
  - registry hit by path → `Normal`;
  - miss → read marker → `ResolveByID`:
    - id maps to current path → `Normal` (registry/path skew, heal silently);
    - id maps to a different path → **probe that path**: gone or marker missing/changed → `Moved` (re-point); else (live same-id copy, OR probe errored/unreachable) → `Forked` (mint new id, rewrite marker, new entry, warn);
  - no marker → `NewProject` (mint, write marker, entry).
  - `Resolution{ProjectID, Action, Warning}`; `Action ∈ {Normal, Moved, Forked, NewProject}`.
- [ ] `cmd/run.go`: after `AssignHarp` succeeds (~line 478) and before launch, call `projectid.Resolve(workDir)`; on error/`Forked`/`Moved` print `ctxloom: warning: ...` and continue; set `runEnv["CTXLOOM_PROJECT_ID"]`. Never block (CLAUDE.md).
- [ ] `cmd/init.go`: extend `ensureGitignoreEntry` to also ensure `.ctxloom/project-id`.
- [ ] Tests (`internal/projectid/resolve_test.go`): table over Normal / Moved (old path removed) / Forked-copy (old path live) / Forked-inconclusive (old path unreadable) / NewProject. Marker round-trip; mint uniqueness against a seeded registry.

**Done when:** `projectid.Resolve` returns the right action for every row, startup logs a project-id, and nothing consumes it yet.

### Phase 1 — Append-only log store (`internal/tasks` log backend)

Build the event log behind the existing `*Store` method surface so call sites need no change in this phase.

- [ ] `internal/tasks/event.go`: `Event{Op, Task, Text, Status, Session, From, To, Ts}`; `marshal`/`unmarshal` (one JSON object per line).
- [ ] `internal/tasks/log.go`: `OpenLog(path, sessionHarp string) (*Store, error)`. Internals:
  - `fold()` — read the log, skip malformed lines with a warning (never error out), apply events last-write-wins per task harp, drop nothing (Done/Archived retained), return `[]Task` + the issued-harp set.
  - `append(ev Event)` — `O_APPEND` single-line write under a filelock on the log.
  - `mintTaskHarp()` — fold issued set, draw via `harp.GenerateName`, redraw on collision (extend the `uniqueHarpID` idiom to the whole log).
  - `repair()` — on fold, detect two `add`s with the same harp but different origin/text → append a `rekey` for the later one (covers the post-100-draw fallback and the concurrent-mint race).
  - Implement `List`, `Add` (→ `add` event, stamps `Session`), `SetStatus` (→ `status` event, stamps `Session`), `Remove` (→ `status: Archived` or a `remove` op — decide during impl), `Snapshot`, `Summarize`, `Path` against the fold.
- [ ] `compaction` stub: fold → rewrite preserving the **full issued-harp set** (never free a harp). Real compaction can land later; document the invariant now.
- [ ] Tests (`internal/tasks/log_test.go`): fold last-write-wins; malformed-line skip; rekey repair on duplicate add; concurrent appends (goroutines) all land; harp never reused after Archived.

**Done when:** `OpenLog` satisfies every method the handlers call, with fold/rekey/lock covered by tests.

### Phase 2 — Origin-session reference + integration

Point the one integration seam at the new backend; optionally close the 0019 gap.

- [ ] `Task.OriginSession` populated from the `add` event; surfaced in `List`/`Snapshot` so `task_list` can annotate provenance.
- [ ] Rewrite `cmd/tasks_cmd.go:openSessionTaskStore`: resolve project-id (`CTXLOOM_PROJECT_ID`, else `projectid.Resolve(wd)`), map to `paths.TasksLogPath(id)`, return `tasks.OpenLog(path, os.Getenv("CTXLOOM_SESSION_HARP"))`. Drop the `SessionsRoot/ResumedFrom/RestoreTasks` plumbing.
- [ ] (Optional, ADR 0019) add `internal/operations/tasks.go` — `List/Add/SetStatus` thin wrappers over the store — and route both `cmd/mcp_tools_tasks.go` and `cmd/tasks_cmd.go` through it, so the two frontends share one path.
- [ ] MCP/CLI handler bodies unchanged beyond the store source (same method surface).
- [ ] Tests: MCP `handleTaskAdd` stamps the env harp as `OriginSession`; `task_list` round-trips through the log.

**Done when:** adding a task in a session writes an `add` event referencing that session's harp; list/status/summary work through the log from both MCP and CLI.

### Phase 3 — Migration + retire old paths

- [ ] `internal/tasks/migrate.go`: on first open of a project's log (file absent), import existing markdown — legacy `<projectDir>/.ctxloom/tasks.md` (origin unset) and any per-harp `~/.ctxloom/sessions/<harp>/tasks.md` (origin = that harp) — replaying each task as an `add` (+ `status` if not To Do) via the existing `parseFile`. Idempotent: guarded by log existence.
- [ ] Remove `OpenSession` move-on-resume (`migrationSource`/`moveStore`); keep `parseFile` for migration reads only. Retire `renderFile`/`write` once nothing writes markdown.
- [ ] Simplify `CollectForSessions` (the `ctxloom tasks run` picker): one project log replaces the per-harp + legacy scatter.
- [ ] Tests: migration replay produces the expected fold; second run is a no-op; per-harp origin preserved.

**Done when:** a project with pre-existing `tasks.md` files comes up with all tasks in the log, correct origins, and the markdown writers are gone.

### Phase 4 — Movement layer (DEFERRED — ADR revive triggers)

Not built in the core. Stub only the reconcile target that Phase 0/1 fork warnings point at.

- [ ] `ctxloom project repoint <path>` — manual override of the registry mapping.
- [ ] `ctxloom tasks dump <dir>` / `load <dir>` — private relocation; honors the wall (no bundle path). Load merges into the target log via the same mint-with-check + rekey, preserving project-id for continuity.
- [ ] Copy-fork seeding: optionally clone the contested id's log into the fork via dump/load instead of starting empty.

## Cross-cutting

- **Fault tolerance:** malformed event → warn + skip; unresolvable identity → warn + fork/degrade; un-mintable harp → rekey. Never block `task_list`, never block startup.
- **The wall:** nothing in this work touches bundles. The marker is gitignored; dump/load (deferred) is a separate subsystem from bundles.
- **Concurrency:** registry and log are each filelocked on write; log appends are single-line `O_APPEND`.

## Open questions to settle during implementation

- `Remove` semantics in an append-only log: `status: Archived` vs a dedicated `remove` op (and whether Removed harps stay reserved — they should).
- Whether Phase 2 lands the `internal/operations/tasks.go` layer now or defers the 0019 cleanup to a follow-up.
- Registry filename (`index.yaml` to match sessions, vs `registry.yaml`).
- Should session `Entry.ProjectDir` start resolving through project-id now, or stay raw until a move actually breaks matching (ADR defers this).
