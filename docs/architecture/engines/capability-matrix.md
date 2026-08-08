# Engine capability matrix

The fastest way to answer "does engine X actually support Y?". The `Backend`
abstraction is uniform; **the engines behind it are not**. Several capabilities the
abstraction implies are unsupported on specific engines, and a few are wired
host-side but never reach any engine at all. Every cell below is what the code
**does**, with a `file:line`.

Registered backend ids: `claude-code`, `antigravity`, `codex`, `kiro`, `opencode`,
`acp` (generic), `mock` — all in one `init()` at
`internal/lm/backends/registry.go:261-450`. `internal/mockengine` is **not** a
registered backend; it is a fake vendor CLI (see [mockengine](mockengine.md)).

## 1. How each engine is driven

| | claude-code | codex | kiro | antigravity | opencode | acp (generic) |
|---|---|---|---|---|---|---|
| Binary | `claude` | `codex` | `kiro-cli` | `agy` | `opencode` | config's `command` |
| ACP transport kind | `ACPAdapter` | `ACPAdapter` | `ACPNative` | **`ACPBespoke`** | `ACPNative` | `ACPNative` |
| Adapter binary | `claude-code-acp` (Zed) | `codex-acp` (Zed) | — | — | — | — |
| Declared at | `internal/claude/chat.go:28` | `internal/codex/chat.go:21` | `registry.go:221` | `registry.go:229` | `registry.go:224` | `registry.go:236` |
| Structured chat | generic ACP driver | generic ACP driver | generic ACP driver over `kiro-cli acp` | **bespoke prose loop over repeated `agy -p`** (`internal/antigravity/chat.go:57`) | generic ACP driver over `opencode acp` | generic ACP driver |
| Oneshot subcommand | none (`claude --print`) | **`codex exec`** | `kiro-cli chat --no-interactive` | `agy -p` | none (TUI is the default subcommand) | n/a |
| Prompt channel | **stdin** (oneshot); trailing positional (interactive) | **positional, both surfaces** | positional | `-p`/`-i` flag value | `--prompt <text>` | n/a |
| Prompt-channel decl | `internal/claude/enginecli.go:182`,`:194` | `internal/codex/enginecli.go:186`,`:197` | `internal/kiro/backend.go:199` | `internal/antigravity/backend.go:120` | `internal/opencode/interactive.go:136` | — |
| Session name at launch | `--name <harp>` (interactive only) | none (harp env crosses, unread) | none | none | none | none |

ctxloom **never installs** the Zed ACP adapters; it locates them on PATH and prints
an install hint on absence (`registry.go:208-220`).

## 2. Permission tiers — what each `PermissionMode` becomes

`agent.PermissionMode` (`internal/shared/agent/permissions.go:15-33`) is one
vocabulary; each engine maps it to its own mechanism.

