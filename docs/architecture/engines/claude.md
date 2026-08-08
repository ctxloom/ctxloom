# `claude-code` — `internal/claude`

ctxloom's adapter for Anthropic's `claude` CLI. It declares the CLI's process
contract (argv, flags, probes, env), builds its argv, materializes claude's native
on-disk surfaces (`.claude/settings.json`, `.mcp.json`, `CLAUDE.md`,
`.claude/commands/`, `.claude/skills/`), and drives structured chat through the
`claude-code-acp` ACP adapter. It owns the mapping from ctxloom's generalized
posture — permission tier, context, MCP set, hooks, commands, skills, deny-tools —
onto claude's **own documented surfaces**, never onto private internals.

It is **the exercised default engine** and the only backend with a
`SharedRealization`: out-of-cwd scratch files converted into
`--append-system-prompt-file` / `--mcp-config` / `--settings`, so a live shared cwd
is never written into.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `ClaudeCode` | `claudecode.go:38` | The launch backend; embeds `agent.LaunchBackend`. Fields `surfaces Surfaces`, `thinking agent.ThinkingLevel` |
| `NewClaudeCode` | `claudecode.go:54` | Constructor: `BinaryPath="claude"`, `NewBaseBackend("claude-code","1.0.0")`, `InitLaunch(lifecycle, &ClaudeCommands{}, ctxProvider, nil /*SessionHistory*/, &agent.CellDelivery{Build: b.buildSurfaces})`, `SetACPTransport(ClaudeACPTransport)` |
| `ClaudeConfig` | `claudecode.go:18` | Typed decode target. `BinaryPath`/`Args`/`Env`/`Thinking` are live; `Model` is decoded and never read |
| `ClaudeConfig.BackendType` | `claudecode.go:33` | `"claude-code"` |
| `Configure` | `claudecode.go:96` | `agent.Configurable`: binary/args/env + thinking level |
| `Execute` | `claudecode.go:114` | Minimal-oneshot JSON branch, else `ExecuteCLI` |
| `buildArgs` | `claudecode.go:231` | The whole claude argv |
| `Chat` | `chat.go:53` | `agent.StructuredChat` via `acp.NewChatDriver` |
| `ResolveModel` | `chat.go:254` | Nickname → concrete model id; `ok=false` fails loud. Sole production caller `internal/operations/delegate.go:317` |
| `ClaudeACPTransport` | `chat.go:28` | `{Kind: ACPAdapter, Binary: ClaudeACPAdapter, InstallCmd: "npm install -g @zed-industries/claude-code-acp", Publisher: "Zed Industries"}` |
| `EngineCLIs` / `ClaudeEngineCLIs` | `enginecli.go:172` / `:178` | Oneshot + interactive surface declarations |
| `ClaudeCodeHookWriter` | `claude.go:26` | `agent.SettingsWriter` + `agent.ContextWriter` |
| `NewWriter` | `claude.go:20` | Registry `newWriter` seam (`registry.go:276`) |
| `WriteSettings` / `RemoveSettings` / `Status` | `claude.go:161` / `:744` / `:798` | The `SettingsWriter` trio. `WriteSettings` has **zero production callers for claude** — live only via the conformance suite |
| `WriteContext` | `claude.go:243` | Marker-merge into `CLAUDE.md` |
| `ProjectSettingsPath` / `GlobalSettingsPath` / `GlobalCommandsDir` / `SettingsPath` / `MCPConfigPath` | `claude.go:48` / `:54` / `:67` / `:76` / `:83` | Path vocabulary consumed by `internal/operations/hooks.go:272,277` and `internal/ltk/engine/claudecode.go:151,153` |
| `MCPRegistrar` | `mcp_registrar.go:15` | `agent.MCPRegistrar` for taskloom |
| `WriteCommandFiles` / `TransformToClaudeCommand` | `commandfiles.go:18` / `:44` | `.claude/commands/*.md` manifest write + renderer |
| `WriteSkillFiles` | `skillfiles.go:21` | `.claude/skills/<name>/**` manifest write |
| `Surfaces` / `NewSurfaces` / `SurfaceInputs` | `surfaces.go:280` / `:298` / `:249` | claude's `agent.SurfaceSet`. It is the **only** backend that keeps a local copy of `agent.SurfaceInputs` |
| `HookPayload` / `HookOutput` / `DecodeHookPayload` / `EncodeDeny` | `hooks_wire.go:33` / `:103` / `:110` | The hook wire contract `internal/ltk/engine` and `internal/cli` import rather than redefine |

