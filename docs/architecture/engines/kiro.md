# `kiro` — `internal/kiro`

ctxloom's adapter for AWS's Kiro CLI (`kiro-cli`). It launches the vendor binary
(interactive TUI passthrough, headless oneshot via `kiro-cli chat --no-interactive`),
**delegates structured chat to the generic ACP driver over `kiro-cli acp`** — kiro
speaks ACP natively, so there is no bespoke mapper and no adapter binary — and
materializes the native config surfaces kiro reads from a workspace.

It is the **strongest native-surfaces citizen** of the engines reviewed: it parses
no private engine internals at all. Every read is either a file ctxloom itself wrote
or a documented native surface.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `Kiro` | `backend.go:71` | Embeds `agent.LaunchBackend`; adds `effort`, `agentName`, `agentEngine` |
| `KiroConfig` | `backend.go:43` | mapstructure: `model`, `binary_path`, `args`, `env`, `effort`, `agent`, `agent_engine`, `thinking` |
| `KiroConfig.BackendType` | `backend.go:68` | `"kiro"` |
| `defaultAgentName` | `backend.go:39` | `"ctxloom"` — the materialized custom agent |
| `NewKiro` | `backend.go:79` | `BaseBackend("kiro","1.0.0")`, `BinaryPath="kiro-cli"`, `InitLaunch(lifecycle, &KiroCommands{}, ctxProvider, nil, &agent.CellDelivery{Build: BuildWellKnown(NewSurfaces), RawContext: true})` |
| `Configure` | `backend.go:100` | Applies CLI config + effort/agent/agent-engine; **warns** on `Thinking` (`:119-121`); **silently returns** on a wrong config type (`:103`) |
| `Execute` | `backend.go:125` | Pins `ModelInfo{Provider: "aws-bedrock"}` regardless of model family; delegates to `ExecuteCLI` |
| `buildArgs` | `backend.go:145` | The `kiro-cli chat` argv |
| `Chat` | `chat.go:19` | `agent.StructuredChat` → `acp.NewChatDriver(b.chatACPConfig())`; correctly does **not** `close(out)` |
| `chatACPConfig` | `chat.go:44` | `acp.ACPConfig{Command: BinaryPath + " acp", Agent, AgentEngine, Args, Env}` |
| `KiroWriter` | `settings.go:58` | `agent.SettingsWriter` + `agent.ContextWriter` |
| `NewWriter` | `settings.go:48` | Registry seam (`registry.go:366`) |
| `WriteSettings` / `WriteContext` / `RemoveSettings` / `Status` | `settings.go:178` / `:219` / `:289` / `:306` | Interface impl. `WriteSettings` is **production-dead** |
| `mapHooks` / `writeAgentConfig` / `reconcileSteering` / `writeSteering` / `mcpFile` | `settings.go:122` / `:193` / `:226` / `:244` / `:274` | Hook translation, agent JSON write, steering write, MCP binding |
| `kiroAgent` / `kiroHooks` / `kiroHook` | `settings.go:110` / `:95` / `:90` | `.kiro/agents/<name>.json` DTOs; four hook buckets `AgentSpawn`, `PreToolUse`, `PostToolUse`, `Stop` |
| `kiroSkillsGlob` | `settings.go:30` | `"skill://.kiro/skills/**/SKILL.md"` — the agent `resources` entry |
| `kiroShellMatcher` / `kiroFileEditMatcher` | `settings.go:34-35` | `"execute_bash"` / `"fs_write"` |
| `NewSurfaces` / `Surfaces` | `surfaces.go:219` / `:192` | `agent.SurfaceSet`: Context, MCP, Settings, Commands, Skills |
| `WriteCommandFiles` / `renderSkillFile` | `capabilities.go:36` / `:49` | Commands → `.kiro/skills/`; renderer seam |
| `WriteSkillFiles` | `skillfiles.go:30` | Skill packages → `.kiro/skills/<name>/…` |
| `MCPRegistrar` | `mcp_registrar.go:22` | Scope-aware (project + global) MCP registrar |
| `GlobalHome` / `ProjectHome` | `mcp_registrar.go:96` / `:102` | `$KIRO_HOME` else `~/.kiro` / `<workDir>/.kiro` — consumed by `internal/operations/hooks.go:327,331` for the `$HOME`-collision guard |

**Stubbed or absent:** `History()` is `nil` (`backend.go:92`). `SharedRealization`
returns `nil, false` always (`surfaces.go:299`). `KiroCommands.RegisterFromContent`
is stored and never called. `WriteSettings` is implemented but never called in
production.

## How it drives the engine

Two paths.

