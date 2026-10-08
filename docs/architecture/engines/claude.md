# `claude-code` — `internal/engines/claude`

ctxloom's adapter for Anthropic's `claude` CLI. It declares the CLI's process
contract (argv, flags, probes, env), builds its argv, materializes claude's native
on-disk surfaces (`.claude/settings.json`, `.mcp.json`, `CLAUDE.md`,
`.claude/commands/`, `.claude/skills/`), and drives structured chat through the
vendor CLI. It owns the mapping from ctxloom's generalized
posture — permission tier, context, MCP set, hooks, commands, skills, deny-tools —
onto claude's **own documented surfaces**, never onto private internals.

It is **the exercised default engine**, and the engine whose approaches carry
an out-of-cwd form (`agent.OutOfCwd`): on a shared-cwd launch, context, MCP
and settings land beneath the advised Scratch root and are announced to the
CLI by launch flag, so a live shared cwd is never written into. Its
`Declaration` (`Surfaces`, `surfaces.go`) is authored in this package and is
the one place claude's surface membership is stated.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `ClaudeCode` | `claudecode.go` | The launch backend; embeds `agent.LaunchBackend` |
| `NewClaudeCode` | `claudecode.go` | Constructor: sets the binary, embeds `agent.LaunchBackend`, and hands `InitLaunch` the lifecycle, the context provider, and `Surfaces` — claude's `agent.Declaration` |
| `ClaudeConfig` | `claudecode.go:18` | Typed decode target. `BinaryPath`/`Args`/`Env` are live; `Model` is decoded and never read |
| `ClaudeConfig.BackendType` | `claudecode.go:33` | `"claude-code"` |
| `Configure` | `claudecode.go:96` | `agent.Configurable`: binary/args/env |
| `Execute` | `claudecode.go:114` | Minimal-oneshot JSON branch, else `ExecuteCLI` |
| `buildArgs` | `claudecode.go:231` | The whole claude argv |
| `ResolveModel` | `chat.go:254` | Nickname → concrete model id; `ok=false` fails loud. Sole production caller `internal/adapters/operations/delegate.go:317` |
| `EngineCLIs` / `ClaudeEngineCLIs` | `enginecli.go:172` / `:178` | Oneshot + interactive surface declarations |
| `ClaudeCodeHookWriter` | `claude.go` | `agent.SettingsReader` |
| `NewWriter` | `claude.go` | Constructs the settings status read (`agent.SettingsReader`), reached through `Claude.SettingsReader` (`definition.go`) |
| `Status` | `claude.go` | The `SettingsReader`: what the project writer's claims say is installed. Every write is a claim (`DeliverSettings`, `DeliverHooks`, `DeliverMCP` in `definition.go`) |
| `ProjectSettingsPath` / `GlobalSettingsPath` / `SettingsPath` / `MCPConfigPath` | `claude.go` | Path vocabulary consumed by `internal/adapters/operations/hooks.go:272,277` and `internal/ltk/engine/claudecode.go:151,153` |
| `MCPRegistrar` | `mcp_registrar.go` | taskloom's `engine.Engine`; `Register` patches one `mcpServers` member through a taskloom-owned `confpatch.Store` via `applyMCPServers` |
| `renderCommand` / `TransformToClaudeCommand` | `commandfiles.go` | The commands approach's `<name>.md` renderer, written by `kit.DeliverCommands` |
| `Declaration` | `definition.go` | claude's `agent.Declaration`: per surface kind, the approach names a binding may select and the default — a static table; delivery is the typed approaches' |
| `ApproachSystemPrompt` | `surfaces.go` | claude's own name for its out-of-cwd framed context consumed via `--append-system-prompt-file`. Declared here and nowhere shared: no other engine has it |
| `HookPayload` / `HookOutput` / `DecodeHookPayload` / `EncodeDeny` | `hooks_wire.go:33` / `:103` / `:110` | The hook wire contract `internal/ltk/engine` and `internal/adapters/cli` import rather than redefine |