**Stubbed or absent:** `SessionHistory` is `nil` (`claudecode.go:67`). There is no
`Setup` override — the shared `LaunchBackend` path is used.

## How it drives the engine

Two native CLI surfaces plus an ACP adapter for structured chat.

- **Oneshot**: `claude --print`, **prompt on stdin** (`agent.PromptStdin`, `enginecli.go:182`; `promptStdin`, `claudecode.go:350`). Argv delivery was moved to stdin after it hit `E2BIG` on `ctxloom weave`.
- **Interactive**: prompt as a trailing argv positional (`enginecli.go:194`; `claudecode.go:338-342`), plus `--name <harp>` from `CTXLOOM_SESSION_HARP` (`claudecode.go:223`, `:273`) — interactive only, since `/rename` cannot be injected.
- **SkipSetup / distill argv** (`claudecode.go:314-330`): `--output-format json --tools "" --disable-slash-commands --no-session-persistence --strict-mcp-config --system-prompt "" --settings <inline JSON>`.
- **Structured chat**: `claude-code-acp` (Zed Industries), located on PATH and **never installed by ctxloom** — absence produces an install hint (`chat.go:28-34`). The retired stream-json path left no code residue.

The declared flag vocabulary (`enginecli.go:79-95`) is 15 flags, all verified
against installed `claude 2.1.220`: `--dangerously-skip-permissions`,
`--permission-mode`, `--disallowedTools`, `--model`, `--name`, `--print`,
`--append-system-prompt-file`, `--mcp-config`, `--settings`, `--output-format`,
`--tools`, `--disable-slash-commands`, `--no-session-persistence`,
`--strict-mcp-config`, `--system-prompt`.

## Capabilities