- **Direct CLI (`Execute`)**: `kiro-cli chat`, argv at `backend.go:145-204` — `[configured args…] chat [--agent <name>, skipped when SkipSetup] [--model <m>] [--effort <low|medium|high|xhigh|max>] [--agent-engine <v1|v2|v3>] [--trust-tools=… | --trust-all-tools] [--no-interactive (oneshot)] [<prompt positional>]`.
- **Structured chat**: **ACP native** — `agent.ACPTransport{Kind: agent.ACPNative}` (`registry.go:221`), command `"<BinaryPath> acp"` (`chat.go:47`), driven by the generic `internal/acp` driver. On this path `--model` is a **genuinely honored flag**: the self-report matches, and an unrecognized model produces a real mid-session RPC error — unlike claude-code-acp, which silently ignores it (`chat.go:27-34`).

## Capabilities

| Capability | Answer |
|---|---|
| Backend id | `"kiro"` (`registry.go:355`); binary `kiro-cli` |
| Permission tiers | plan **or** `SkipSetup` → `--trust-tools=fs_read`; acceptEdits → `--trust-tools=fs_read,fs_write`; bypass → `--trust-all-tools`; default → no flag (`backend.go:185-193`). `SkipSetup` wins over a requested bypass — a distillation run never widens (`backend.go:169-174`) |
| `EnforcesReadOnlyPlan` | **true** (`registry.go:377`), so `plan` survives end to end. **LIVE VERIFIED 2026-07-15, authenticated kiro-cli 2.12.1**: a headless sentinel-write probe under `--trust-tools=fs_read` left the target byte-unchanged and kiro-cli printed *"Command fs_write is rejected because it matches one or more rules on the denied list: - non-interactive mode (no user to approve)"*; `--trust-tools=fs_read,fs_write` and `--trust-all-tools` positive controls both let the identical write land (`registry.go:369-376`, `backend.go:175-184`) |
| Native per-tool deny list | **allowlist-shaped, not a deny list.** kiro's native lever is `--trust-tools=<tool,…>` — an allowlist over its real vocabulary (`fs_read`, `fs_write`, `execute_bash`); anything unlisted is refused by kiro itself. **ctxloom's cross-engine `deny_tools` is not wired for kiro** — `NewSurfaces` (`surfaces.go:219`) never reads it. Three fixed allowlist tiers only |
| Context surface | Native steering file `.kiro/steering/ctxloom-context.md` with front-matter `inclusion: always`, auto-loaded by kiro every session; no SessionStart hook, so the merge hash stays `""` (`settings.go:24-26`, `:244`; `surfaces.go:64`). `WithContextHook` on kiro **fails loudly** (`surfaces.go:286-293`). Live-verified: a real oneshot echoed a sentinel planted in the materialized steering context (`backend.go:8-16`) |
| MCP | **Both scopes.** Project `.kiro/settings/mcp.json` (ledger sidecar `.ctxloom-mcp-managed`, `settings.go:22`, `:274`; `surfaces.go:91`); global `$KIRO_HOME/settings/mcp.json` via `MCPRegistrar.ConfigPath` (`mcp_registrar.go:43-54`). The materialized agent JSON sets `IncludeMCPJSON: true` (`settings.go:197`) so the ctxloom agent picks the file up |
| Commands **and** skills | **One shared directory** — `.kiro/skills/<name>/SKILL.md` for both, auto-discovered via the agent's `resources` glob `skill://.kiro/skills/**/SKILL.md` (`settings.go:28-30`), invocable as a model-selected skill **and** as a `/<name>` slash command. kiro is the one engine where these two surfaces collide; resolved **skill-wins** by `agent.FilterCommandsClaimedBySkills` via `agent.NewSkillShapedCommandsAndSkills` (`surfaces.go:41-48`, `:219`) **before** the commands delivery is built |
| One-shot / resume | **No `driving: oneshot` support — fails loud.** kiro is absent from `resumeCapableBackends` (`spawner.go:225-228`), so `resolveResumeMode` rejects it before the release gate: *"driving: oneshot requires a resume-capable engine; backend %q has no resume-by-key primitive"* (`spawner.go:275-278`). Headless single-shot execution (`--no-interactive`) exists but captures no resume id; ACP `session/load` is not wired for kiro |
| Transcript | **None; scraper deleted outright.** `internal/kiro/session.go` was **deleted** in commit `6683bc4c` — not demoted to a vendor reader — because it parsed kiro's private v1 `sessions/cli/*.jsonl` store while a real `kiro-cli chat --no-interactive` oneshot (the exact mode `Execute` uses) persists to a structurally different **v2 SQLite blob** at `$XDG_DATA_HOME/kiro-cli/data.sqlite3` (tables `auth_kv`, `state`, `conversations_v2`), so it returned empty forever without erroring (`backend.go:18-26`). A `nil` history now fails loudly at both consumers |
| Model + auth | `--model` on both paths; **honor is live-verified two ways** (`backend.go:11-16`) — the self-report matches across every listed model, and an unrecognized value is rejected before any chat runs ("Model '<x>' does not exist"). Extra native knobs `--effort` and `--agent-engine` are **not validated** in-package (the vendor is the authority). Auth is **ambient**: `kiro-cli login` (GitHub OAuth), or `KIRO_API_KEY` in the inherited env for headless. The adapter sets no auth env |
| Status | **Experimental**, with a registry `LIVE-UNTESTED` banner (`registry.go:352-354`) — **but that banner may itself be stale**: the package doc (`backend.go:8-16`) records later live verification against authenticated kiro-cli 2.12.1. Not exercised live: preToolUse/postToolUse actually firing, and `mcp.json` tolerance |

