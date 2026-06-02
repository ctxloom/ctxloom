# Implementation plan — adopting ADR 0026 (ports & adapters) and closing ADR 0019 gaps

Living plan for [ADR 0026](adr/0026-ports-and-adapters.md) (the architecture) and the [ADR 0019](adr/0019-cli-pure-frontend.md) gaps the same work closes. The ADRs hold the *why*; this holds the *how*, the survey, and the sequencing.

## Status (execution)

Phases A–D executed; full offline suite green (`just test-verbose`, 0 failures).

- **A — tasks → operations: done.** `internal/operations/tasks.go` (`TaskContext`, `ListTasks`/`AddTask`/`SetTaskStatus`, `ResolveProjectIdentity`, resolution + migration). MCP handlers, CLI commands, the `tasks run` picker, `run.go` `--seed-task`, and the task-summary resource all route through it; `cmd` no longer touches the task store.
- **B — bundle storage port: done.** `bundles.Source`/`Store` ports + `fsStore` (which also fixes the latent os-vs-afero write split) + `MemStore`. `Bundle.Save` removed; all 8 write ops inject `req.Store` (default filesystem). `ListBundles`/`GetBundle` added.
- **C — sessions → operations: done for the standalone surface.** `internal/operations/sessions.go` (List/Get/ListForProject/Rename/Forget/Bind). `session_cmd.go`, `mcp_resources.go`, `mcp_tools_memory.go` rerouted. **Deferred:** `run.go`'s session orchestration (the held `Manager` feeding the resume picker + the interactive schema-upgrade confirm + `AssignHarp`/`MarkEnded`/adopt) — a focused launch-path pass, kept out to avoid risking the fault-tolerant hot path.
- **D — read-path → operations: bundle core done.** `bundle list`/`bundle show` route through `operations.ListBundles`/`GetBundle` (the latter on the Phase B port). **Long tail (read-path plumbing, deferred):** `profile list/show/default` (custom rendering over `ListProfiles`/`GetProfile`'s result shape), `completion.go`, `item_helpers.go`, `bundle_distill.go`'s distill-prompt loader, `bundle_transfer.go`, and `run.go:350`'s context-assembly loader.

## Two intertwined threads

This work has two distinct edges, often confused because the same refactor touches both:

- **0019 — where logic lives.** Domain logic belongs in `internal/operations`, not in `cmd`. Closing a gap means *creating the operations function* and making the frontend call it.
- **0026 — how the core reaches storage.** Operations should depend on a *port* (interface), not construct a concrete loader or call `os.WriteFile`. Closing a gap means *introducing the interface + a filesystem adapter* and injecting it.

A domain can be 0019-clean but 0026-concrete (operations owns the logic, but reaches the disk directly), or 0019-dirty (logic still in `cmd`). The plan sequences both.

## Survey — current state

| Domain | 0019 (logic in operations?) | 0026 (storage behind a port?) |
|---|---|---|
| **tasks** | ✗ `cmd` resolves project-id, migrates, calls `Store` directly | ✗ concrete `Store` (internal markdown/log seam only) |
| **sessions** | ✗ no `operations/sessions.go`; `Rename`/`Forget`/`Bind`/`AssignHarp` + reads in `cmd` | ✗ concrete `Manager` |
| **bundles** | ~ writes go through operations; **reads** (`List`/`Load`/`GetPrompt`) inline in `cmd` | ✗ concrete `Loader`; persistence on `Bundle.Save()` |
| **profiles** | ~ mutations in operations; **reads** (`List`/`Load`) inline in `cmd` | ✗ concrete `Loader` (injected as a *concrete* type via `req.Loader`) |
| **projectid** | ✗ resolution in `cmd` (`resolveProjectID`) + `run.go` | ✗ concrete `Manager` |
| **remotes/lockfile** | ✓ operations | ✗ concrete `LockfileManager` (inline-constructed in operations) |
| **config** | ✓ operations | ✗ concrete, but `ConfigLoaderFunc` injection seam exists |
| **LLM (0020)** | ✓ | ✓ `Distiller` + `backends` plugin — compliant |

Fixed since the 0019 audit: export/import, default-setters, init bootstrap. Established injection seams in operations to build on: `Distiller`, `req.Loader` (profiles), `FS afero.Fs`, `ConfigLoaderFunc`.

## Principles for this work (from 0026)

- The core depends only on ports; concrete adapters are constructed at the edge and injected. Reuse the existing Request-field injection seam.
- **Incremental, not speculative.** Don't port a domain's storage until its logic is in operations *and* a port earns its keep. The exception below is deliberate and flagged.
- **`afero.Fs` composes beneath a port; it is not the port.** A storage port is repository-level (`BundleStore`), not filesystem-level (`afero.Fs`).

## Phases

Ordered by the two explicit asks first (tasks 0019 fix, bundle port), then the broader cleanup. Each phase leaves the tree green (`just test-verbose`).

### Phase A — tasks → operations (0019 closure; the worked example) — PRIORITY

Move all task domain logic out of `cmd` into a new `internal/operations/tasks.go`. Frontends become pure.

- [ ] `internal/operations/tasks.go`:
  - `type TaskContext struct { WorkDir, ProjectID, SessionHarp string }` — the resolved inputs a frontend gathers (git-root, `CTXLOOM_PROJECT_ID`, `CTXLOOM_SESSION_HARP`).
  - internal `resolveTaskStore(tc) (store *tasks.Store, warning string, err error)` — moves `resolveProjectID` (projectid.Resolve when `ProjectID==""`) + `migrateLegacyTasks` (sessions.ListForProject → `tasks.MigrateLegacyIfNeeded`) + `tasks.OpenLog` here. The project-resolution warning is *returned*, not printed (operations doesn't render).
  - `ListTasks(tc, statuses, term, includeSummary) (*TaskListResult, error)`, `AddTask(tc, text, status) (*TaskResult, error)`, `SetTaskStatus(tc, harpID, status) (*TaskResult, error)`. Results carry `Path`, the task data, and `Warning`.
- [ ] `cmd`: add a `taskContext()` builder (git-root + env). Reroute `mcp_tools_tasks.go` handlers, the `tasks` CLI commands (list/add/status/summary), the `tasks run` picker, and `run.go`'s `--seed-task` to call operations; print returned warnings to stderr. Delete `openSessionTaskStore`, `resolveProjectID`, `migrateLegacyTasks` from `cmd` (keep `taskRunWorkDir` — input gathering is a frontend concern).
- [ ] `run.go` pre-launch project resolution (sets `CTXLOOM_PROJECT_ID` for the child) → optional `operations.ResolveProjectIdentity(workDir)` so the resolution lives in one place.
- [ ] Tests: `operations/tasks_test.go` (resolve/migrate/list/add/status with a temp HOME); update the `cmd` task tests to the operations seam.

Scope note: Phase A is 0019-only. It calls the concrete `tasks.Store` (which already has the markdown/log seam); a tasks *storage interface* is deferred to Phase E — no second backend yet.

### Phase B — bundle storage port (the 0026 worked example) — PRIORITY, larger

Introduce the polymorphic bundle loader the ADR names: a port operations depends on, FS adapter today, DB-swappable tomorrow.

- [ ] `internal/bundles`: define the port.
  - `type Source interface { Load(name) (*Bundle, error); List() ([]*Bundle, error); LoadFile(path) (*Bundle, error) }` — the concrete `Loader` already satisfies this.
  - `type Store interface { Source; Save(*Bundle) error; Delete(name string) error }`.
  - **Untie persistence from the data type:** move `Bundle.Save()`'s `os.WriteFile` into an `fsStore.Save(b)` adapter (FS adapter wrapping `Loader` for reads + write/delete). `Bundle` becomes pure data (a DB adapter keys by name, not `Bundle.Path`). This is the crux and the riskiest edit.
- [ ] `internal/operations`: depend on the port. Provide `cfg.BundleStore()` (or a `req.Store bundles.Store` field, matching the existing `req.Loader` seam) and reroute the write ops (`CreateBundle`, `UpdateBundle`, `DeleteBundle`, `AddItem`, `DeleteItem`, `SetItemContent`, `DistillItem`, `SetBundleMCP`, `DistillBundleFile`) and the read ops (`ReadBundle`, `GetItemContent`, `loadBundleForUpdate`) off inline `NewLoader`/`bundle.Save()` and onto the injected port.
- [ ] (Optional) an in-memory `Store` adapter — proves swappability and sharpens the operations tests.
- [ ] Tests: operations bundle ops against the in-memory adapter; an FS-adapter conformance test.

Decision to confirm (see open questions): Phase B establishes the port **before** any non-FS backend exists — a deliberate exception to "not speculative," justified by it being the architectural reference + the user's stated DB-swappability goal. Worth confirming the appetite vs. deferring until a DB is real.

### Phase C — sessions → operations (0019 closure)

Largest 0019 gap after tasks; sessions are mutated in the hot `run` path and the SessionStart bind hook.

- [ ] `internal/operations/sessions.go`: `RenameSession`, `ForgetSession`, `BindSession`, `AssignSession` (mints a harp), plus reads (`ListSessions`, `GetSession`, `ListSessionsForProject`).
- [ ] Reroute `session_cmd.go`, `run.go` (`AssignHarp`), `mcp_resources.go`, `mcp_tools_memory.go`. Care: the bind hook and run path are fault-tolerant — preserve warn-and-continue.

### Phase D — bundle/profile read-path → operations (0019 closure)

- [ ] `operations.ListBundles`/`GetBundle`/`GetBundlePrompt`, `operations.ListProfiles`/`GetProfile`. Reroute `bundle_list.go`, `bundle_distill.go`, `profile.go`, `completion.go`, `item_helpers.go`. (Phase B's `Source` port is the natural read dependency for the bundle half.)

### Phase E — storage ports for remaining domains (deferred, incremental)

Only as touched or as a second backend appears: profiles `Store`, sessions/projectid `Manager` ports, lockfile port, tasks `Store` interface. Each follows Phase B's pattern. No big-bang.

## Sequencing, risk, test strategy

- **Order:** A → B → C → D, with E opportunistic. A is self-contained and high-value (closes the gap we just widened). B is independent of A and can run in parallel if desired, but is the riskiest (untying `Bundle.Save`). C is broad but mechanical. D rides on B's read port.
- **Risk hotspots:** B's `Bundle.Save()` removal (touches every write op + any caller of `Bundle.Save`); C's `run.go`/bind-hook fault-tolerance.
- **Tests:** `just test-verbose` (offline) after each phase; new operations tests use a temp HOME and, for B, the in-memory adapter.

## Open questions

1. **Storage-port appetite (Phase B):** establish the bundle `Store` port now as the reference (accepting churn + the `Bundle.Save` untie before a DB exists), or defer until a non-FS backend is actually wanted and do only Phase A + the 0019 cleanups (C/D) first?
2. **Injection style:** `cfg.BundleStore()` factory vs. a `req.Store` Request field (matches the existing `req.Loader` seam). Pick one convention for all future ports.
3. **Scope of this pass:** just Phase A now, or A+B as the paired "tasks fix + bundle port" the asks implied?
