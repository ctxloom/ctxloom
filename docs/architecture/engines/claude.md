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
| `NewClaudeCode` | `claudecode.go` | Constructor: sets the binary, embeds `agent.LaunchBackend`, and hands `InitLaunch` the lifecycle, the context provider, a nil `SessionHistory`, and `Surfaces` — claude's `agent.Declaration` |
| `ClaudeConfig` | `claudecode.go:18` | Typed decode target. `BinaryPath`/`Args`/`Env` are live; `Model` is decoded and never read |
| `ClaudeConfig.BackendType` | `claudecode.go:33` | `"claude-code"` |
| `Configure` | `claudecode.go:96` | `agent.Configurable`: binary/args/env |
| `Execute` | `claudecode.go:114` | Minimal-oneshot JSON branch, else `ExecuteCLI` |
| `buildArgs` | `claudecode.go:231` | The whole claude argv |
| `ResolveModel` | `chat.go:254` | Nickname → concrete model id; `ok=false` fails loud. Sole production caller `internal/adapters/operations/delegate.go:317` |
| `EngineCLIs` / `ClaudeEngineCLIs` | `enginecli.go:172` / `:178` | Oneshot + interactive surface declarations |
| `ClaudeCodeHookWriter` | `claude.go:26` | `agent.SettingsWriter` + `agent.ContextWriter` |
| `NewWriter` | `claude.go:20` | Registry `newWriter` seam (`registry.go:276`) |
| `WriteSettings` / `RemoveSettings` / `Status` | `claude.go:161` / `:744` / `:798` | The `SettingsWriter` trio. `WriteSettings` has **zero production callers for claude** — live only via the conformance suite |
| `WriteContext` | `claude.go:243` | Marker-merge into `CLAUDE.md` |
| `ProjectSettingsPath` / `GlobalSettingsPath` / `GlobalCommandsDir` / `SettingsPath` / `MCPConfigPath` | `claude.go:48` / `:54` / `:67` / `:76` / `:83` | Path vocabulary consumed by `internal/adapters/operations/hooks.go:272,277` and `internal/ltk/engine/claudecode.go:151,153` |
| `MCPRegistrar` | `mcp_registrar.go` | taskloom's `engine.Engine`; `Register` patches one `mcpServers` member through a taskloom-owned `confpatch.Store` via the same `applyMCPServers` as `writeMCPConfig` |
| `WriteCommandFiles` / `TransformToClaudeCommand` | `commandfiles.go:18` / `:44` | `.claude/commands/*.md` manifest write + renderer |
| `WriteSkillFiles` | `skillfiles.go:21` | `.claude/skills/<name>/**` manifest write |
| `Surfaces` | `surfaces.go` | claude's `agent.Declaration`: per surface kind, the approaches claude can construct and its default. Every approach wraps an existing claude writer verbatim |
| `ApproachSystemPrompt` | `surfaces.go` | claude's own name for its out-of-cwd framed context consumed via `--append-system-prompt-file`. Declared here and nowhere shared: no other engine has it |
| `flagArgs` | `surfaces.go` | Reads the out-of-cwd launch flags off the run's `Resolved()` selection — flag name from each approach's own `Present`, path from what it recorded — and contributes nothing for an approach that delivered nothing |
| `HookPayload` / `HookOutput` / `DecodeHookPayload` / `EncodeDeny` | `hooks_wire.go:33` / `:103` / `:110` | The hook wire contract `internal/ltk/engine` and `internal/adapters/cli` import rather than redefine |

**Stubbed or absent:** `SessionHistory` is `nil` (`claudecode.go:67`). There is no
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
| Native per-tool deny list | **yes — the only engine with one.** (a) the fixed plan-tier `--disallowedTools` token; (b) configurable `deny_tools` unioned into `permissions.deny` in `.claude/settings.json` via `mergeDenyTools` (`claude.go:536`), monotonic union only |
| Context surface | Isolated cell → marker-merge into `CLAUDE.md` (`surfaces.go:81` → `WriteContext`, `claude.go:243`). Shared cell → out-of-cwd `<hash>.sysprompt.md` (`contextdelivery.go:50`) pointed at by `--append-system-prompt-file` (`claudecode.go:294-299`). **claude does not read `AGENTS.md`** — deliberate (`enginecli.go:34-38`) |
| MCP | Project `.mcp.json` (`writeMCPConfig`, `claude.go:460`; `mcpSurface`, `surfaces.go:105`). In a shared cell it is an out-of-cwd file passed as `--mcp-config` **without** `--strict-mcp-config`, so ctxloom's servers **layer over** the user's project `.mcp.json` (`claudecode.go:288-292`). Global via `MCPRegistrar.ConfigPath` → `~/.claude.json` |
| Commands | `.claude/commands/*.md`, frontmatter + mustache→`$N` body (`commandfiles.go:18`, `:44`); optional home dedup against `~/.claude/commands` (`surfacedelivery.go:99-104`) |
| Skills | `.claude/skills/<name>/**` (`skillfiles.go:21`) |
| One-shot / resume | **Supported.** In both `resumeCapableBackends` and `oneShotSupportedBackends` (`internal/core/coord/spawner.go:225`, `:248`). This adapter's only session-identity lever is `--name <harp>` (display name only) |
| Transcript | **No scrape.** `SessionHistory` is `nil`; the `~/.claude/projects/<encoded-cwd>/*.jsonl` scraper was deleted (`capabilities.go:17-27`) after its cwd→slug encoder produced non-existent dirs for any path with a dot, underscore, or space. An opt-in vendor reader exists for the interactive-pty gap (`internal/adapters/operations/vendorreader.go:71`) |
| Model + auth | `--model` emitted when non-empty; empty lets the CLI pick (`claudecode.go:263-266`). Auth is the **agent's declared mode** (`claudeAuth`; see [isolation](isolation.md#credential-delivery)); undeclared is the token |
| Isolation | **Supported, no auth gap.** Authenticates in the agent's declared mode (`claudeAuth`): `token` (`CLAUDE_CODE_OAUTH_TOKEN`, the `claude setup-token` token the human exports), `api-key` (`ANTHROPIC_API_KEY`) or `cloud` (the provider's own variables), all read from the launching env and set in the launch env with every other credential variable removed; no credential file is ever copied. `login` shares the human's storage: in place on the host, mounted at `$HOME/.claude` in a container (refused on macOS, where it is the Keychain); `cloud` mounts the provider credential dirs the human has read-only (`providerStores`; the AWS SSO cache inside them read-write), and each file a provider variable names read-only with the variable rewritten (`Credentials.FileVars`). `~/.claude.json` is never copied. Additionally, claude is the one engine that can isolate a *shared* cwd without a container, via the out-of-cwd flag trio |
| Status | **Supported — the exercised default** |