## Isolation — the credential gap

**`KIRO_HOME` relocates config and session state, not credentials.**
`credentialSeedSpecs["kiro"]` (`internal/lm/isolation/auth.go:705-715`) declares
`envTrigger: "KIRO_API_KEY"`, `HomeVars: [{KIRO_HOME, Subdir "kiro"}, {XDG_DATA_HOME,
Subdir "xdg-data", GatedOnCreds: true}]`, and **`HonoursVarForCreds: false`** — there
is no per-agent credential file to copy.

Live-verified against kiro-cli 2.12.1 (`auth.go:162-183`, `:626-640`): subscription
credentials do not live under `~/.kiro` at all. They live in
`$XDG_DATA_HOME/kiro-cli/data.sqlite3`, a **global SQLite database** that also holds
the v2 session store. Bind-mounting a live SQLite another `kiro-cli` may hold open is
rejected as a mount strategy.

`GatedOnCreds` behavior: `seedCredentials` includes `XDG_DATA_HOME` in `Env()` **only
when `KIRO_API_KEY` is set** (live-verified: a fresh `XDG_DATA_HOME` plus
`KIRO_API_KEY` authenticates headlessly with no browser). Otherwise it omits the var
and records a `ClassIsolation` fail-loud finding — converting kiro's previously
*silent* shared-sqlite non-isolation into either real per-agent isolation or a loud,
degradable error. `KIRO_HOME` (sessions only) stays unconditional.

**Container** (`internal/lm/isolation/profile.go:418-445`): image
`ctxloom-agent-kiro:latest` (no official image), official installer with a
**two-step gate** — `kiro-cli --version` **and**
`nativeACPRunGate("kiro-cli","acp")`, proving the surface every structured run spawns
actually loads. Auth resolver `resolveKiroContainerAuth` (`auth.go:183`) treats
`KIRO_API_KEY` as the sole trigger (`AWS_*` ride along when present but are not
standalone). `overlayDirs = kiroOverlayDirs`; `transcriptStoreRel = ".kiro"`.

**Known uncovered gap** (`profile.go:436-444`): the `.kiro` mount does **not** capture
a `kiro-cli chat --no-interactive` oneshot's session state — that persists to
`$XDG_DATA_HOME/kiro-cli/data.sqlite3`, entirely outside `containerHome/.kiro`.

Net: **a containerized kiro with only a subscription login cannot authenticate**; it
needs `KIRO_API_KEY`.

## Invariants

1. **kiro-cli resolves the materialized workspace `.kiro/agents/<name>.json` over any global `~/.kiro/agents` copy** — its own "Agent conflict … Using workspace version" precedence, confirmed via `kiro-cli agent list` (`settings.go:38-42`).
2. **Skills are discovered via the agent's `resources` glob**, so a nested `<name>/SKILL.md` must be reachable through `**` (`settings.go:30`).
3. **Hook buckets are exactly four** — `agentSpawn`, `preToolUse`, `postToolUse`, `stop` (`settings.go:95-100`); tool matchers are `execute_bash` (shell) and `fs_write` (file edit) (`settings.go:34-35`).
4. **`kiroHooks.empty()` means omit the whole hooks block** (`kiroAgent.Hooks` is a pointer with `omitempty`, `settings.go:102`, `:200`).
5. **Two undocumented sentinels inside `KiroWriter`**: `hash == ""` means no context hook (`settings.go:228`); `content == ""` means remove the file (`settings.go:249`).
6. **`writeAgentConfig` can never produce zero bytes** — `Name` and `IncludeMCPJSON` have no `omitempty`, so `CanonicalJSON` always emits at least `{"includeMcpJson":…,"name":"ctxloom"}\n`. `writeSteering` likewise always prepends the 30-byte `---\ninclusion: always\n---\n\n` front-matter (`settings.go:261`).
7. **The ACP driver owns `close(out)`** — `Kiro.Chat` must not close it (`chat.go:19-22`). kiro is `ACPNative`, so there is no `RequireOnHost` early return.
8. **`Chat` is a full-setup path** — Setup wrote the `.kiro/` config the spawned agent reads from cwd, including the `--agent`-selected agent (`chat.go:15-18`).
9. **`GlobalHome`/`ProjectHome` must stay exported** for `internal/operations/hooks.go:319-331`'s `$HOME`-collision guard.

