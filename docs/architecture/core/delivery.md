# core/delivery — the delivery layer

`internal/core/delivery` decides which package items go STATIC and which
DYNAMIC for an engine (`Route` → `Plan`), and declares the two delivery
ports and the ONE ownership record. It knows no engine argv, transport or
config. The static port is implemented by `internal/adapters/fsstatic`,
the record by `internal/adapters/confpatch` (`Records`), the dynamic port by
the runner's MCP package.

## The graph

```mermaid
flowchart TB
    classDef decide fill:#dfe,stroke:#282
    classDef consume fill:#eef,stroke:#228
    classDef store fill:#ffd,stroke:#a80

    PKG["composite.Package (decoded)"]:::decide
    PREF["delivery.Preference — the binding's Roots (agent set --root, validated by operations.ResolveAgentRoots) + accepted losses"]:::decide
    ROUTE["delivery.Route(items, engine root, pref, cell roots) → Plan{Static routes, Dynamic refs, Losses} | ErrUncarried | Unrootable"]:::decide
    LO["delivery.Loadout — launch.Launch.Loadout(pkg): the ONE builder the runner and the local launcher share; delivery.InputsFor(lo) projects it into every kind's typed inputs once"]:::consume
    TGT["delivery.Target{Root (absolute), Ownership, Writer} — Validate refuses the zero value and a relative root; launch.Launch.Target(records) for a session, operations.ProjectTarget for a materialize"]:::decide
    STATIC["fsstatic.Static.Deliver(lo, surfaces, target): reconcile the writer to EMPTY from the record → per static item: the engine's typed Deliver over an OVERLAY of the target fs → every written file recorded under the writer (Ownership.Apply)"]:::consume
    REC[("confpatch.Records — ONE record per target file, writer-tagged, home-rooted: a hew reversal for JSON/YAML/TOML (the user's own entries survive), the pre-image for opaque files; a created file leaves with its last writer; drift is refused")]:::store
    DYN["Dynamic.Serve(lo, ServePolicy) — the runner's MCP package BINDS Launch.MCP"]:::consume
    EMPTY["the EMPTY plan = uninstall for that writer: only what the record names under the target's roots is removed (manage uninstall / hooks uninstall → operations.RemoveProject)"]:::consume

    PKG --> ROUTE
    PREF --> ROUTE
    ROUTE --> LO
    PKG --> LO
    LO --> STATIC
    TGT --> STATIC
    STATIC <--> REC
    LO --> DYN
    EMPTY --> STATIC
```

## Who delivers, and under which writer

| Caller | Target | Writer | Symbol |
| --- | --- | --- | --- |
| a delegated run (the runner tail) | the cell's advised roots: the session home, and the project root where the binding selected it | `delivery.SessionWriter(harp)` | `runner.Execute` → `Static.Deliver(l.Loadout(pkg), kind.Root().Surfaces(), l.Target(records))` |
| `profile materialize` | the `--target` directory (absolute) | `delivery.ProjectWriter` | `operations.MaterializeProfile` → `operations.DeliverProject` |
| `manage install` / `manage hooks install` / `manage statusline` | the project root | `delivery.ProjectWriter` | `operations.ApplyHooks` → `applyHooksToBackend` → `operations.DeliverProject` |
| `manage uninstall` / `manage hooks uninstall` | the project root | `delivery.ProjectWriter` | `operations.RemoveHooks` → `operations.RemoveProject` (the empty plan) |

Two writers meet on one project-root file (a session whose binding selected
the shared root, and a materialize): each keeps its own entries in the one
record and each reconcile-to-empty leaves the other's in place —
`TestStatic_TwoWritersOneTarget_ReconcileRemovesOnlyOwnEntries` and
`TestRecords_TwoWritersOneStructuredFile_EachRemovesOnlyItsOwnEntries`.

## The at-rest plan

`operations.ProjectPlan` selects the project root for every kind the
engine's approach offers there; a kind the engine does not carry is an
accepted loss the report names (`MaterializeProfileResult.NotCarried`); a
carried kind offering no root the project target has is `Unrootable`,
refused with the remedy, never rerouted. Whether the CONTEXT is a file at
the project root or rides the engine's session-start injection hook is
derived from the declaration alone (`operations.contextRidesTheHook`): an
engine whose context approach is told on argv and which exports a
`session_start` event takes it live; an engine that opens a context file
reads the file. `profile materialize` always writes the file (a
materialized tree must be readable with ctxloom out of the loop).

## Hooks are a delivered surface the engine fires

The mock kind delivers its hook file and its turn READS it back: a
`pre_tool` hook delivered as a static item fires when the turn runs a tool,
with the mock's payload on the hook's stdin, decoded by `Engine.Hooks()` —
`TestMock_ADeliveredPreToolHookFires_WhenTheTurnRunsATool`. Claude's half
is the registration/decode round trip
(`TestHooks_ADeliveredPreToolHook_RoundTripsThroughTheCodec`); that claude
itself runs the registered command is claude's contract, not provable
without a launch.

## What the record replaced

The ledger sidecar (`.ctxloom-managed` beside every managed directory) and
the marker section inside a context file were two in-place ownership
mechanisms. Under the record, a context file is APPENDED after the user's
bytes (`iox.AppendSection`) and the record keeps the pre-image; a
structured file keeps a hew reversal. An old sidecar is not read: the
legacy writers that still consult one (the host plugin arm's `Setup`, until
slice 13 moves that arm onto the runner) keep writing it, and the new
static writer records whatever an approach wrote — a sidecar an approach
still writes is owned by the record like any other file and leaves with the
empty plan.
