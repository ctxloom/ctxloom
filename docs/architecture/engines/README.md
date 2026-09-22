# Engine & launch layer

How ctxloom turns "run this agent" into a running vendor engine process. This
directory documents `internal/engines` (the composition root, the per-engine
packages and the conformance suite) and `internal/adapters/isolation` (the
isolation seam).

**The one architectural fact to carry into everything else**: ctxloom holds no
provider SDK and makes no direct model-API call. Every engine reaches its model by
spawning the **vendor's own binary**. This is a licensing invariant, not a style
preference, and it lives in the *shape* of every engine's `Instance.Exec`: an
argv for the vendor's binary, never an HTTP client.

## The definition / instance split

An engine is two halves on one port (`internal/core/engine`):

- **The DEFINITION — declarative, in `internal/core/engine`.** An engine KIND
  is a value: the engine package's struct embedding `engine.Base`, whose
  `Definition` declares one typed approach per surface kind (`Context`, `MCP`,
  `Settings`, `Hooks`, `Commands`, `Skills`), the optional `Dynamic` approach,
  the modes and their argv grammars (`CLI`, derived from the L1 `EngineCLI`),
  the permission facts (the host default posture, whether `plan` is
  read-only) and the export schema. `Base` derives every view
  (`Surfaces`, `Carries`, `Static`), decides static-vs-dynamic delegation
  once (`Delegate`), and `Validate`s coherence in the ONE plain constructor
  each engine package exposes (`claude.Build`, `mock.New`/`mock.Build`).
  `engines.Build()` composes the kinds into an `engine.Registry` value.
  `core/engine/conformance` asserts the declarative half for every kind;
  each engine package runs it against its own constructor.
- **The INSTANCE — what runs a session, on the port.** `Engine.Instance(Session)`
  binds one session and is where REQUIREDNESS is refused (a kind whose
  `Definition` lacks the context surface the session needs refuses it by
  name, `engine.ErrUnsupported`); `Instance.Exec(presented)` is the ONE
  place an engine's argv/env/cwd is composed — for claude, `buildArgs` and
  `Chat` are projections onto it and the launch golden
  (`engines/claude/testdata/exec_parity.golden`) pins every launch of the
  matrix byte-identical; `Instance.Drivers()` are the engine's native
  structured drivers (claude: a per-turn stream-json driver; empty means
  pty-only and a Structured launch is refused with
  `ErrUnsupported{drive}`); `Instance.Resume(key)` re-attaches a native
  session. The engine's own stories are the kind's methods: `Home()`
  (`engine.HomeSpec`, the zero value the null object; the credential seed
  carries the deliveries it accepts), `Container()` (a spec or a refusal),
  `Transcripts()` (readers the composition root hands in — they are
  transcript adapters an engine must not import) and `Hooks()` (the native
  payload codec). `adapters/isolation` reads those facts off the engine
  (`isolation.FactsOf`), and `adapters/runner.Execute` binds the Instance
  before delivering. `core/engine/conformance` asserts both halves
  (Part 4.2 test A in full) for every kind.