**Stubbed or absent:** there is no
`Setup` override — the shared `LaunchBackend` path is used.

## How it drives the engine

Two native CLI surfaces.

- **Oneshot**: `claude --print`, **prompt on stdin** (`agent.PromptStdin`, `enginecli.go:182`; `promptStdin`, `claudecode.go:350`). Argv delivery was moved to stdin after it hit `E2BIG` on `ctxloom weave`.
- **Interactive**: prompt as a trailing argv positional (`enginecli.go:194`; `claudecode.go:338-342`), plus `--name <harp>` from `CTXLOOM_SESSION_HARP` (`claudecode.go:223`, `:273`) — interactive only, since `/rename` cannot be injected.
- **Minimal-form / distill argv** (`minimalModeArgs`, declared via `MinimalLaunch` and resolved by `Setup`): `--output-format json --tools "" --disable-slash-commands --no-session-persistence --strict-mcp-config --system-prompt "" --settings <inline JSON>`.

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
| Native per-tool deny list | **yes — the only engine with one.** (a) the fixed plan-tier `--disallowedTools` token; (b) configurable `deny_tools` unioned into `permissions.deny` in `.claude/settings.json` as one claim per denied tool on a `permissions.deny` element (`settingsClaims`, `definition.go`); a deny the user already has stays theirs |
| Context surface | Project root → a section appended to `CLAUDE.md`, owned by the ownership record (`kit.AppendedSection`, `definition.go`). Shared cell → out-of-cwd `<hash>.sysprompt.md` (`contextdelivery.go:50`) pointed at by `--append-system-prompt-file` (`claudecode.go:294-299`). **claude does not read `AGENTS.md`** — deliberate (`enginecli.go:34-38`) |
| MCP | Project `.mcp.json` (`mcpApproach.DeliverMCP`'s claims, `definition.go`). In a shared cell it is an out-of-cwd file passed as `--mcp-config`; in a trusted repository ctxloom's servers **layer over** the project `.mcp.json`, and otherwise `--strict-mcp-config` keeps it out (`repoSourceArgs`, invariant 8). Global via `MCPRegistrar.ConfigPath` → `~/.claude.json` |
| Commands | `.claude/commands/*.md` at the project root, `<config dir>/commands/*.md` under the session home; frontmatter + mustache→`$N` body (`commandsApproach.DeliverCommands` over `kit.DeliverCommands`, `commandfiles.go`). No dedup against the user's `~/.claude/commands` |
| Skills | `.claude/skills/<name>/**` at the project root, `<config dir>/skills/<name>/**` under the session home, vendor-invalid skills refused (`skillsApproach.DeliverSkills` over `kit.DeliverSkills`, `skillconstraints.go`) |
| One-shot / resume | **Supported.** Declares `DelegatedChildren` with `ResumesByKey` (`Build`). This adapter's only session-identity lever is `--name <harp>` (display name only) |
| Transcript | **No scrape.** The `~/.claude/projects/<encoded-cwd>/*.jsonl` scraper was deleted (`capabilities.go:17-27`) after its cwd→slug encoder produced non-existent dirs for any path with a dot, underscore, or space. An opt-in vendor reader exists for the interactive-pty gap (`internal/adapters/operations/vendorreader.go:71`) |
| Model + auth | `--model` emitted when non-empty; empty lets the CLI pick (`claudecode.go:263-266`). Auth is the run's mode (`claudeAuth`, settled by `launch.RunAuth`; see [isolation](isolation.md#credential-delivery)): the token for every agent, the top-level `auth:` for the human's own session |
| Isolation | **Supported, no auth gap.** Every agent authenticates with `token` (`CLAUDE_CODE_OAUTH_TOKEN`, the `claude setup-token` token the human exports), read from the launching env and set in the launch env with every other credential variable removed (`claudeAuth`); no credential file is ever copied. Only the human's own session may run in `login`, which shares their storage in place on the host and is refused in a container (`engine.ErrHostOnlyStore`, remedy `auth: token`). `~/.claude.json` is never copied: its `primaryApiKey` crosses only into the human's own `login` session's generated instance. Additionally, claude is the one engine that can isolate a *shared* cwd without a container, via the out-of-cwd flag trio |
| Status | **Supported — the exercised default** |

## Invariants

1. **The argv's surface flags come from what the runner delivered.** `Execute` hands `inst.Exec` the delivery's presentations (`req.Presented`); each flag's NAME and path come from the typed approach's own `Presented`, never from a constant beside it, so a moved file or a changed flag changes the argv with it.
2. **What is presented is where the bytes are.** A delivery's `Presented` path is a file it declares, a file it claims into, or the directory its files land in (`TestDelivered_PresentsWhereTheApproachWrites`).
3. **`Path() == ""` means "emit no flag"** — the seam between delivery and argv. An approach reports `""` when it delivered nothing (empty content), and claude must never be handed a flag naming a file that was never written.
4. **Ownership is marked by the `"ctxloom"` executable token** via `agent.IsManaged(cmd, "ctxloom")`, repeated at six call sites and deliberately verb-agnostic.
5. **`claudeCodeHook.SCM` is `json:"-"`** because claude validates settings against a strict Zod schema (`claude.go:149`).
6. **Writes are marker-merged or manifest-scoped, never whole-file overwrites.**
7. **claude never passes `safefs.AllowEmpty()`**, so a zero-byte write over an existing file it writes is refused by safefs's empty-write guard (and `agent.CanonicalJSON` always emits at least `{}\n` anyway).
8. **`--strict-mcp-config` follows the repository's trust verdict** (`repoSourceArgs`): a trusted repository's launch layers `--mcp-config` over its project `.mcp.json`; any other verdict adds `--setting-sources user --strict-mcp-config`, so the repository's own settings and MCP servers never load, and a presentation that depends on them is refused (`ErrUntrustedProjectMCP`, `errUntrustedSettingsPresented`).

## Divergences from documented or implied behavior

- **`buildArgs` can emit a variadic flag as the last token before the trailing prompt positional, with no `--` terminator** (`claudecode.go:258`, `:300-309`, `:338-342`), so claude's parser can swallow the prompt as flag values. Today's safety is incidental — `--model`/`--name` happen to follow. `agent.CLIFlag.Value` has no `ValueVariadic`, so the anti-drift test structurally cannot catch it.
- **The minimal-oneshot path (distill/compaction) returns `ExitCode 0, err nil` having written zero bytes** — both write errors are discarded with `_, _ =` (`claudecode.go:132-150`).
- **A malformed `permissions` block is warned about and then deleted from the user's file** — `delete(raw, "permissions")` runs unconditionally, outside the `else` (`claude.go:311-330`), destroying user `allow`/`ask`/`defaultMode`/`additionalDirectories`. No corrupt-file backup on this path.
- **The field doc promises to preserve "legacy mcpServers for backwards compat" while the code does `delete(raw, "mcpServers")`** with no migration anywhere (`claude.go:100` vs `:333`). It also fires on the uninstall path.
- **Empty assembled context produces no file, no flag, and no warning** (`contextdelivery.go:50-55`; `claudecode.go:294-299`); nothing distinguishes "legitimately no context" from "assembly bug".
- **`agentfiles.go` — the entire sub-agent-roster writer (`ClaudeAgents`, `AgentExport`, `WriteAgentFiles`, `TransformToClaudeAgent`) plus a 219-line test suite — has zero production callers**; `enginecli.go:149` states it outright. It predates claude's own native `--agents <json>` flag.
- **A `minimalSettings` marshal failure returns `"{}"`, dropping `permissions.defaultMode: bypassPermissions`** — the setting that keeps a headless distill run from blocking (`claudecode.go:375-378`).
- **`internal/engines/claude/docs/design/*.md` carries 357 lines describing deleted symbols** (`chat_stream.go`, `chat_run.go`, `ClaudeSessionHistory.parseEntries`) and the unwired `agentfiles.go`.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Transport](../agentcoord/transport.md) · [Isolation](isolation.md)
