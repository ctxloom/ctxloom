# `antigravity` — `internal/antigravity`

ctxloom's backend for Google's Antigravity CLI (`agy`). It launches the binary
(interactive `-i`, headless `-p`), drives a **bespoke prose "structured chat" over
repeated `agy -p` spawns** — the only non-ACP `StructuredChat` in the codebase — and
writes the four workspace files agy reads: `.agents/hooks.json`,
`.agents/mcp_config.json`, `.agents/AGENTS.md`, and
`.agents/skills/<name>/SKILL.md`.

It additionally owns the **agy PreToolUse hook wire protocol** (`hooks_wire.go`) as a
cross-binary contract consumed by `internal/ltk/engine` and `internal/cli` — a half
that shares no state with the launch half.

Antigravity is the engine where the abstraction leaks most: it is the only backend
whose `plan` tier is **not enforced**, the only one whose host+worktree isolation is
**refused rather than degraded**, and the only one driving chat without ACP.

## Exported surface

| Symbol | Location | Meaning |
|---|---|---|
| `Antigravity` | `backend.go:26` | Embeds `agent.LaunchBackend`; adds `convMap agyConversationMap` (chat-only state) |
| `AntigravityConfig` | `backend.go:12` | mapstructure: `model`, `binary_path`, `args`, `env`. `Model` is decoded but never read in-package (read externally at `internal/cli/llm_resolve.go:77,94`) |
| `AntigravityConfig.BackendType` | `backend.go:20` | `"antigravity"` |
| `NewAntigravity` | `backend.go:38` | `BaseBackend("antigravity","1.0.0")`, `BinaryPath="agy"`, `InitLaunch(lifecycle, &AntigravityCommands{}, ctxProvider, nil /*history*/, &agent.CellDelivery{Build: BuildWellKnown(NewSurfaces), RawContext: true})` |
| `Configure` | `backend.go:60` | Type-asserts `*AntigravityConfig`; **silently ignores a wrong type** |
| `Execute` | `backend.go:67` | `ModelInfo{Provider: "google"}`, delegates to `ExecuteCLI` |
| `buildArgs` | `backend.go:84` | model + permission tier + prompt → agy flags |
| `Chat` | `chat.go:57` | `agent.StructuredChat`; the bespoke prose turn loop |
| `AntigravityHookWriter` | `antigravity.go:47` | `agent.SettingsWriter` + `agent.ContextWriter`; owns all four `.agents/` files |
| `NewWriter` | `antigravity.go:26` | Registry seam (`registry.go:312`) |
| `WorkspaceHooksPath` | `antigravity.go:67` | `dir/.agents/hooks.json` — exported for ltk (`internal/ltk/engine/antigravity.go:103,118`) |
| `SettingsPath` / `MCPConfigPath` | `antigravity.go:76` / `:83` | `.agents/hooks.json` / `.agents/mcp_config.json` |
| `WriteSettings` / `WriteContext` / `RemoveSettings` / `Status` | `antigravity.go:215` / `:541` / `:585` / `:608` | Interface impl |
| `NewSurfaces` / `Surfaces` | `surfaces.go:213` / `:185` | `agent.SurfaceSet`: context, mcp, hooks, commands, skills |
| `Surfaces.SharedRealization` | `surfaces.go:286` | **always `nil, false`** — agy has no out-of-cwd redirect for any surface |
| `WriteCommandFiles` | `capabilities.go:52` | Commands → `<name>/SKILL.md` dirs + manifest |
| `WriteSkillFiles` | `skillfiles.go:36` | Skill packages → `<name>/…` dirs + manifest |
| `MCPRegistrar` | `mcp_registrar.go:21` | `agent.MCPRegistrar` for taskloom; **project scope only** |
| `ErrNoGlobalMCPConfig` | `mcp_registrar.go:16` | Sentinel: agy has no user-level MCP config |
| `HookPayload` / `ToolCall` / `ToolArgs` / `HookDecision` | `hooks_wire.go:41` / `:51` / `:60` / `:98` | The agy PreToolUse wire contract |
| `DecodeHookPayload` / `EncodeDeny` | `hooks_wire.go:104` / `:111` | Wire entry points (`internal/ltk/engine:31`, `internal/cli/session_cmd.go:307,335`, `internal/cli/hook_stamp_plan.go:82`) |
| `agyConversationMap` | `capabilities.go:95` | Reads agy's private `~/.gemini/antigravity-cli/cache/last_conversations.json` |

