# core/delivery — the delivery layer

`internal/core/delivery` decides which package items go STATIC and which
DYNAMIC for an engine (`Route` → `Plan`), and declares the two delivery
ports and the ONE ownership record. It knows no engine argv, transport or
config. The static port is implemented by `internal/adapters/fsstatic`,
the record by `fsstatic.Records`, the dynamic port by
the runner's MCP package.

## The graph

```mermaid
flowchart TB
    classDef decide fill:#dfe,stroke:#282
    classDef consume fill:#eef,stroke:#228
    classDef store fill:#ffd,stroke:#a80

    PKG["composite.Package (decoded)"]:::decide
    PREF["delivery.Preference — the binding's Roots (agent create/edit --root, validated by operations.ResolveAgentRoots) + accepted losses"]:::decide
    ROUTE["delivery.Route(items, engine root, pref, cell roots) → Plan{Static routes, Dynamic refs, Losses} | ErrUncarried | Unrootable"]:::decide
    LO["delivery.Loadout — launch.Launch.Loadout(pkg): the ONE builder the runner and the local launcher share; delivery.InputsFor(lo) projects it into every kind's typed inputs once"]:::consume
    TGT["delivery.Target{Root (absolute), Ownership, Writer} — Validate refuses the zero value and a relative root; launch.Launch.Target(records) for a session, operations.ProjectTarget for a materialize"]:::decide
    STATIC["fsstatic.Static.Deliver(lo, surfaces, target): per static item the engine's typed Deliver runs over an OVERLAY of the target fs and DECLARES what it owns (present.Delivered: Files owned whole, Claims on a place in a file, a section, an array element) → ONE safefs.Batch: release the writer's claims under the target's roots except on a declared file it did not rewrite → stage every declaration → Commit writes each changed file once"]:::consume
    REC[("fsstatic.Records — ONE claims record per target file, home-rooted: per place, the writers that put a value there (a session's value over the project's; the latest among sessions); a place leaves the file with its last writer; a user's value is never claimed, and ctxloom's own drifted value is refused")]:::store
    DYN["Dynamic.Serve(lo, ServePolicy) — the runner's MCP package BINDS Launch.MCP"]:::consume
    EMPTY["the EMPTY plan = uninstall for that writer: only what the record names under the target's roots is removed (ctxloom materialize --release, manage uninstall → operations.Release)"]:::consume

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

## The root order: the session home first, the project only by selection

Every static approach of every engine declares `present.RootSessionHome`
FIRST in its `Traits().Roots` — the root `Route` takes when the binding
selects none — and offers `present.RootProjectRoot` second (ruled
2026-09-21; pinned per engine by each engine package's
`TestBuild_EverySurfaceDefaultsToTheSessionHome` and
`TestRoute_DefaultBindingPlansOnlySessionHomeRoots`). A session delivers
only into its session home. The project root is reached only by the
binding's `roots:` selection (`delivery.Preference.Root`), and that
selection IS the unsafe option: the config reference names it so, `run
--dry-run` marks the route unsafe in its delivery section, and the launch
banner names it before the engine spawns. The user's real home is never
written — an approach that lands beneath the engine home refuses a run that
advised none (`agent.ErrUnrootedEngineHome`). The explicit project-side
door is `manage hooks install`.

On the host arm the plugin run-start selects legacy forms by name;
`operations.PreferPlanRoots` projects the plan's project routes onto that
selection so `roots:` governs it too, and the mock's legacy default form is
its session form (`mock.MockSessionFile`). The engine home is the
session's by default (`engine_home: session`; `agents.ParseHomeMode`), so
claude on that arm advises its session home
for context and MCP; only the binding's explicit `engine_home: host` — the
unsafe selection, named beside the project routes in the plan and the
banner (`cli.unsafeLabels`) — gives the launch no session home
(`launch.SessionHome` reports none), so the delivery selects the project
file there.

No credential is delivered into a home. A launch authenticates in the mode
its run settles (`launch.RunAuth`: the token for every agent, the top-level
`auth:` for the human's own session), resolved by `engine.Auth.Credentials`,
which the environment makes true where the engine runs; the session home
holds none. A mode whose credential the
launching environment does not export is refused with the engine's remedy;
ctxloom never mints or stores one. See
[isolation](../engines/isolation.md#credential-delivery).

## Who delivers, and under which writer

| Caller | Target | Writer | Symbol |
| --- | --- | --- | --- |
| a delegated run (the runner tail) | the cell's advised roots: the session home, and the project root where the binding selected it | `delivery.SessionWriter(harp)` | `runner.Execute` → `Static.Deliver(l.Loadout(pkg), kind.Root().Surfaces(), l.Target(records))` |
| `ctxloom materialize` | `--target` (any directory, symlink-resolved) or, with `--yes`, the project root | `project:<engine>:<kind>`, one per selected kind | `operations.Materialize` → `operations.Deliver` / `operations.Release` |
| the post-sync refresh | the project root (`projectroot.WorkDir()`, named explicitly) | `project:<engine>:<kind>` | `operations.syncMaterializeStep` → `operations.Materialize` |
| `profile materialize`, `manage hooks install`, `manage uninstall` / `manage hooks uninstall` (still shipped; to be removed in favour of `ctxloom materialize`) | the `--target` directory / the project root | `project:<engine>:<kind>`, every kind | `operations.MaterializeProfile` / `ApplyHooks` / `RemoveHooks` → `operations.Deliver` / `operations.Release` |

**Per-kind release.** An at-rest delivery for one engine is still ONE
`Static.Deliver` (so settings and hooks fold into one write of
`settings.json`), but its target names the kinds it speaks for
(`delivery.Target.Kinds`): each kind's claims are staged under its own tag
(`delivery.Target.WriterOf`), and only those tags are released or undone
(`Target.Writers`). A kind the run did not select has a writer the run never
releases, so `--surface context` re-delivers the context and leaves the
commands, skills, MCP entries and settings standing; and one engine's
delivery never releases another engine's files in the same directory. A
selected kind with nothing to deliver is released. A target with `Kinds`
nil (a session) is one writer for every kind, as before. Records under the
old bare `project` tag are left alone; nothing releases them.

The record store is owner-onlyThe record store is owner-only before any delivery writes through it, and
that is a security invariant: a claims record keeps every value ctxloom put
into the file it describes. The store lives under `paths.HomeRecordsDir`, one
of the home roots every ctxloom process establishes owner-only at startup
(`paths.EnsureHomeRoots`, through `safefs.Root`'s `Private`: a mode on unix,
an owner-only DACL on Windows), and every directory a delivery creates beneath
it is created `safefs.PrivateDirMode`; opening the store applies nothing
(`TestDeliver_CreatesAMissingRecordDirOwnerOnly`,
`TestDeliver_AtRest_ClaudesMCPRecordOverALooseRecordDir`).

Two writers meet on one project-root file (a session whose binding selected
the shared root, and a materialize): each keeps its own claims in the one
record and each release leaves what the other still claims —
`TestStatic_TwoWritersOneTarget_ReconcileRemovesOnlyOwnEntries`,
`TestClaimsASharedEntrySurvivesEitherWritersRelease` and
`TestClaimsAnAtRestApplyMidRunKeepsTheRunsEntry`.

## The placement core, and the at-rest plan

The planning and targeting every static delivery shares is one pair of
constructors: `delivery.PlanFor` (every kind the Definition does not carry
is an accepted loss; the binding's roots, or each approach's first offered
root the cell has; `Route`; then a kinds filter) and `delivery.TargetFor`
(the roots, under a writer family, split per kind when kinds are named).
The launch plans and targets through them (`launch.planLaunch`,
`Launch.Target`); an at-rest delivery reaches them through
`operations.Deliver` at an `operations.Placement` — the start roots, the
writer family, the kinds, the root preference and the context file — which
assembles nothing and reads no config. Root and writer family are its
parameters, so a session home could be prepared through the same core; it
is not yet: a session keeps its own assembly (`launch.Resolve`, which may
run in another process than the runner) and its own lifecycle (Undo on a
failed drive, the sweep of departed sessions).

At rest every kind the engine's approach offers at the project root lands
there; a kind the engine does not carry is reported as not carried
(`EngineOutcome.NotCarried`); a carried kind offering no root the target
has is `Unrootable`, refused with the remedy, never rerouted. By default
the context is delivered for every engine, including one whose in-the-loop
context rides argv: a materialized tree must be readable with ctxloom out
of the loop. `--surface context=file:PATH` names the file
(`engine.ContextInputs.File`), which an approach honours at a root or
refuses with `engine.ErrContextFileUnsupported`, never ignores.

ctxloom's own MCP endpoint is session-scoped and never written at rest:
`PlanFor` plans no MCP route that exists only for the session-endpoint
declaration unless the placement serves an endpoint (a session), and
`InputsFor` renders that declaration only from an endpoint the loadout
carries.

## Hooks are a delivered surface the engine fires

The mock kind delivers its hook file and its turn READS it back: a
`pre_tool` hook delivered as a static item fires when the turn runs a tool,
with the mock's own payload on the hook's stdin, decoded by `Engine.Hooks()` —
`TestMock_ADeliveredPreToolHookFires_WhenTheTurnRunsATool`. Every hooks
approach binds the set to its engine as it delivers it (`agent.BindHooks`):
ctxloom's callbacks gain `--engine <name>` and a hook narrowed to a neutral
tool class gets the engine's matcher, so the verbs read the firing engine's
wire through its codec ([hooks](../cli/hooks.md#the-hook-codec)). The mock's
hook file leaves out the events a double declares lost
(`TestMock_TheLossyDoublesHookFileOmitsItsDeclaredLosses`). Claude's half
is the registration/decode round trip
(`TestHooks_ADeliveredPreToolHook_RoundTripsThroughTheCodec`); that claude
itself runs the registered command is claude's contract, not provable
without a launch.

## What the record replaced

The ledger sidecar (`.ctxloom-managed` beside every managed directory) and
the marker section inside a context file were two in-place ownership
mechanisms. Under the record, a context file's section is claimed after the
user's text (`present.AppendedSection`) and a structured file's entries are
claimed place by place. Nothing writes a sidecar any more; one a project
still carries is claimed whole in the record from when it was written, is
declared by nothing, and so is released and removed by the next delivery
(`TestDeliver_RemovesALegacyManagedMarkerTheRecordClaims`).

## Ownership is declared, never inferred

An approach reports what it owns in `present.Delivered`: `Files`, owned
whole, and `Claims`, values in files it does not own. The static writer
claims exactly that declaration, whatever the approach wrote this run. A
declared file it did not rewrite keeps the writer's earlier whole-file claim
(which must exist, on a file that stands); a file written under a root but
not declared, or a path both declared and claimed, fails the delivery
(`fsstatic.ErrUndeclaredWrite` and its siblings). This is not a preference:
when ownership was inferred from writes, an approach that skipped an
identical write lost the file at the next commit. The contract is held for
every shipped engine by
`TestStaticDelivery_UnchangedRedeliveryKeepsEveryDeclaredFile`.