| Capability | Answer |
|---|---|
| Backend id | `"claude-code"` (`registry.go:266`); binary `claude` |
| Permission tiers | bypass → `--dangerously-skip-permissions`; acceptEdits → `--permission-mode acceptEdits`; plan → `--permission-mode plan` **plus** `--disallowedTools "Bash,Edit,Write,NotebookEdit"`; default → no flag (`claudecode.go:253-258`) |
| `EnforcesReadOnlyPlan` | **true** (`registry.go:297`), so `plan` is **not** collapsed. LIVE VERIFIED 2026-07-15 against authenticated claude 2.1.210: plan + deny list denied a sentinel-file overwrite (`claudecode.go:237-247`) |
| Native per-tool deny list | **yes — the only engine with one.** (a) the fixed plan-tier `--disallowedTools` token; (b) configurable `deny_tools` unioned into `permissions.deny` in `.claude/settings.json` via `mergeDenyTools` (`claude.go:536`), monotonic union only |
| Context surface | Isolated cell → marker-merge into `CLAUDE.md` (`surfaces.go:81` → `WriteContext`, `claude.go:243`). Shared cell → out-of-cwd `<hash>.sysprompt.md` (`contextdelivery.go:50`) pointed at by `--append-system-prompt-file` (`claudecode.go:294-299`). **claude does not read `AGENTS.md`** — deliberate (`enginecli.go:34-38`) |
| MCP | Project `.mcp.json` (`writeMCPConfig`, `claude.go:460`; `mcpSurface`, `surfaces.go:105`). In a shared cell it is an out-of-cwd file passed as `--mcp-config` **without** `--strict-mcp-config`, so ctxloom's servers **layer over** the user's project `.mcp.json` (`claudecode.go:288-292`). Global via `MCPRegistrar.ConfigPath` → `~/.claude.json` |
| Commands | `.claude/commands/*.md`, frontmatter + mustache→`$N` body (`commandfiles.go:18`, `:44`); optional home dedup against `~/.claude/commands` (`surfacedelivery.go:99-104`) |
| Skills | `.claude/skills/<name>/**` (`skillfiles.go:21`) |
| One-shot / resume | **Supported.** In both `resumeCapableBackends` and `oneShotSupportedBackends` (`internal/agentcoord/coord/spawner.go:225`, `:248`). Resume-id capture lives in `internal/acp`, not here; this adapter's only session-identity lever is `--name <harp>` (display name only) |
| Transcript | **No scrape.** `SessionHistory` is `nil`; the `~/.claude/projects/<encoded-cwd>/*.jsonl` scraper was deleted (`capabilities.go:17-27`) after its cwd→slug encoder produced non-existent dirs for any path with a dot, underscore, or space. An opt-in vendor importer exists for the interactive-pty gap (`internal/operations/vendorreader.go:71`) |
| Model + auth | `--model` emitted when non-empty; empty lets the CLI pick (`claudecode.go:263-266`). Auth is **ambient subscription** by default; `chatACPConfig` (`chat.go:207`) declares `ModelEnvVar` and strips `CLAUDECODE`. Thinking rides `MAX_THINKING_TOKENS` (`chat.go:110`, `:129`) |
| Isolation | **Supported, no auth gap.** Scoped host env passthrough plus a **copy-then-mount-read-write** of `~/.claude/.credentials.json` — RW because claude refreshes its OAuth token in place (`internal/lm/isolation/auth.go:423-468`). `~/.claude.json` is deliberately not copied. Additionally, claude is the one engine that can isolate a *shared* cwd without a container, via the out-of-cwd flag trio |
| Status | **Supported — the exercised default** |

## Invariants

1. **`Setup` must run `buildSurfaces` before `buildArgs` reads `b.surfaces.*.Path()`** — connascence of execution order. The zero value is nil-safe but yields a *silently flagless* argv, not an error (`claudecode.go:38`).
2. **`SurfaceFor` must return the same instance** for `ApproachUnsafeFile` and `ApproachSystemPrompt`, so `buildArgs`' later `Path()` read observes the write (`internal/shared/agent/cells.go:124-127`).
3. **`Path() == ""` means "emit no flag"** — the seam between delivery and argv (`claudecode.go:296`, `:301`, `:306`).
4. **Ownership is marked by the `"ctxloom"` executable token** via `agent.IsManaged(cmd, "ctxloom")`, repeated at six call sites and deliberately verb-agnostic.
5. **`loadSettings` and `saveSettings` must mirror each other key-for-key** — the round-trip is what preserves foreign keys (`claude.go:259`, `:364`).
6. **`claudeCodeHook.SCM` is `json:"-"`** because claude validates settings against a strict Zod schema (`claude.go:149`).
7. **Writes are marker-merged or manifest-scoped, never whole-file overwrites.**
8. **`agent.CanonicalJSON` always emits at least `{}\n`**, so claude's two `AtomicWriteFile` callers cannot write zero bytes — safe by accident of the JSON encoder, not by a guard.
9. **`--mcp-config` is used without `--strict-mcp-config` on the launch path**, so ctxloom layers rather than replaces.
10. **The ACP producer closes `out` exactly once**: `RequireOnHost`'s error is returned *after* `close(out)` (`chat.go:53`).

## Divergences from documented or implied behavior