**Stubbed or absent:** `History()` is `nil` (`backend.go:51`; the scraper was
deleted). `SharedRealization` is constant false. `AntigravityCommands` /
`RegisterFromContent` (`capabilities.go:26`, `:31`) are stored and never called.

## How it drives the engine

Direct CLI exec, **no ACP**. `antigravityACPTransport = agent.ACPTransport{Kind:
agent.ACPBespoke}` (`registry.go:229`) — agy has neither a native ACP subcommand nor
a first-party adapter, and its `agy agentapi` JSON API is unusable because it
hard-requires `ANTIGRAVITY_LS_ADDRESS`, a private `exa.language_server_pb` gRPC
server (`chat.go:26-34`).

- **`Execute` argv** (`backend.go:84-131`): `[configured args…] [--model <m>] [--mode plan | --mode accept-edits | --dangerously-skip-permissions] [-p <prompt> (oneshot) | -i <prompt> (interactive)]`.
- **`Chat` argv** (`chat.go:259-278`): `buildArgs` reused (model + permission only), plus `[--conversation <id> --continue]` when a conversation id is known, plus `-p <text>`, plus `[--print-timeout <go-duration>]` derived from the context deadline (`chat.go:209`). **Every turn is a fresh `agy -p` subprocess** via `os/exec` with `cmd.WaitDelay = 3s` (`chat.go:22`, `:232`).

## Capabilities