## Divergences from documented or implied behavior

- **The `agent:` config key produces a broken launch.** `buildArgs` emits `--agent <b.agentName>` (`backend.go:153-155`) and `chatACPConfig` sets `Agent: b.agentName` (`chat.go:47`), but `agentPath` (`settings.go:73`) and `writeAgentConfig` (`settings.go:195`) both **hardcode `defaultAgentName`**, and no `KiroWriter` construction path carries the agent name. kiro-cli is told to select an agent that was never materialized. The doc at `backend.go:38` asserts the opposite; `backend_test.go:38` asserts the in-memory field, never the written filename.
- **`effort:` is silently dropped on the ACP path.** `buildArgs` emits `--effort` (`backend.go:160`) but `chatACPConfig` never reads `b.effort` (`chat.go:44-52`), so the same config yields different reasoning behavior depending on oneshot-CLI vs ACP chat, with no diagnostic. This is inconsistent with `Thinking`, which is also unwired but *does* warn.
- **Missing context file silently removes the steering file.** `agent.ReadContextFile` returns `("", nil)` for a missing file (`internal/shared/agent/contextfile.go:174`) → `writeSteering(dir, "")` → the steering file is removed and `nil` returned (`settings.go:230-234`). On a genuine read error the code warns and then sets `content = ""` **anyway** — the same removal. On the live path there is not even a warning, because `contextSurface.Deliver` (`surfaces.go:65`) routes through `DeliverManagedContext`, which **discards the `ContextReport`** (`internal/shared/agent/managedcontext.go:121`), so `Removed` vs `Wrote` is never inspected.
- **Empty-prompt oneshot**: `--no-interactive` is appended unconditionally on `ModeOneshot` while the prompt is appended only `if prompt != ""` (`backend.go:196-201`), producing `kiro-cli chat --no-interactive` with no INPUT positional. `RunNonInteractive` passes `nil` stdin, so it either blocks or errors vendor-side. Nothing rejects it.
- **A stale and overclaiming isolation comment** at `mcp_registrar.go:14-16` says `KIRO_HOME` is "the same override the session-history reader and the worktree isolation policy use to relocate kiro's global state". The reader was deleted in `6683bc4c` — the same file says so 58 lines later at `:72-73` — and "relocate global state" is false for credentials (`internal/lm/isolation/auth.go:537`, `:627`, `HonoursVarForCreds: false`). The *code* is correct; only the comment misleads.
- **`KiroWriter.WriteSettings` has no production call site** (`settings.go:178`) — tests and the conformance suite only. The cross-backend settings write rides the surfaces × cells seam; generic dispatch reaches only `RemoveSettings` and `Status`. Cascade: `reconcileSteering`'s hash arm and `mapHooks`' `contextHash` return are both production-unreachable.
- **`Status` swallows two parse errors** (`settings.go:310-315`), so a corrupt or unreadable `.kiro/agents/ctxloom.json` reports as "not wired" rather than "broken" — the same silent-empty-parse class that killed the deleted session reader.
- **`Configure` silently ignores a wrong config type** (`backend.go:100-104`), dropping every override with no diagnostic.
- **`MCPRegistrar.Present` collapses three conditions to `false`** — kiro absent, `$HOME` unresolvable (`mcp_registrar.go:35`), and `os.Stat` failing including EACCES. `RemoveSettings` drops `afero.Exists`'s error the same way (`settings.go:292`).
- **`KiroCommands.RegisterFromContent` and the whole type are dead** (`capabilities.go:27`) — six implementations across the fleet, zero invocations.
- **`registry.go:352` cites `kiro.filterClaimedCommands`, a function that does not exist** — the logic moved to `agent.FilterCommandsClaimedBySkills`.
- **`RenderCommandAsSkillFile` slash-sanitizes the front-matter `name` but returns an unsanitized relPath** (`internal/shared/agent/skillcommandshape.go:42` vs `:53`), so a command named `go/review` writes `.kiro/skills/go/review/SKILL.md` while declaring `name: go-review`. Not kiro's defect (`renderSkillFile` is a pure wrapper), but it lands in kiro's directory; `capabilities_test.go:45` exercises exactly that case.
- **`deliveredFunc` is defined six times across the fleet** (`surfaces.go:181` here) while `agent.DeliveredFunc` is already exported at `internal/shared/agent/managedcontext.go:107`.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Plugin wire](grpc-wire.md) · [Isolation](isolation.md)