- **`buildArgs` can emit a variadic flag as the last token before the trailing prompt positional, with no `--` terminator** (`claudecode.go:258`, `:300-309`, `:338-342`), so claude's parser can swallow the prompt as flag values. Today's safety is incidental — `--model`/`--name` happen to follow. `agent.CLIFlag.Value` has no `ValueVariadic`, so the anti-drift test structurally cannot catch it.
- **The minimal-oneshot path (distill/compaction) returns `ExitCode 0, err nil` having written zero bytes** — both write errors are discarded with `_, _ =` (`claudecode.go:132-150`).
- **A malformed `permissions` block is warned about and then deleted from the user's file** — `delete(raw, "permissions")` runs unconditionally, outside the `else` (`claude.go:311-330`), destroying user `allow`/`ask`/`defaultMode`/`additionalDirectories`. No corrupt-file backup on this path.
- **The field doc promises to preserve "legacy mcpServers for backwards compat" while the code does `delete(raw, "mcpServers")`** with no migration anywhere (`claude.go:100` vs `:333`). It also fires on the uninstall path.
- **An unparseable `.mcp.json` becomes an empty config, and `writeMCPConfig` then saves a file containing only ctxloom's servers** (`claude.go:434-438`) — asymmetric with `loadSettings`, which was hardened for exactly this.
- **Empty assembled context produces no file, no flag, and no warning** (`contextdelivery.go:50-55`; `claudecode.go:294-299`); nothing distinguishes "legitimately no context" from "assembly bug".
- **`ClaudeCommands` is constructed and passed to `InitLaunch` but never dispatched** — `LaunchBackend.commands` is write-only (`internal/shared/agent/launch_backend.go:92`). Real command writes go via `commandsSurface`.
- **`agentfiles.go` — the entire sub-agent-roster writer (`ClaudeAgents`, `AgentExport`, `WriteAgentFiles`, `TransformToClaudeAgent`) plus a 219-line test suite — has zero production callers**; `enginecli.go:149` states it outright. It predates claude's own native `--agents <json>` flag.
- **`claude.SurfaceInputs` duplicates `agent.SurfaceInputs` and the two hand-written mappers have already diverged** — `registry.go:282-293` omits the `MCPCommandOverride` that `claudecode.go:79-91` sets (`surfaces.go:249`). Inert today, unenforced.
- **`SharedRealization` keys on `SurfaceKind` alone, discarding the approach** (`surfaces.go:388`), so a shared-cwd context delivery under `ApproachHook` would run `DeliverIsolated` instead of the documented no-op, producing double context. Latent, not live.
- **`SelfContainedSkills` is set by three call sites and read by none** (`surfaces.go:265`).
- **Three mutually-exclusive optional fields on the delivery types cannot be set by the constructor**, producing six construct-then-assign rituals (`surfacedelivery.go:23-55`; `surfaces.go:116-217`). Omitting one is compile-clean and silently wrong: the container MCP command reverts to the host path, or `deny_tools` silently stops applying.
- **On a failed `DeliverIsolated`, `s.path` retains its prior value**, so `Path()` can report a path for a delivery that did not happen (`surfaces.go:131-136`, `:182-187`).
- **A `minimalSettings` marshal failure returns `"{}"`, dropping `permissions.defaultMode: bypassPermissions`** — the setting that keeps a headless distill run from blocking (`claudecode.go:375-378`).
- **Four `exists, _ := afero.Exists(...)` sites treat an I/O error as "absent"** (`claude.go:759`, `:781`, `:803`, `:814`; `commandfiles.go:24`), so a permission-denied `settings.json` makes `RemoveSettings` a silent no-op and `Status` report "not installed".
- **The one departure from "native surfaces only"**: ACP model switching drives the unstable, undocumented `session/set_model` JSON-RPC method found by grepping `claude-code-acp`'s `dist/*.js`, pinned to adapter version `0.16.2` (`chat.go:143-187`). It is version-gated with a documented removal condition.
- **`internal/claude/docs/design/*.md` carries 357 lines describing deleted symbols** (`chat_stream.go`, `chat_run.go`, `ClaudeSessionHistory.parseEntries`) and the unwired `agentfiles.go`.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Plugin wire](grpc-wire.md) · [Isolation](isolation.md)