- **The composition root and the seam's remainder.** `engines.Compose()`
  builds the shipped kinds once per process (`engines.Registry()` is what
  every adapter that resolves an engine by name reads; `engines.Use` is the
  test seam; `isolation.RegistryFacts` is installed beside it by the root
  that composed). What `agent.Backend` still needs that the port does not
  carry — the backend constructor, the typed config, the named-form table a
  binding's `surfaces:` is validated against, the settings writer `manage
  status` reads, the project/global settings collision guard — is
  `agent.Hosted`, implemented by each engine VALUE and asserted on the
  registry's value (`engines.Hosted`); the L1 process-surface grammar the
  standalone mock impersonates is `agent.EngineCLIProvider` on the same
  value (`engines.EngineCLIs`). Both leave with `agent.Backend`. The
  structured drive is per turn (`Instance.Drivers()[0].Turn`); the
  interactive pane is the hosted engine's `Backend.Execute` over the
  runner's launcher (`runner.RunLaunchSpec`, a tmux pane on the runner's
  terminal). The managed-hooks assembly is `operations/managedhooks`.

Core code reads an engine's facts off the Definition (through the
registry) and never branches on its name: `tests/arch`'s
`no-engine-name-in-core` gate holds that, with a shrinking allowlist.

## Start here

| If you want to know… | Read |
|---|---|
| **"Does engine X support Y?"** | **[Capability matrix](capability-matrix.md)** — engine × capability, every cell sourced |
| What the `Backend` interface still is, and how engines are composed | [Backend abstraction & registry](backend-abstraction.md) |
| How a run got from the host to an engine process over the go-plugin wire (RETIRED, slice 13 — kept as history until the runner is documented here) | [The plugin wire](grpc-wire.md) |
| What "isolated" actually means, per axis and per engine | [Isolation](isolation.md) |

## Per-engine adapters

| Page | Backend id | Drive | `plan` enforced? | Notable |
|---|---|---|---|---|
| [claude](claude.md) | `claude-code` | vendor CLI | **yes** | The exercised default. Carries a native per-tool deny list, and its approaches declare an out-of-cwd form (`agent.OutOfCwd`) so a shared cwd is never written into |
| [mockengine](mockengine.md) | *(not a backend)* | it *is* the engine | n/a | A fake vendor CLI that proves context delivery — and what a mock-only pass does not prove |

The in-process test doubles (`mock.Name` and its siblings, `mock.Doubles`) are
composed engines too; they have no page of their own and are described
alongside the composition in [Backend abstraction & registry](backend-abstraction.md).

## The shape of the launch path

```mermaid
flowchart LR
    CFG["config + profiles<br/>+ bundles"] --> LNCH["launch.Resolve → launch.Launch<br/>(the package, the cell, the label)"]
    LNCH --> CH(["RunnerChannel.StartRun<br/>(the coordinator → the runner)"])
    CH --> RUN["runner.Execute: Kind.Instance(session),<br/>delivery.Static over the plan"]
    RUN --> INT["interactive: Hosted.Backend.Execute<br/>over runner.RunLaunchSpec (tmux pane)"]
    RUN --> STR["structured: Instance.Drivers()[0].Turn<br/>over Instance.Exec"]
    INT --> ENG["vendor engine process"]
    STR --> ENG

    ISO["isolation (the cell)"] -.->|host / worktree / container| RUN
```

The runner is the ONE unit on both cells: `ctxloom runner <engine>` hosts the
launch on the host or as a container's foreground, and the Launch it decodes
is the same value the originator resolved — there is no hand-written wire
projection of a managed config left to drift.

## Facts worth knowing before you read any source here

These are documented in full on the pages above; they are collected here because
each one contradicts what the surrounding code looks like it does.

1. ~~**The launch wire is hand-written and nothing but a test binds it to the Go struct.**~~ — **RETIRED with the go-plugin wire (slice 13).** The runner decodes the same `launch.Launch` the originator resolved; the historical account stays in [wire](grpc-wire.md).
2. ~~**`wire.Hook.PreToolFallback` is always `false` on the engine side**~~ — **RESOLVED `40b49a7f`.** It is persisted, bundled, trust-hashed and now carried; no registered engine reads it at launch today, and it stays wired for whichever engine needs it next. → [wire](grpc-wire.md)
3. ~~**`ChatRequest.Runtime` does not cross the wire**~~ — **RESOLVED `40b49a7f`.** It used to mean a container-bound structured session ran the engine on the host while the session summary reported container isolation. Repairing it *activated* a path-confinement hole it had been masking, which is why confinement landed first (`73ea8d7f`). → [wire](grpc-wire.md)
4. ~~**An unprofiled backend's container inherits claude's credentials.**~~ — **RESOLVED `a6d9bd95`.** The `default:` arm of `engineContainerSpecFor` returned `resolveClaudeContainerAuth` for any unrecognized engine. It now fails closed, and `runtime: container-*` for an engine with no auth mapping is refused when the binding is *written*, not when it is launched. → [isolation](isolation.md)
5. **Isolating a shared cwd without a container requires `agent.OutOfCwd`.** claude-code's approaches declare it; a backend whose approaches lack it falls back to the loudly-warned well-known write, and concurrent per-agent isolation for it needs a worktree or a container cell. → [matrix §4](capability-matrix.md)
6. **No composed engine has a live transcript scraper.** claude-code's was deleted outright rather than demoted (its `Backend.History()` is nil, which `operations.HistoryForBackend` refuses by name), and the mock's answers every read with an error; canonical capture is written runner-side into `internal/adapters/transcript`. → [matrix §6](capability-matrix.md)

## Scope

Covered here: `internal/engines` (the composition root), `internal/engines/conformance`,
`internal/adapters/isolation`, `internal/engines/claude`, `internal/engines/mock`.

Types shared with the rest of the system — `agent.Backend`, `agent.ManagedConfig`,
`agent.PermissionMode`, `agent.SurfaceInputs`, `agent.CellKind` — live in
`internal/core/agent` and are documented here from the launch layer's point of
view.

These pages record **behavior**, not verdicts. Defect triage lives in `FINDINGS.md`;
where behavior diverges from what a doc comment or interface implies, the divergence
is stated as a fact with a `file:line` and nothing more.