| Capability | Answer |
|---|---|
| Backend id | `"antigravity"` (`registry.go:301`); binary `agy` |
| Permission tiers | `SkipSetup` **or** plan → `--mode plan`; acceptEdits → `--mode accept-edits`; bypass → `--dangerously-skip-permissions`; default → **no flag** (agy's own prompting, described as "unreachable headless", `backend.go:110-118`) |
| `EnforcesReadOnlyPlan` | **false** — the descriptor at `registry.go:301-316` deliberately omits the field, documented at `registry.go:133-147` and pinned by `internal/lm/backends/capabilities_test.go:29`. **`plan` therefore collapses to `default`** via `CollapsePlanIfUnenforced` |
| Native per-tool deny list | **no support.** `NewSurfaces` (`surfaces.go:213`) never reads `in.DenyTools`. The nearest thing is the out-of-band ltk PreToolUse hook's `EncodeDeny` (`hooks_wire.go:111`) — a ctxloom mechanism, not an agy native deny list |
| Context surface | Native file, **whole-file write**, no hook: `.agents/AGENTS.md` via `WriteContext` → `writeManagedContext` (`antigravity.go:541`, `:575`; `surfaces.go:61`). agy fires **no SessionStart hook for context** — it reads `.agents/AGENTS.md` at session start, so the merge hash stays `""` (`backend.go:42-46`). All five surface kinds use `agent.ApproachUnsafeFile` (`surfaces.go:256-262`) |
| MCP | `.agents/mcp_config.json`, JSON `mcpServers` shape (same as claude's). Two paths: the delivery seam (`surfaces.go:88` → `antigravity.go:496`, ledger-tracked reconcile, per-run) and `MCPRegistrar` for taskloom. **Project scope ONLY** — `ConfigPath(dir, global=true)` returns `ErrNoGlobalMCPConfig`. **No per-invocation MCP flag**: `Chat` reports requested servers as an advisory status string — *"advisory (agy has no per-invocation MCP flag; see .agents/mcp_config.json)"* (`chat.go:281`) |
| Commands **and** skills | **One shared directory**: both write `.agents/skills/<name>/SKILL.md` (`capabilities.go:52` via `agent.RenderCommandAsSkillFile`; `skillfiles.go:36`; wired at `surfaces.go:220`, `:223`). Same-name collisions resolve **skill-wins** via `agent.NewSkillShapedCommandsAndSkills` → `FilterCommandsClaimedBySkills` (`surfaces.go:218`) |
| One-shot / resume | **Partial.** Resume-by-conversation exists *inside* `Chat`: `resolveChatConversationID` (`chat.go:297`) reads agy's own workspace→conversation-UUID cache and emits `--conversation <id> --continue`. But **`driving: oneshot` is not available**: antigravity is in `resumeCapableBackends` (`spawner.go:225-228`) and deliberately excluded from `oneShotSupportedBackends` (`spawner.go:249-252`), because it is LEGACY (go-plugin Chat dial, not `viaStartRun`) with no live loadSession confirm. `Resolve` **fails loud** (`spawner.go:371-375`) |
| Transcript | **None from this adapter.** `History()` is `nil`; agy's `transcript_full.jsonl` scraper was deleted after it mis-keyed the global brain store. **Residual private-format read**: `agyConversationMap` (`capabilities.go:95-153`) still parses agy's private `last_conversations.json` — a live continuation lookup, not a transcript, but it cuts against the never-scrape rule |
| Model + auth | `--model <m>` from `req.Model`; `ModelInfo{Provider: "google"}`. No forced fallback model — agy is closed-source and fast-moving, so its own default tier is used when nothing is pinned (`backend.go:68-71`). Auth is **ambient**: an OS-session-scoped D-Bus Secret Service keyring that `$HOME` does not gate at all (measured with `env -i` plus an empty fake HOME — still authenticated; `internal/lm/isolation/auth.go:595-609`), with a file-based fallback `~/.gemini/antigravity-cli/antigravity-oauth-token`. No `ANTIGRAVITY_*`/`AGY_*` env trigger is documented |
| Status | **Experimental.** The registry carries no `LIVE-UNTESTED` banner (unlike codex/kiro), but the package's own capability claims are stamped to agy **v1.0.7** while the verified install is **1.1.2** — and one v1.0.7 claim ("no plan mode") is already proven false |

## The plan-mode finding

This is the canonical example of a flag that parses and does nothing.

`buildArgs` emits `--mode plan` (`backend.go:110-112`) and agy accepts it. **LIVE
VERIFIED 2026-07-15, authenticated agy 1.1.2, real model turns** (`backend.go:98-109`;
restated for the chat path at `chat.go:44-49`):

- A sentinel-write probe under `--mode plan` — and again under `--mode plan --sandbox` — **overwrote the target file exactly like the bypass positive control**.
- The model self-reported *"I am not currently in plan mode or read-only mode."*

The compensation is `enforcesReadOnlyPlan: false` in the descriptor, which makes
`CollapsePlanIfUnenforced` (`internal/shared/agent/permissions.go:116-121`) return
`PermissionDefault` in place of `PermissionPlan` for antigravity at both call sites
(`internal/cli/run.go:1499`, `internal/operations/oneshot.go:417`). Flipping the
descriptor `true` would tell the resolver to trust a flag proven not to work.

A second permission fact: **mid-turn permission answers are inert**. `agy -p` never
forwards a permission request, so `req.ForwardPermissions` cannot be honored; the
posture is decided once at launch (`chat.go:41-43`).

## Isolation — refused on host, token-only in a container

**Host worktree isolation is REFUSED, not degraded.** antigravity is the sole entry
in `curatedHomeSpecs` (`internal/lm/isolation/curatedhome.go:131-136`) with
`authIsolated: false` and `workspaceViable: false`. It has no scoped isolation
variable — `$HOME` is the only lever, and `$HOME` carries no credentials
(`auth.go:595-609`). Measured 2026-07-22 against agy 1.1.5: **`agy -p` ignores the
launch cwd entirely** and always writes to its fixed global scratch
`~/.gemini/antigravity-cli/scratch/` (`curatedhome.go:94-100`, `:245`). Both escapes
are unfixable on the host, so `PrepareWorkspace` **refuses** a host worktree request
(`curatedHomeRefusal`).

**Container**: composable since 2026-07-15.
`containerProfileFor("antigravity")` (`internal/lm/isolation/profile.go:488-499`) uses
the official `install.sh`, validates with `agy --version` only (no ACP gate), sets
`transcriptStoreRel = ".gemini/antigravity-cli/brain"`, and **`overlayDirs = nil`**.

**The container-auth gap**: the keyring's UID-addressed socket `/run/user/<uid>/bus`
**does not exist inside the container's fresh mount and PID namespaces**, so a
containerized agy authenticates **only** via a seeded host file-based OAuth token,
copied read-write into the container at the identical
`~/.gemini/antigravity-cli/antigravity-oauth-token` path (`auth.go:324-401`). With no
such host token it **fails closed** — documented as deliberate, not a plumbing hole.
It has never been driven through a live container `agy -p` (`auth.go:353-356`), and
antigravity has **no `credentialSeedSpecs` entry** by design.

## Invariants

1. **`.agents/` is workspace-level and the ONLY hooks location.** `~/.gemini/antigravity-cli/hooks.json` is silently ignored, and a `hooks.json` under `~/.gemini/` or `~/.gemini/config/` **hangs headless agy before any hook executes** (`antigravity.go:73-75`).
2. **Unknown fields in `hooks.json` must round-trip** — agy rejects unknown fields elsewhere; hence `extra` + `marshalWithExtra` (`antigravity.go:112-208`).
3. **An absent `hooks.json` is equivalent to `{}`** — `saveHooksFile` deliberately does not create an empty file (`antigravity.go:290`).
4. **Hook event names are the literals** `"PreToolUse"` / `"PostToolUse"` / `"SessionStart"` / `"SessionEnd"` (`antigravity.go:393-403`).
5. **`Chat` and `Execute` can never diverge on permission mapping** — `chatArgs` reuses `buildArgs` (`chat.go:265`).
6. **Every surface is well-known-only**, so concurrent per-agent isolation requires a private cwd or a container cell (`surfaces.go:22-27`).
7. **`req.WorkDir` is the only reliable cwd in `Chat`** — the Chat gRPC path never calls `Setup`, so `BaseBackend.workDir` is unpopulated (`chat.go:50-56`).

## Divergences from documented or implied behavior

- **`--mode plan` is accepted and unenforced headlessly** (see above). The `EnforcesReadOnlyPlan: false` compensation is correct and deliberate.
- **A missing hash-addressed context file silently removes `.agents/AGENTS.md`.** `agent.ReadContextFile` returns `("", nil)` for a missing file (`internal/shared/agent/contextfile.go:175-178`), so `WriteManagedContext` removes the file (`antigravity.go:549-563`). The agent launches with zero context, exit 0, no warning.
- **`contextSurface.Deliver` with empty context returns success and a non-nil cleanup handle while writing zero bytes** (`surfaces.go:61-63`); `DeliverManagedContext` discards the `ContextReport` (`internal/shared/agent/managedcontext.go:120-128`).
- **An unparseable `hooks.json` becomes an empty struct that the caller then rewrites, destroying user hooks** (`antigravity.go:252-282`); `delete(raw, "hooks")` at `:277` also drops the raw bytes. The warning text understates it ("may not be preserved" — they are guaranteed not to be).
- **`resolveChatConversationID` discards the parse error** that `read()`'s own doc (`capabilities.go:132-135`) says must be surfaced (`chat.go:301-303`). If agy changes its cache format, every chat silently degrades to a fresh conversation per turn.
- **An empty `Command` writes a live-looking dead hook entry** `{"type":"command","name":"ctxloom-managed"}` (`antigravity.go:426-448`).
- **`AntigravityCommands` / `RegisterFromContent` are never invoked** — `LaunchBackend.commands` (`internal/shared/agent/launch_backend.go:64,92`) is write-only repo-wide.
- **Eight wire declarations have zero readers** (`hooks_wire.go:21-23`, `:34-35`, `:42`, `:77`) — `EventPreToolUse`/`PostToolUse`/`Stop`, `ToolViewFile`, `ToolListDir`, `HookPayload.ArtifactDirectoryPath`, `ToolArgs.DirectoryPath` — while `antigravity.go:393-403` hardcodes the same strings.
- **`EncodeDeny("")` emits `{"decision":"deny"}`** (`hooks_wire.go:111-113`), so the model is told "denied with reason: " and nothing.
- **Two comments assert the opposite of the code**: `skillfiles.go:10-23` claims flat `<name>.md` files and "This is not a collision" while the code writes `<name>/SKILL.md` and `surfaces.go:163-173` says they DO collide; `surfaces.go:159` cites `renderAntigravitySkillFile`, a function that does not exist anywhere.
- **Every package capability claim is stamped "verified agy v1.0.7"** while the verified install is 1.1.2 (`antigravity.go:8`, `:42`, `:73-75`; `hooks_wire.go:10`, `:19`; `mcp_registrar.go:12-16`). Treat "no global MCP config", "SessionStart entries silently skipped", "unknown fields hang headless agy", and "agy fails OPEN on non-zero hook exit" as unverified against 1.1.2.
- **The container profile sets `overlayDirs = nil` on a false premise**: `internal/lm/isolation/profile.go:494-497` justifies it as "antigravity's writers all target GLOBAL `~/.gemini/*` paths, not anything under the project dir", but every antigravity surface writes project-relative `.agents/…` files (`antigravity.go:67`, `:83`, `:532`, plus skills). The managed-config overlay shadowing kiro and claude receive is therefore absent for antigravity.
- **`--print-timeout` is sent as a Go duration string** (e.g. `"4m30s"`, `chat.go:209`); whether agy parses that format is unverified — a rejected value would either error the turn or be silently ignored.
- **`PermissionDefault` sends no flag** while every `Chat` turn goes through `-p` (`backend.go:118`), so a default-posture chat may block on an invisible prompt, bounded only by agy's 5-minute default.

## See also

[Capability matrix](capability-matrix.md) · [Backend abstraction](backend-abstraction.md) · [Plugin wire](grpc-wire.md) · [Isolation](isolation.md)
