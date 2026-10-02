# Engine capability matrix

The fastest way to answer "does the engine actually support Y?". The `Backend`
abstraction is uniform; **what an engine behind it can carry is not**, and a few
capabilities are wired host-side without any engine honouring them. Every cell
below is what the code **does**, with a `file:line`.

Composed engine ids are what `operations.EngineNames()` returns: `claude-code`
(`config.BackendClaudeCode`) and the test doubles (`config.BackendMock` and its
`config.BackendMock*` siblings) — all in one `init()` in
`internal/engines` (the composition root). `cmd/mockengine` is **not** a
registered backend; it is a fake vendor CLI (see [mockengine](mockengine.md)).
Where a row below says "the doubles", the mock family behaves alike unless the
cell says otherwise.

## 1. How the engine is driven

| | claude-code |
|---|---|
| Binary | `claude` |
| Oneshot subcommand | none (`claude --print`) |
| Prompt channel | **stdin** (oneshot); trailing positional (interactive) |
| Prompt-channel decl | `internal/engines/claude/enginecli.go:182`,`:194` |
| Session name at launch | `--name <harp>` (interactive only) |

`agent.EngineCLI` is the single declaration of a vendor's flags, prompt channel
and probe set; the mock engine reads the same declaration rather than carrying
its own copy, which is what keeps a fake in step with the driver.

## 2. Permissions — what an engine's posture becomes

Each engine owns its permission model (`Engine.Permissions`): its modes, how
it resolves and validates them, what it can enforce, and how the result maps
to its own mechanism. Core carries the resolved posture opaquely. claude-code
maps it so (`internal/engines/claude`, `postureOf` and its callers):

| Declared | claude-code |
|---|---|
| mode `default` | no flag |
| mode `acceptEdits` | `--permission-mode acceptEdits` |
| mode `plan` | `--permission-mode plan` **+** `--disallowedTools "Bash,Edit,Write,NotebookEdit"` (plan-first, with `after_plan`, drops the belt) |
| mode `bypass` | `--dangerously-skip-permissions` |
| `approver: none` | mode `dontAsk` (with mode `default`) |
| `approver: reviewer` | mode `auto` (with mode `default`) |
| `sandbox: workspace-write` | a `sandbox` settings member: enabled, `failIfUnavailable`, no unsandboxed escape, unlisted hosts denied |

A pairing claude has no mode for, a sandbox it cannot enforce where the run
executes, and `network: true` under its sandbox are refused before anything
starts. A headless run carries its mode on each turn's `--settings`.

One further permission fact:

- A headless **structured** run launches at whatever posture resolved and cannot hang on an engine approval prompt: claude is told nobody answers (`--permission-prompts none`), so it denies what the posture leaves open, and the runner reports that turn BLOCKED to the parent (`agent.ChatEvent.Denied`, `agent.TurnMeta.Denials`).

## 3. Native per-tool deny list

| Backend | Native per-tool deny list? | Mechanism |
|---|---|---|
| `claude-code` | **yes** | (a) fixed plan-tier `--disallowedTools "Bash,Edit,Write,NotebookEdit"` (`claudecode.go:258`); (b) configurable `deny_tools` unioned into `permissions.deny` in `.claude/settings.json` — `SurfaceInputs.DenyTools` → `internal/engines/claude/surfaces.go:271` → `surfacedelivery.go:47` → `mergeDenyTools` (`internal/engines/claude/claude.go:536`), monotonic union only |
| the doubles | **no** | there are no tools; the double executes nothing |

### The deny-list reality check

**`ManagedConfig.DenyTools` reaches the launch path.** The runner decodes the
same `launch.Launch` the originator resolved (see
[transport](../agentcoord/transport.md)).

All three delivery paths now carry it:

| Path | Carries `DenyTools`? | Carries `Skills`? | Site |
|---|---|---|---|
| `ctxloom apply-hooks` | yes | **no** | `internal/adapters/operations/hooks.go:452` |
| `ctxloom profile materialize` | yes | yes | `internal/adapters/operations/profile_materialize.go:129`, `:131` |

**Whether the engine then *honours* it is a separate question** — the per-engine
table above is the one that answers it. An engine whose surface constructors
never read `in.DenyTools` accepts the field at the seam and drops it without a
warning; the wire carrying the field does not give an engine a capability it
never had.

## 4. Context surface — how assembled context actually reaches the engine

| Backend | Mechanism | Reads `AGENTS.md`? | Hook-mediated? | Site |
|---|---|---|---|---|
| `claude-code` | **two realizations of one surface**: isolated cell → marker-merge into `CLAUDE.md`; shared cell → out-of-cwd `<hash>.sysprompt.md` passed as `--append-system-prompt-file` | **no — deliberate** (`enginecli.go:34-38`) | no (apply path uses a SessionStart injection hook) | `internal/engines/claude/surfaces.go:81`, `contextdelivery.go:50`, `claudecode.go:294-299` |
| the doubles | a single project-root file (`mockContextPath`) whose bytes the mock engine hashes and reports | no | no | `internal/engines/mock/surfaces.go` |

**`agent.OutOfCwd` — the out-of-cwd form.** `claude-code`'s approaches carry
one (`internal/engines/claude/surfaces.go`): flag-pointed scratch files for context,
MCP and settings, so a live shared cwd is never written into. An engine whose
approaches lack it gets the loudly-warned well-known write on a shared cell.
**Consequence: for such an engine, concurrent per-agent isolation requires a
private cwd (worktree) or a container cell.**

## 5. MCP, commands and skills

