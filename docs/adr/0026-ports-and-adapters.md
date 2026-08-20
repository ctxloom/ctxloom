# 0026 — Ports and adapters: domain logic in operations; subsidiary-application IO via plugins; persistence via storage ports

**Date:** 2026-06-02.

## Status

Accepted (the architecture and its inbound + LLM edges, already in force via ADRs 0019/0020) and Proposed (the storage-port edge, adopted incrementally rather than in a big-bang refactor).

**AMENDED 2026-08-20 — the profiles storage port was retired, by owner ruling.**
`profiles.Source` / `profiles.Store` were the first instance of the storage-port
edge this ADR proposes. They were deleted along with the `MemStore` adapter.

The reason is not a rejection of the principle; it is what the instance turned
out to be. It had exactly ONE real adapter (`*Loader`) and one test double, and
the double's only consumer outside `internal/profiles` was the port test proving
the port existed — an abstraction whose sole client was the test justifying it.
Retiring it also removed a quiet hazard: the double was more permissive than the
adapter (it accepted an empty profile the loader refuses), so a test written
against the port could pass against behaviour production does not have.

What this ADR still asks for is unchanged. A storage port earns its place when a
SECOND real adapter exists or is committed to — not in anticipation of one. The
proposed edge stays proposed; this instance is simply not evidence for it.

## Context

Two prior ADRs each settled one edge of the same architecture without naming the whole:

- **[0019](0019-cli-pure-frontend.md)** — the *inbound* edge: every frontend (CLI, MCP) parses input, calls `internal/operations`, and renders output; it does no domain logic. Operations is the sole component that touches domain state.
- **[0020](0020-operations-llm-boundary.md)** — one *outbound* edge: operations reaches an LLM only through the injected `Distiller` interface and the `internal/lm/backends` polymorphic package. No model IDs, prompts, or backend-identity branching leak into the core.

The third edge — persistence — has no stated principle, and the code shows it. Storage is concrete filesystem in every domain: `bundles.Loader`, `profiles.Loader`, `remote.LockfileManager`, `config.Config`, `sessions.Manager`, `projectid.Manager` all read and write files directly. Operations frequently constructs a concrete loader inline (`bundles.NewLoader(...).Load(name)` in `ReadBundle`) and writes through `Bundle.Save()` → `os.WriteFile` — domain logic reaching straight past any port to the disk. Only two places hint at the missing seam: `remote.BundleByteSource` (an interface abstracting *where bundle bytes come from*) and the tasks `Store`'s internal markdown-vs-log backend (ADR [0025](0025-per-project-task-log.md)).

There is also a live 0019 violation that the same principle resolves: the task store's resolution and domain logic (project-id resolution, migration, list/add/set-status) lives in `cmd` (`openSessionTaskStore` and friends), not in operations.

Note `afero.Fs`, used widely, is *not* this seam: it swaps one filesystem implementation for another (real vs in-memory, for tests). The missing port is at the *repository* level — "which storage technology holds bundles," not "which filesystem" — and the two compose.

## Decision

Adopt **ports and adapters** as the standing architecture, with `internal/operations` as the core and three edge types:

1. **Inbound adapters — frontends.** Per 0019. CLI, MCP, and any future frontend translate external input into operations calls and render results. No domain logic.

2. **Subsidiary-application plugins — outbound IO to external programs.** When the core must drive another application (today: LLM backends), it does so through an interface the core depends on and an adapter that frontends construct and inject — never by naming or branching on a specific application inside the core. Per 0020: the `Distiller` interface and the `internal/lm/backends` package are this edge. (Historical naming: these LLM application-plugins live under the package name `backends`; that is the *application* edge, distinct from the *storage* edge below. The conflated word "backend" is why this ADR names the storage edge "storage port / adapter" instead.)

3. **Storage adapters — outbound persistence behind ports.** Domain state is read and written through a *port* — a Go interface the core depends on — whose default adapter is the filesystem, but which equally admits a database, cache, or remote source with no change to core logic. The established shape is `remote.BundleByteSource` (a bytes-source port) and the tasks `Store` backend seam; the direction is to give each domain's persistence such a port (e.g. a `BundleLoader` interface operations depends on, implementable by filesystem or database) **as that domain's logic moves into operations or gains a second backend** — not speculatively, and not all at once.

The invariant tying the three together: **the core depends only on ports; concrete IO — a frontend, an LLM adapter, a filesystem store — is constructed at the edge and injected.** A core function that constructs a concrete loader, calls `os.ReadFile`/`WriteFile` for domain state, or branches on a specific application's identity has reached across an edge it should depend on through an interface.

## Consequences

- The core becomes testable with in-memory adapters and portable across storage technologies and frontends. A bundle store could move from files to a database, or tasks from JSONL to a service, with the change confined to one adapter.
- The cost is indirection: a port per domain. This is paid **incrementally**. Do not abstract a domain's storage until its logic lives in operations and a port earns its keep (a second backend, a test seam, or a 0019 cleanup). Speculative ports are churn, the same trap 0020 declined.
- The first concrete step is the **tasks → operations** extraction: move project-id resolution, migration, and list/add/set-status out of `cmd` into `internal/operations`, with the tasks `Store` as the storage adapter behind it. That closes the standing 0019 gap and makes tasks the worked example of this ADR. Realizing a full storage *interface* for tasks (vs. calling the concrete `Store`, which already carries the markdown/log seam) is deferred until a second non-filesystem backend is real.
- `afero.Fs`-based filesystem swapping stays as-is; it composes beneath a storage port rather than competing with it.

## Relationship to prior ADRs

- **0019** is the inbound edge of this architecture; unchanged.
- **0020** is the canonical subsidiary-application plugin edge; unchanged. This ADR generalizes its shape ("inject an interface, don't construct a backend") to any external application and names it.
- **0025**'s task-log backend seam is an early storage-adapter instance and the substrate for the extraction named above.

**Revive / compliance triggers (storage edge), addressed incrementally:**
- A core (`internal/operations`) function constructs a concrete loader/manager inline or calls `os.ReadFile`/`os.WriteFile`/`loader.Save()` for domain state → that domain wants a storage port; introduce one when touched.
- A domain needs a second persistence backend (DB, cache, remote) → realize its port then.
- IO to a *new* subsidiary application appears baked into operations rather than behind an injected interface → that is a leak (same bar as 0020), fix at introduction.