## Invariants

1. **`Setup` must run before `buildArgs`** — connascence of execution order. `buildArgs` reads the out-of-cwd paths off `LaunchBackend.Resolved()`, which is nil before `Setup`; `flagArgs` then contributes nothing, so the argv is *silently flagless*, not an error.
2. **`Present` is load-bearing for argv.** `flagArgs` takes each flag's NAME from the resolved approach's own `Present(...)`, never from a constant beside it: change a declared flag and the argv changes with it.
3. **`Path() == ""` means "emit no flag"** — the seam between delivery and argv. An approach reports `""` when it delivered nothing (empty content, or context that fell back to the injection hook), and claude must never be handed a flag naming a file that was never written.
4. **Ownership is marked by the `"ctxloom"` executable token** via `agent.IsManaged(cmd, "ctxloom")`, repeated at six call sites and deliberately verb-agnostic.
5. **`loadSettings` and `saveSettings` must mirror each other key-for-key** — the round-trip is what preserves foreign keys (`claude.go:259`, `:364`).
6. **`claudeCodeHook.SCM` is `json:"-"`** because claude validates settings against a strict Zod schema (`claude.go:149`).
7. **Writes are marker-merged or manifest-scoped, never whole-file overwrites.**
8. **`agent.CanonicalJSON` always emits at least `{}\n`**, so claude's two `AtomicWriteFile` callers cannot write zero bytes — safe by accident of the JSON encoder, not by a guard.
9. **`--mcp-config` is used without `--strict-mcp-config` on the launch path**, so ctxloom layers rather than replaces.

## Divergences from documented or implied behavior

- **`buildArgs` can emit a variadic flag as the last token before the trailing prompt positional, with no `--` terminator** (`claudecode.go:258`, `:300-309`, `:338-342`), so claude's parser can swallow the prompt as flag values. Today's safety is incidental — `--model`/`--name` happen to follow. `agent.CLIFlag.Value` has no `ValueVariadic`, so the anti-drift test structurally cannot catch it.
- **The minimal-oneshot path (distill/compaction) returns `ExitCode 0, err nil` having written zero bytes** — both write errors are discarded with `_, _ =` (`claudecode.go:132-150`).
- **A malformed `permissions` block is warned about and then deleted from the user's file** — `delete(raw, "permissions")` runs unconditionally, outside the `else` (`claude.go:311-330`), destroying user `allow`/`ask`/`defaultMode`/`additionalDirectories`. No corrupt-file backup on this path.
- **The field doc promises to preserve "legacy mcpServers for backwards compat" while the code does `delete(raw, "mcpServers")`** with no migration anywhere (`claude.go:100` vs `:333`). It also fires on the uninstall path.
- **An unparseable `.mcp.json` becomes an empty config, and `writeMCPConfig` then saves a file containing only ctxloom's servers** (`claude.go:434-438`) — asymmetric with `loadSettings`, which was hardened for exactly this.
- **Empty assembled context produces no file, no flag, and no warning** (`contextdelivery.go:50-55`; `claudecode.go:294-299`); nothing distinguishes "legitimately no context" from "assembly bug".
- **`agentfiles.go` — the entire sub-agent-roster writer (`ClaudeAgents`, `AgentExport`, `WriteAgentFiles`, `TransformToClaudeAgent`) plus a 219-line test suite — has zero production callers**; `enginecli.go:149` states it outright. It predates claude's own native `--agents <json>` flag.
- **A `minimalSettings` marshal failure returns `"{}"`, dropping `permissions.defaultMode: bypassPermissions`** — the setting that keeps a headless distill run from blocking (`claudecode.go:375-378`).
- **Four `exists, _ := afero.Exists(...)` sites treat an I/O error as "absent"** (`claude.go:759`, `:781`, `:803`, `:814`; `commandfiles.go:24`), so a permission-denied `settings.json` makes `RemoveSettings` a silent no-op and `Status` report "not installed".
- **`internal/engines/claude/docs/design/*.md` carries 357 lines describing deleted symbols** (`chat_stream.go`, `chat_run.go`, `ClaudeSessionHistory.parseEntries`) and the unwired `agentfiles.go`.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Transport](../agentcoord/transport.md) · [Isolation](isolation.md)