| Backend | MCP file | MCP scopes | Commands dir | Skills dir |
|---|---|---|---|---|
| `claude-code` | `.mcp.json` (+ out-of-cwd via `--mcp-config`, **without** `--strict-mcp-config`, so ctxloom's servers **layer over** the user's) | project + global (`~/.claude.json`) | `.claude/commands/*.md` | `.claude/skills/<n>/**` |
| the doubles | — | — | — | `.mock/skills/<n>/**` (`mockSkillsPath`), except `config.BackendMockNoSkills`, which declares no skills mapper at all |

**Skills cross the launch wire** (§3). A descriptor's `skillExports` maps them
into `SurfaceInputs.Skills`; `Engine.Exports` reports which engines
declare one, and `config.BackendMockNoSkills` exists so the "no skills surface"
arm of every caller has a subject.

## 5b. Hooks — declaring what an engine cannot carry

`claude-code` carries ctxloom's unified hooks into its native settings surface:

| Backend | Hooks land in | Routed by |
|---|---|---|
| `claude-code` | `.claude/settings.json` | `internal/engines/claude/claude.go:680` |
| the doubles | `.mock/settings.json` (`NewMockSettingsWriter`) | not routed — the unified `HooksConfig` is marshalled whole under `mockSettingsHooksKey` |

A descriptor declares what it *cannot* carry in one of two fields:
`noHooksReason` (the engine has no hook mechanism at all) and
`unsupportedHookKinds` (specific unified events with no native equivalent —
`config.BackendMockLossy` models this with two events, each naming its own
reason). The loss itself is structural and fine to have; the **silence** was not.
A materialize used to print only true `wrote` lines with the dropped hook
nowhere among them, so a team could ship a guardrail and a deskmate could
inherit the profile without either being told the guardrail did not come with
it (`whiny-exclusive`).

The gap is now DECLARED and reported: `operations.CapabilityLoss` (over `Definition.HookLosses`) turns a
declaration into an `agent.SurfaceLoss` whenever the run actually carries
hooks, materialize puts it in `MaterializeProfileResult.NotCarried` (so
`--format json` sees it as data) and prints it beside the `wrote` lines, and
`doctor`, `manage check` and `agent show` read the same predicate through
`operations.CapabilityLoss`.

A capability gap nobody asked to use stays quiet — a profile declaring no hooks
gets no line, the same rule `agent.RouteUnifiedHooks` applies one level down for
a single unsupported event. `TestDeliveryApproach_HookCarriageMatchesDeclaration`
(`tests/integration`) holds the declaration against the delivered payload for
every registered backend, so it cannot drift into claiming a loss that isn't real
or missing one that is.

**Still silent elsewhere:** `ctxloom run` and `manage hooks install` deliver to
an engine with a declared loss and say nothing — neither calls
`UncarriedSurfaces`.

## 6. Session history and transcripts

| Backend | `History()` | Mechanism | Note |
|---|---|---|---|
| `claude-code` | **nil** | scraper **deleted** | its cwd→slug encoder produced non-existent dirs for any path with a dot/underscore/space |
| the doubles | `NilSessionHistory` — non-nil, holds nothing | the double keeps no transcript store | — |

A `nil` history **fails loudly** at its consumer
(`internal/adapters/operations/sessionfeed.go`). Canonical capture is written runner-side
into `internal/adapters/transcript`'s canonical JSONL; each engine declares its own
vendor reader on its descriptor (`engine.Descriptor.TranscriptReaders`), and
`internal/adapters/operations/vendorreader.go` reads that declaration for the
interactive-pty gap.

## 7. One-shot driving and resume

Two gates in `internal/core/coord/spawner.go`, `resumeCapableBackends`
and `oneShotSupportedBackends`; both name `claude-code` alone.

`driving: oneshot` on a backend outside their intersection **fails loud** rather
than silently degrading — `resolveResumeMode` refuses at the resume gate, and
`prodSpawner.Resolve` refuses at the oneshot gate.

## 8. Isolation support

Full detail in [isolation](isolation.md). Summary:

| Backend | Host + worktree lever | Container image | Container auth | Gap |
|---|---|---|---|---|
| `claude-code` | `CLAUDE_CONFIG_DIR` | `ctxloom-agent:latest` | the agent's `auth:` mode's `engine.Credentials`: `Env` forwarded, each of `Stores` mounted under the container `$HOME` with its var blanked, each of `FileVars` bound read-only on its own with the var rewritten (`containerRelocator`); `login` refused where its store is the macOS Keychain | none |
| the doubles | none needed — mock's descriptor declares `Home` absent (a bare echo that never touches disk), a NAMED exemption; a double that declared nothing would be refused at registration | `ctxloom-agent:latest`, installing no vendor CLI (its descriptor's install fragment asserts `cat` only) | a `Vendorless` auth declaration — the one plan that never fails to resolve | none |

`composableEngines()` (`internal/adapters/isolation/enginespec.go`) names the engines
with a container install fragment; an engine absent from
`engineContainerSpecFor`'s declarations gets the default arm, which is
undeclared and **fails closed** at the container gate.

## 9. Support status

| Backend | Status | Source |
|---|---|---|
| `claude-code` | **supported — the exercised default** | `website/src/content/docs/concepts/architecture.md` |
| the doubles | test-only (`agentDescriptor.testOnly`) | `registry.go` |

## 10. Capabilities that exist on every engine and fire on none

- **`wire.Hook.PreToolFallback`** — carried to the engine, read by no registered engine at launch.
- **`agent.ExecuteRequest.Temperature`** — declared, set by nothing, read by no backend.

## See also

- [The `Backend` abstraction and registry](backend-abstraction.md)
- [Transport](../agentcoord/transport.md)
- [Isolation](isolation.md)
- Per-engine pages: [claude](claude.md) · [mockengine](mockengine.md)
