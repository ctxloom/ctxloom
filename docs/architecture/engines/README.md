# Engine & launch layer

How ctxloom turns "run this agent" into a running vendor engine process. This
directory documents `internal/lm` (the backend registry and the gRPC plugin wire,
both retiring), `internal/adapters/isolation` (the isolation seam) and
`internal/engines` (the per-engine adapters and the conformance suite).

**The one architectural fact to carry into everything else**: ctxloom holds no
provider SDK and makes no direct model-API call. Every backend reaches its model by
spawning the **vendor's own binary**. This is a
licensing invariant, not a style preference, and it lives in the *shape* of the
registry table — the doc comment above `init()` in
`internal/lm/backends/registry.go` states it.

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
- **What `internal/lm/backends` still is.** The name-keyed registry over
  `agent.Backend` (`Setup`/`Execute`/`Chat`), the managed-hooks assembly,
  the pty launcher and the availability/version probes, paired with each
  kind through the HOSTING remainder in `internal/lm/hosting` (the backend
  constructor, the typed config, the named-form table, the settings writer,
  the hook scope guard, the version command). The structured drive is still
  `coord.EngineHost.Drive` over `agent.StructuredChat`: the port's per-turn
  `StructuredDriver` carries no channel for the mid-turn control messages
  the coordinator sends (permission answers, cancel, terminal), which is
  the open fork before the drive can move onto it.

Core code reads an engine's facts off the Definition (through the
registry) and never branches on its name: `tests/arch`'s
`no-engine-name-in-core` gate holds that, with a shrinking allowlist.

## Start here

| If you want to know… | Read |
|---|---|
| **"Does engine X support Y?"** | **[Capability matrix](capability-matrix.md)** — engine × capability, every cell sourced |
| What the `Backend` interface is, who implements it, how engines register | [Backend abstraction & registry](backend-abstraction.md) |
| How a run gets from the host to an engine process — and which fields do *not* survive the trip | [The plugin wire](grpc-wire.md) |
| What "isolated" actually means, per axis and per engine | [Isolation](isolation.md) |

## Per-engine adapters

| Page | Backend id | Drive | `plan` enforced? | Notable |
|---|---|---|---|---|
| [claude](claude.md) | `claude-code` | vendor CLI | **yes** | The exercised default. Carries a native per-tool deny list, and its approaches declare an out-of-cwd form (`agent.OutOfCwd`) so a shared cwd is never written into |
| [mockengine](mockengine.md) | *(not a backend)* | it *is* the engine | n/a | A fake vendor CLI that proves context delivery — and what a mock-only pass does not prove |

The in-process test doubles (`config.BackendMock` and its siblings) are
registered backends too; they have no page of their own and are described
alongside the registry in [Backend abstraction & registry](backend-abstraction.md).

## The shape of the launch path

```mermaid
flowchart LR
    CFG["config + profiles<br/>+ bundles"] --> ASM["backends.<br/>AssembleManagedConfig"]
    ASM --> MC["agent.ManagedConfig<br/>(7 fields)"]
    MC --> CONV["ManagedConfigToProto"]
    CONV --> PB["pb.ManagedConfig<br/>(7 fields)"]
    PB --> WIRE(["gRPC / unix socket<br/>go-plugin handshake"])
    WIRE --> SRV["GRPCServer.RunTurn"]
    SRV --> BE["agent.Backend<br/>Setup → Execute → Cleanup"]
    BE --> ENG["vendor engine process"]

    ISO["isolation.Prepare"] -.->|CellKind| SRV
    ISO -.->|worktree / container| ENG

    style CONV fill:#f884,stroke:#c44
```

The highlighted hop is hand-written and has no compiler link to the Go struct — it
carries all 7 fields today, and a reflective total-struct parity sweep
(`internal/lm/grpc/arch_test.go`, `40b49a7f`) is what keeps it that way. **It
used to carry 5**, silently zeroing `Skills` and `DenyTools` in transit.

## Facts worth knowing before you read any source here

These are documented in full on the pages above; they are collected here because
each one contradicts what the surrounding code looks like it does.

1. **The launch wire is hand-written and nothing but a test binds it to the Go struct.** `internal/core/agent.ManagedConfig` and proto `ManagedConfig` agree on 7 fields today; they disagreed on 2 until `40b49a7f`, and `Skills` + `DenyTools` reached **no** launched engine for as long as that lasted. The guard is now `internal/lm/grpc/arch_test.go` — a reflective sweep that names no field, so it covers fields added after it. → [wire](grpc-wire.md), [matrix §3](capability-matrix.md)
2. ~~**`wire.Hook.PreToolFallback` is always `false` on the engine side**~~ — **RESOLVED `40b49a7f`.** It is persisted, bundled, trust-hashed and now carried; no registered engine reads it at launch today, and it stays wired for whichever engine needs it next. → [wire](grpc-wire.md)
3. ~~**`ChatRequest.Runtime` does not cross the wire**~~ — **RESOLVED `40b49a7f`.** It used to mean a container-bound structured session ran the engine on the host while the session summary reported container isolation. Repairing it *activated* a path-confinement hole it had been masking, which is why confinement landed first (`73ea8d7f`). → [wire](grpc-wire.md)
4. ~~**An unprofiled backend's container inherits claude's credentials.**~~ — **RESOLVED `a6d9bd95`.** The `default:` arm of `engineContainerSpecFor` returned `resolveClaudeContainerAuth` for any unrecognized engine. It now fails closed, and `runtime: container-*` for an engine with no auth mapping is refused when the binding is *written*, not when it is launched. → [isolation](isolation.md)
5. **Isolating a shared cwd without a container requires `agent.OutOfCwd`.** claude-code's approaches declare it; a backend whose approaches lack it falls back to the loudly-warned well-known write, and concurrent per-agent isolation for it needs a worktree or a container cell. → [matrix §4](capability-matrix.md)
6. **No registered backend has a live transcript scraper.** claude-code's was deleted outright rather than demoted (its descriptor's `NoLegacyHistoryReason` says so), and a `nil` `History()` fails loudly at both consumers; canonical capture is written runner-side into `internal/adapters/transcript`. → [matrix §6](capability-matrix.md)

## Scope

Covered here: `internal/lm/backends`, `internal/engines/conformance`, `internal/lm/grpc`,
`internal/adapters/isolation`, `internal/engines/claude`, `internal/engines/mock`.

Types shared with the rest of the system — `agent.Backend`, `agent.ManagedConfig`,
`agent.PermissionMode`, `agent.SurfaceInputs`, `agent.CellKind` — live in
`internal/core/agent` and are documented here from the launch layer's point of
view.

These pages record **behavior**, not verdicts. Defect triage lives in `FINDINGS.md`;
where behavior diverges from what a doc comment or interface implies, the divergence
is stated as a fact with a `file:line` and nothing more.
