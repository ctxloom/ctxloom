# `internal/shared` — architecture reference

Reference pages for the shared layer: the engine-agnostic agent substrate plus the leaf packages every ctxloom-family binary builds on. Each page opens with what the subsystem is and the contract it owns, carries a diagram, an inventory of exported symbols with `file:line`, and an explicit list of invariants and contracts. Where a package's documented behaviour and its real behaviour differ, the real behaviour is documented and the divergence is called out in one line.

Defects, verdicts, and remediation belong in `FINDINGS.md`, not here.

## The agent substrate — `internal/shared/agent`

The largest package in the layer (26 internal importers). It has no single responsibility, so it is documented by concern rather than by file.

| Page | Purpose |
|---|---|
| [`agent-backend-contract.md`](agent-backend-contract.md) | What a backend must implement (`Backend`, `ContextProvider`, `SessionHistory`, `SettingsWriter`, `ContextWriter`), the `Base*` embeddables every engine reuses, the `Launcher` process seam, and the cross-cutting `PermissionMode` / `ThinkingLevel` enums. |
| [`agent-session-ir.md`](agent-session-ir.md) | The normalized transcript IR (`SessionEntry` and friends) every engine's history is mapped into, plus the shared JSONL parse loop. |
| [`agent-chat.md`](agent-chat.md) | The optional `StructuredChat` capability: request/event unions, ACP transport declaration, and chat MCP-server composition. |
| [`agent-enginecli.md`](agent-enginecli.md) | `EngineCLI` — the declared vendor-CLI grammar that both the real driver and the mock engine parse against; the anti-drift mechanism of the launch path. |
| [`agent-surface-delivery.md`](agent-surface-delivery.md) | Surface kinds, approaches, `SurfaceSet` declarations, the `SurfaceSelection` builder, and isolated-cell vs shared-cwd delivery. |
| [`agent-launch-lifecycle.md`](agent-launch-lifecycle.md) | `LaunchBackend` — the shared Setup/deliver/Cleanup path every local-CLI engine embeds, and its exec half. |
| [`agent-context-delivery.md`](agent-context-delivery.md) | Fragment assembly, the hash-named context cache file, chunking, SessionStart injection hooks, and the flock chunk-ordering rendezvous. |
| [`agent-managed-files.md`](agent-managed-files.md) | Every writer that puts bytes into a user's engine config: atomic writes, marker sections, manifest-tracked trees, command/skill rendering, and MCP registries. |

## Configuration and policy

| Page | Purpose |
|---|---|
| [`confload.md`](confload.md) | The config precedence chain (home file < project file < env < `--config-set`) shared by every ctxloom-family binary, plus overlay merge and path resolution. |
| [`strictness.md`](strictness.md) | The fail-loudly policy layer: classified `Finding`s at startup chokes and the process-wide `--degraded` switch that reverts them to warn-and-continue. |

## Task tracking — `internal/shared/tasks/*`

| Page | Purpose |
|---|---|
| [`tasks-store.md`](tasks-store.md) | The append-only JSONL event log, the fold to current state, the `Task` model, compaction, path resolution, and the operations layer. |
| [`tasks-identity.md`](tasks-identity.md) | Project identity: the in-tree marker, the home registry, and the move-vs-copy-vs-new resolution procedure. |
| [`tasks-schema.md`](tasks-schema.md) | The declared tag vocabulary (`tagschema`), the derived priority model, and the lint pass over both. |
| [`tasks-triggers.md`](tasks-triggers.md) | The pure triage core for deferred-task revive triggers: evidence shaping, the closed query vocabulary, prompt construction, and verdict parsing. |

## Filesystem, process, and leaf primitives

| Page | Purpose |
|---|---|
| [`filesystem-io.md`](filesystem-io.md) | Atomic writes and error-latching writers (`iox`), advisory file locking (`github.com/gofrs/flock` + `internal/paths`'s lock-path derivation), and filesystem watching (`watch`). |
| [`process-execution.md`](process-execution.md) | Starting and observing children: pty-attached runs (`ptyrunner`), liveness checks (`pidalive`), stderr tail capture (`stderrtail`), and login-shell `PATH` resolution (`shellenv`). |
| [`identity-and-primitives.md`](identity-and-primitives.md) | Harp ID minting and markers, git-root discovery, sets and sorted keys, text helpers, and token estimation. |
| [`wire-types.md`](wire-types.md) | The engine-agnostic hook and MCP-server vocabulary that crosses the host↔engine boundary, and the merge/append/default-resolve operations on it. |
| [`cli-support.md`](cli-support.md) | The six CLI-side leaf helpers shared across the binaries: `clidiag`, `cliemit`, `cliversion`, `companionloadout`, `plans`, `upgrade`. |