| Tier | claude-code | codex | kiro | antigravity | opencode |
|---|---|---|---|---|---|
| `default` | no flag | `--sandbox workspace-write` | no flag | no flag | no managed permission key |
| `acceptEdits` | `--permission-mode acceptEdits` | `--sandbox workspace-write` (**not distinguished from default**) | `--trust-tools=fs_read,fs_write` | `--mode accept-edits` | no managed key |
| `plan` | `--permission-mode plan` **+** `--disallowedTools "Bash,Edit,Write,NotebookEdit"` | `--sandbox read-only` + `--ask-for-approval never` | `--trust-tools=fs_read` | `--mode plan` — **emitted, not enforced** | `opencode.json` `permission {edit:"deny", bash:"deny"}` |
| `bypass` | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` (and **no** `--sandbox`) | `--trust-all-tools` | `--dangerously-skip-permissions` | argv `--auto` |
| `buildArgs` | `internal/claude/claudecode.go:253-258` | `internal/codex/backend.go:452-467` | `internal/kiro/backend.go:185-193` | `internal/antigravity/backend.go:110-118` | `internal/opencode/settings.go:57-59`, `interactive.go:141-145` |

### `EnforcesReadOnlyPlan` — where `plan` collapses

`CollapsePlanIfUnenforced` (`internal/shared/agent/permissions.go:116-121`) turns
`plan` into `default` for any backend that cannot enforce a genuine read-only tier,
so `plan` never runs unrestrained. Applied at `internal/cli/run.go:1499`
(interactive) and `internal/operations/oneshot.go:417` (headless fan-out).

| Backend | `enforcesReadOnlyPlan` | Does `plan` survive? | Evidence |
|---|---|---|---|
| `claude-code` | **true** (`registry.go:297`) | yes | LIVE VERIFIED 2026-07-15, claude 2.1.210: plan + deny list denied a sentinel overwrite (`claudecode.go:237-247`) |
| `codex` | **true** (`registry.go:342`) | yes | `--sandbox read-only` on both subcommands |
| `kiro` | **true** (`registry.go:377`) | yes | LIVE VERIFIED 2026-07-15, kiro-cli 2.12.1: sentinel byte-unchanged; kiro-cli printed *"Command fs_write is rejected … denied list"*; both positive controls landed the write (`registry.go:370-376`, `backend.go:175-184`) |
| `opencode` | **true** (`registry.go:437`) | yes | `edit:deny` gates both edit and write tools, `bash:deny` too — **stricter than opencode's own built-in `plan` agent**, which leaves bash allowed (`settings.go:48-56`) |
| `antigravity` | **false** (field omitted, `registry.go:301-317`) | **no — collapses to `default`** | LIVE VERIFIED 2026-07-15, agy 1.1.2: under `--mode plan` a sentinel write **landed exactly like the bypass control**, and the model self-reported "not in plan mode or read-only mode" (`registry.go:133-147`, `backend.go:98-109`) |
| `acp` (generic) | **false** | **no** | no read-only tier |
| `mock` | **false** | **no** | unset |

**The antigravity row is the one to remember.** The `--mode plan` flag *is* emitted
by `buildArgs`, and agy accepts it — it simply does not enforce read-only under
headless `-p`. Setting the descriptor `true` would tell the resolver to trust a flag
proven not to work. This is a deliberate, documented compensation, pinned by
`internal/lm/backends/capabilities_test.go:29`.

Two further permission facts:

- A headless **oneshot** floors any non-`SafeHeadless()` posture to `PermissionBypass` (`internal/lm/grpc/server.go:253-255`), so a oneshot cannot hang on an engine approval prompt. `SafeHeadless()` is true only for `bypass` and `plan` (`permissions.go:127`).
- On antigravity, **mid-turn permission answers are inert**: `agy -p` never forwards a permission request, so `ForwardPermissions` cannot be honored; posture is decided once at launch (`internal/antigravity/chat.go:41-43`).

## 3. Native per-tool deny list

| Backend | Native per-tool deny list? | Mechanism |
|---|---|---|
| `claude-code` | **YES — the only one** | (a) fixed plan-tier `--disallowedTools "Bash,Edit,Write,NotebookEdit"` (`claudecode.go:258`); (b) configurable `deny_tools` unioned into `permissions.deny` in `.claude/settings.json` — `SurfaceInputs.DenyTools` → `internal/claude/surfaces.go:271` → `surfacedelivery.go:47` → `mergeDenyTools` (`internal/claude/claude.go:536`), monotonic union only |
| `codex` | **no** | Only whole-sandbox tiers. `NewSurfaces` (`internal/codex/surfaces.go:311`) never reads `in.DenyTools` — the field is accepted and dropped with no warning |
| `kiro` | **allowlist-shaped, not a deny list** | `--trust-tools=<tool,…>` is an *allowlist* over kiro's real vocabulary (`fs_read`, `fs_write`, `execute_bash`); anything unlisted is refused by kiro itself. ctxloom's `deny_tools` is **not wired** — `internal/kiro/surfaces.go:219` never reads it |
| `antigravity` | **no** | `internal/antigravity/surfaces.go:213` never reads `in.DenyTools`. The nearest thing is the out-of-band ltk PreToolUse hook's `EncodeDeny` (`hooks_wire.go:111`) |
| `opencode` | **no** | Only the fixed two-key read-only pair `{edit, bash}` (`settings.go:57-59`); no plumbing from a per-tool deny list |

### The deny-list reality check

**`ManagedConfig.DenyTools` reaches the launch path** since `40b49a7f`. The Go
struct carries it (`internal/shared/agent/backend.go:362`) and so does the proto
(`repeated string deny_tools = 7`, `internal/lm/grpc/llm.proto`); `ManagedConfigToProto`
and `managedConfigFromProto` both carry it. See [the plugin wire](grpc-wire.md).

All three delivery paths now carry it:

| Path | Carries `DenyTools`? | Carries `Skills`? | Site |
|---|---|---|---|
| `ctxloom run` / oneshot (gRPC launch) | yes — **since `40b49a7f`** | yes — **since `40b49a7f`** | `internal/lm/grpc/managed.go` |
| `ctxloom apply-hooks` | yes | **no** | `internal/operations/hooks.go:452` |
| `ctxloom profile materialize` | yes | yes | `internal/operations/profile_materialize.go:129`, `:131` |

**Whether the engine then *honours* it is a separate question** — the per-engine
table above is the one that answers it. Only claude has a native per-tool deny
list; codex and antigravity accept `DenyTools` at the seam and drop it in
`NewSurfaces`, and kiro's lever is an allowlist. The wire carrying the field does
not give an engine a capability it never had.

> **This used to be false and it is the reason to distrust "it is configured, so
> it applies".** Before `40b49a7f` the proto had 5 fields and `ManagedConfigToProto`
> dropped `deny_tools` and `skills` outright, so a `deny_tools` entry applied when
> you materialized or applied hooks and **did not apply to the engine ctxloom
> launched** — exit 0, no diagnostic. The field's own comment called it "the
> deny-tools.md root-cause fix"; it had been inert since it was written.

## 4. Context surface — how assembled context actually reaches the engine

| Backend | Mechanism | Reads `AGENTS.md`? | Hook-mediated? | Site |
|---|---|---|---|---|
| `claude-code` | **two realizations of one surface**: isolated cell → marker-merge into `CLAUDE.md`; shared cell → out-of-cwd `<hash>.sysprompt.md` passed as `--append-system-prompt-file` | **no — deliberate** (`enginecli.go:34-38`) | no (apply path uses a SessionStart injection hook) | `internal/claude/surfaces.go:81`, `contextdelivery.go:50`, `claudecode.go:294-299` |
| `codex` | **two additive routes composed**: marker-merge into `AGENTS.md`, **plus** a SessionStart hook in `config.toml` whose argv carries the content hash — codex ingests the *hook's output*, never opening the cache file | yes | **yes — the only engine that fires the inject-context hook** | `internal/codex/surfaces.go:119`, `:165`, `:330-334`; `backend.go:109` |
| `kiro` | native steering file `.kiro/steering/ctxloom-context.md` with front-matter `inclusion: always`, auto-loaded every session | no | no (`WithContextHook` **fails loudly**, `surfaces.go:286-293`) | `internal/kiro/settings.go:244` |
| `antigravity` | whole-file write to `.agents/AGENTS.md` | `.agents/AGENTS.md` | no (agy fires no SessionStart hook for context) | `internal/antigravity/antigravity.go:541`, `surfaces.go:61` |
| `opencode` | `.opencode/ctxloom-context.md` referenced from `opencode.json`'s `instructions[]` key | no | no | `internal/opencode/settings.go:36`, `chat.go:190-205` |

**`SharedRealization` — out-of-cwd redirect.** Only `claude-code` has one
(`internal/claude/surfaces.go:388`): flag-pointed scratch files for context, MCP and
settings, so a live shared cwd is never written into. codex, kiro, antigravity and
opencode all return `nil, false`
(`internal/codex/surfaces.go:414`, `internal/kiro/surfaces.go:299`,
`internal/antigravity/surfaces.go:286`, `internal/opencode/surfaces.go:183`).
**Consequence: for every engine but claude, concurrent per-agent isolation requires
a private cwd (worktree) or a container cell.** codex substitutes a per-run
`CODEX_HOME`, which is also the only thing that isolates its global-only prompts and
skills.

## 5. MCP, commands and skills

| Backend | MCP file | MCP scopes | Commands dir | Skills dir |
|---|---|---|---|---|
| `claude-code` | `.mcp.json` (+ out-of-cwd via `--mcp-config`, **without** `--strict-mcp-config`, so ctxloom's servers **layer over** the user's) | project + global (`~/.claude.json`) | `.claude/commands/*.md` | `.claude/skills/<n>/**` |
| `codex` | `config.toml` `[mcp_servers]` | scoped by per-run `CODEX_HOME`; global `~/.codex/config.toml`; project `<dir>/.codex/config.toml` | **`$CODEX_HOME/prompts/<n>.md` — GLOBAL ONLY** | **`$CODEX_HOME/skills/<n>/SKILL.md` — GLOBAL ONLY** |
| `kiro` | `.kiro/settings/mcp.json` | project **and** global (`$KIRO_HOME/settings/mcp.json`) | `.kiro/skills/<n>/SKILL.md` | `.kiro/skills/<n>/SKILL.md` |
| `antigravity` | `.agents/mcp_config.json` | **project ONLY** — `ConfigPath(global=true)` returns `ErrNoGlobalMCPConfig` (`mcp_registrar.go:16`, `:40`) | `.agents/skills/<n>/SKILL.md` | `.agents/skills/<n>/SKILL.md` |
| `opencode` | `opencode.json` `mcp` key — **not over the ACP wire**; opencode never receives `mcpServers` on `session/new` (`chat.go:28-33`) | project (transient overlay) | `.opencode/command/<n>.md` | `.opencode/skill/<n>/SKILL.md` |
| `acp` (generic) | — | — | **none** (no settings writer, no exports) | none |

Two engines put **commands and skills in the same directory** — `kiro`
(`.kiro/skills/`) and `antigravity` (`.agents/skills/`). Same-name collisions are
resolved **skill-wins** by `agent.FilterCommandsClaimedBySkills`, applied before the
commands delivery is built (`internal/kiro/surfaces.go:41-48`,
`internal/antigravity/surfaces.go:218`).

antigravity has **no per-invocation MCP flag**: requested servers are reported as an
advisory status string, not applied for that turn
(`internal/antigravity/chat.go:281`).

**Skills never reach a launched engine.** `ManagedConfig.Skills` does not cross the
plugin wire (§3 above), so the skills surface receives an empty list on every
`ctxloom run`. Five engines declare a working `skillExports` function
(`registry.go:292`, `:315`, `:341`, `:369`, `:436`) and the machinery is otherwise
complete — it is starved at the wire, not unimplemented.

## 5b. Hooks — and the one engine that has none

Five of the six engines carry ctxloom's unified hooks into a native settings
surface. `opencode` carries none: `opencode.json` has no hook key and there is no
event vocabulary to route the six unified events onto, so
`OpencodeWriter.WriteSettings` takes a `*wire.HooksConfig` it can do nothing with
(`internal/opencode/settings.go:513`).

| Backend | Hooks land in | Routed by |
|---|---|---|
| `claude-code` | `.claude/settings.json` | `internal/claude/claude.go:680` |
| `codex` | `.codex/config.toml` | `internal/codex/settings.go:404` |
| `antigravity` | `.agents/hooks.json` | `internal/antigravity/antigravity.go:463` |
| `kiro` | `.kiro/agents/ctxloom.json` | `internal/kiro/settings.go` |
| `opencode` | **nowhere** | — (`noHooksReason`, `registry.go`) |
| `acp` (generic) | nowhere (no surfaces at all) | — |

The loss is structural and fine to have; the **silence** was not.
`ctxloom profile materialize --backend opencode` used to print four true `wrote`
lines with the dropped hook nowhere among them, so a team could ship a guardrail
and a deskmate could inherit the profile without either being told the guardrail
did not come with it (`whiny-exclusive`).

The gap is now DECLARED (`agentDescriptor.noHooksReason`) and reported:
`backends.UncarriedSurfaces` turns it into an `agent.SurfaceLoss` whenever the
run actually carries hooks, materialize puts it in
`MaterializeProfileResult.NotCarried` (so `--format json` sees it as data), and
the CLI prints it beside the `wrote` lines:

```
Materialized team → ./out (opencode)
  wrote context
  wrote settings
  wrote commands
  wrote skills
  NOT carried: hooks (1 session_start) — opencode has no hook mechanism
```

A capability gap nobody asked to use stays quiet — a profile declaring no hooks
gets no line, the same rule `agent.RouteUnifiedHooks` applies one level down for
a single unsupported event. `TestDeliveryApproach_HookCarriageMatchesDeclaration`
(`tests/integration`) holds the declaration against the delivered payload for
every registered backend, so it cannot drift into claiming a loss that isn't real
or missing one that is.

**Still silent elsewhere:** `ctxloom run` and `manage hooks install` deliver to
the same hookless engine and say nothing. Only materialize reports the loss today.

## 6. Session history and transcripts

| Backend | `History()` | Mechanism | Note |
|---|---|---|---|
| `claude-code` | **nil** | scraper **deleted** (`capabilities.go:17-27`) | its cwd→slug encoder produced non-existent dirs for any path with a dot/underscore/space |
| `codex` | **nil** | `rollout-*.jsonl` scraper **deleted** (`capabilities.go:17-25`) | envelope-vs-flat parse mismatch silently returned zero-entry sessions |
| `kiro` | **nil** | `internal/kiro/session.go` **deleted** (commit `6683bc4c`) | it parsed the v1 JSONL store while a real `--no-interactive` oneshot persists to a v2 SQLite blob — returned empty forever without erroring |
| `antigravity` | **nil** | `transcript_full.jsonl` scraper **deleted** | **but** `agyConversationMap` (`capabilities.go:95-153`) still parses agy's private `~/.gemini/antigravity-cli/cache/last_conversations.json` for chat continuation |
| `opencode` | **real** | `opencode session list --format json` + `opencode export <id>` (`capabilities.go:116`, `:162`) | the only engine with a live `History()`; carries an explicit written refusal to read `opencode.db` (`capabilities.go:19-26`) |

`internal/lm/grpc/canonical_source.go:50` lists the retired-scraper backends:
codex, kiro, antigravity, claude-code. A `nil` history now **fails loudly** at both
consumers (`internal/operations/sessionfeed.go:509`,
`internal/lm/grpc/sessionhistory.go:245`). Canonical capture rides the ACP stream
(`internal/transcript`); claude and codex additionally have opt-in vendor readers
for the interactive-pty gap (`internal/operations/vendorreader.go:71`, `:72`).

## 7. One-shot driving and resume

Two gates in `internal/agentcoord/coord/spawner.go`:

| Backend | `resumeCapableBackends` (`:225-228`) | `oneShotSupportedBackends` (`:248-252`) |
|---|---|---|
| `claude-code` | yes | **yes** |
| `codex` | yes | **yes** |
| `antigravity` | yes | **no** — legacy go-plugin Chat dial, no live loadSession confirm |
| `kiro` | **no** | no |
| `opencode` | **no** | no |

`driving: oneshot` on a backend outside the intersection **fails loud** rather than
silently degrading (`spawner.go:275-278`, `:371-375`). kiro and opencode never reach
the release gate — they are rejected at the resume-capability check first.

Note that `ChatRequest.ResumeSessionID` does not cross the plugin wire at all
([grpc-wire](grpc-wire.md) §3), so the plugin-side resume guards at
`internal/acp/session.go:367` and `internal/antigravity/chat.go:69` are unreachable.

## 8. Isolation support

Full detail in [isolation](isolation.md). Summary:

| Backend | Host + worktree lever | Container image | Container auth | Gap |
|---|---|---|---|---|
| `claude-code` | `CLAUDE_CONFIG_DIR` | `ctxloom-agent:latest` | `ANTHROPIC_*` env, else **RW copy-mount** of `~/.claude/.credentials.json` (RW because claude refreshes the token in place) | none |
| `codex` | `CODEX_HOME` | default tag | `OPENAI_API_KEY`, else **RO mount** of `~/.codex/auth.json` | none in-container; **on plain host runs the credential is copied into the project working tree** and is not covered by ctxloom's managed ignore set |
| `kiro` | `KIRO_HOME` (config + sessions **only**) | `ctxloom-agent-kiro:latest` | **`KIRO_API_KEY` only** | **subscription login cannot authenticate in a container.** Credentials live in a global SQLite (`$XDG_DATA_HOME/kiro-cli/data.sqlite3`), not under `~/.kiro`. `XDG_DATA_HOME` is gated on `KIRO_API_KEY`; without it, a `ClassIsolation` finding |
| `antigravity` | **REFUSED** | default tag | file OAuth token `~/.gemini/antigravity-cli/antigravity-oauth-token` only | **subscription login cannot authenticate in a container** — the keyring's UID-addressed `/run/user/<uid>/bus` socket does not exist in the container's namespaces. On host+worktree, isolation is *refused* (fatal `curatedHomeRefusal`): agy's keyring escapes `$HOME`, and `agy -p` ignores the launch cwd entirely, always writing to global scratch |
| `opencode` | `XDG_DATA_HOME` (**`HonoursVarForCreds: true`** — the opposite of kiro) | default tag | `OPENROUTER_API_KEY` env, else RO mount of XDG `auth.json` | no subscription blocker; unverified whether a containerized run refreshes `auth.json` in place, which an RO mount would break |
| `acp` (generic) | **none — silently** | default arm → `ctxloom-agent:latest` | **inherits `resolveClaudeContainerAuth`** — the user's Anthropic credentials are passed to a foreign engine | in neither `credentialSeedSpecs` nor `curatedHomeSpecs`, so it gets zero engine-global isolation **with no finding** (`internal/lm/isolation/auth.go:341`, `:526`) |
| `mock` | none | — | — | same unprofiled default arm |

Composable container engines, in order (`internal/lm/isolation/profile.go:357`):
`antigravity`, `claude-code`, `codex`, `kiro`, `opencode`.

## 9. Support status

| Backend | Status | Source |
|---|---|---|
| `claude-code` | **supported — the exercised default** | `website/src/content/docs/concepts/architecture.md` @ `0f59fbae` |
| `codex` | **experimental** + registry `LIVE-UNTESTED` banner: "never run against a real account on any dev host" | `registry.go:319-321` |
| `kiro` | **experimental** + registry `LIVE-UNTESTED` banner — **but that banner may be stale**: the package doc records later live verification against authenticated kiro-cli 2.12.1 (permission tiers, `--model` honor) | `registry.go:352-354` vs `internal/kiro/backend.go:8-16` |
| `antigravity` | **experimental**; package capability claims are stamped to agy **v1.0.7** while the verified install is **1.1.2**, and one v1.0.7 claim (no plan mode) is already proven false | `internal/antigravity/antigravity.go:8` |
| `opencode` | **undeclared** — appears in neither the experimental-engines caution nor `reference/environment.md` | grep of `website/src/content/docs/` → 0 hits |
| `acp` (generic) | drives whatever config supplies; provenance vetting for a third-party command is the user's own job | `registry.go:231-236` |

## 10. Capabilities that exist on every engine and fire on none

- **`ContentCommands` / `RegisterFromContent`** — implemented with a real body by all six backends (claude, antigravity, acp, kiro, codex, opencode). `LaunchBackend.commands` is assigned at `internal/shared/agent/launch_backend.go:92` and **read nowhere**. Production call sites: zero. Real command writes go through the commands *surface* instead.
- ~~**`ManagedConfig.Skills`**~~, ~~**`ManagedConfig.DenyTools`**~~, ~~**`wire.Hook.PreToolFallback`**~~ — **all three now cross the launch wire** (`40b49a7f`). They belonged in this section because none of them did: five engines declared `skillExports` that never received anything, claude's deny list was never applied at launch, and `PreToolFallback` arrived `false` at its one consumer (`internal/antigravity/antigravity.go:388`, "the only way it ever fires on agy"). Left visible because "declared everywhere, fires nowhere" is the pattern this section catalogues, and these were its three clearest instances.
- **`RunOptions.temperature` and `RunOptions.max_tokens`** — carried by the proto, constructed by nothing, read by no backend.

## See also

- [The `Backend` abstraction and registry](backend-abstraction.md)
- [The plugin wire](grpc-wire.md)
- [Isolation](isolation.md)
- Per-engine pages: [claude](claude.md) · [codex](codex.md) · [kiro](kiro.md) · [antigravity](antigravity.md) · [opencode](opencode.md) · [mockengine](mockengine.md)
