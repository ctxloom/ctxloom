# Wire types — hooks and MCP

`internal/shared/wire` is the engine-agnostic vocabulary for the two things ctxloom delivers into every backend — lifecycle hooks and MCP server registrations — plus the three operations the assembly pipeline needs on them (merge, append, default-resolve). It is a true leaf: zero internal imports, twelve internal importers, two files, no cross-talk between them.

The contract it owns: **these six types are simultaneously the on-disk config shape, the gRPC payload shape, and the input shape for every engine's native settings writer.** A field added here has to be threaded through four hand-written translation layers that no compiler check binds together.

```mermaid
flowchart TD
    subgraph wire["internal/shared/wire"]
        HC["HooksConfig<br/>Unified + Plugins"]
        UH["UnifiedHooks<br/>6 event slices"]
        BH["BackendHooks<br/>map[event][]Hook"]
        H["Hook<br/>Matcher/Command/Type/Prompt/Timeout/Async<br/>SCM · ContextHash* · PreToolFallback"]
        MC["MCPConfig<br/>AutoRegisterCtxloom *bool + Servers + Plugins"]
        MS["MCPServer<br/>Command/Args/Env/Notes/Installation/SCM"]
        HC --> UH
        HC --> BH
        UH --> H
        BH --> H
        MC --> MS
    end

    YAML["config.yaml · profiles · bundles<br/>yaml.v3 + hand-authored JSON Schema"] -->|decode| HC
    YAML -->|decode| MC
    HC -->|"UnifiedHooks.Append / HooksConfig.HasAny"| ASM["internal/lm/backends<br/>AssembleManagedConfig"]
    MC -->|MergeMCPConfig| ASM
    ASM --> PROTO["internal/lm/grpc<br/>hookToProto / hookFromProto / mcpServerToProto<br/>LOSSY: PreToolFallback has no proto field"]
    PROTO --> LIFE["internal/shared/agent<br/>BaseLifecycle.MergeManaged<br/>MergeHooksConfig lives HERE, not in wire"]
    LIFE --> WRITERS["engine writers<br/>claude · codex · antigravity · kiro · opencode"]
    WRITERS --> NATIVE["settings.json · config.toml · opencode.json"]

    CHAT["internal/shared/agent.ChatMCPServer<br/>Transport/URL/Headers — remote MCP"]
    MS -.->|"ComposeChatMCPServers hard-codes stdio<br/>chat_mcp.go:33-35"| CHAT

    style PROTO fill:#fdd,stroke:#c00
```

`*` `Hook.ContextHash` is in-process only (`yaml:"-" json:"-"`) and is deliberately re-derived agent-side by `agent.NewContextInjectionHooks` (`internal/shared/agent/context_hooks.go:24-76`), so its non-serialization is compensated. `PreToolFallback` has no such compensation.

## `internal/shared/wire` — types

### `Hook` — `internal/shared/wire/hooks.go:14`

One lifecycle action (shell command, prompt, or agent invocation) plus the metadata each engine writer needs to place and re-identify it.

| Field | file:line | Serialized as | Read by |
|---|---|---|---|
| `Matcher string` | `hooks.go:15` | `matcher` | every engine writer |
| `Command string` | `hooks.go:16` | `command` | every engine writer |
| `Type string` | `hooks.go:17` | `type` | every engine writer; free-form, `"command"`/`"prompt"`/`"agent"` by convention, no constant and no validation in this package |
| `Prompt string` | `hooks.go:18` | `prompt` | every engine writer |
| `Timeout int` | `hooks.go:19` | `timeout` | claude only (`internal/claude/claude.go:613`) and codex (`internal/codex/settings.go:360`) |
| `Async bool` | `hooks.go:20` | `async` | claude only (`internal/claude/claude.go:614`) |
| `SCM string` | `hooks.go:21` | `_ctxloom` | the remove-all-then-re-add reconciler (`internal/claude/claude.go:567,834`) |
| `ContextHash string` | `hooks.go:30` | never (`yaml:"-" json:"-" mapstructure:"-"`) | `internal/antigravity/antigravity.go:381`, `internal/kiro/settings.go:136` |
| `PreToolFallback bool` | `hooks.go:38` | `pre_tool_fallback` | `internal/antigravity/antigravity.go:388` only |

### `UnifiedHooks` — `internal/shared/wire/hooks.go:42`

The backend-agnostic six-event bundle. All six are `[]Hook` and are only ever touched as a group.

| Field | file:line | Serialized as |
|---|---|---|
| `PreTool` | `hooks.go:43` | `pre_tool` |
| `PostTool` | `hooks.go:44` | `post_tool` |
| `SessionStart` | `hooks.go:45` | `session_start` |
| `SessionEnd` | `hooks.go:46` | `session_end` |
| `PreShell` | `hooks.go:47` | `pre_shell` |
| `PostFileEdit` | `hooks.go:48` | `post_file_edit` |

### `HooksConfig` — `internal/shared/wire/hooks.go:52`

The persisted hook document.

| Field | file:line | Serialized as |
|---|---|---|
| `Unified UnifiedHooks` | `hooks.go:53` | `unified` |
| `Plugins map[string]BackendHooks` | `hooks.go:54` | `plugins` |

### `BackendHooks` — `internal/shared/wire/hooks.go:76`

Named map type, `map[string][]Hook`. Keys are engine-*native* event names (`"PreToolUse"` for Claude Code, `"beforeShellExecution"` for Cursor). Constructed at eight production sites: `internal/config/config_resolve.go:61,188`, `internal/shared/agent/context_hooks.go:105,109`, `internal/lm/backends/managed.go:234,409`, `internal/lm/grpc/managed.go:190,193`.

### `MCPServer` — `internal/shared/wire/mcp.go:10`

One MCP server registration.

| Field | file:line | Serialized as | Notes |
|---|---|---|---|
| `Command string` | `mcp.go:11` | `command` | executable surface; read by every writer and by `cloneMCPServer` |
| `Args []string` | `mcp.go:12` | `args` | deep-copied by `cloneMCPServer` |
| `Env map[string]string` | `mcp.go:13` | `env` | deep-copied by `cloneMCPServer` |
| `Notes string` | `mcp.go:14` | `notes` | human-only, explicitly not sent to AI; read by `internal/cli/bundle_list.go:244`, `internal/operations/review.go:396` |
| `Installation string` | `mcp.go:15` | `installation` | human-only; same readers |
| `SCM string` | `mcp.go:16` | `_ctxloom` | ctxloom-managed marker |

### `MCPConfig` — `internal/shared/wire/mcp.go:20`

The persisted MCP document.

| Field | file:line | Serialized as | Notes |
|---|---|---|---|
| `AutoRegisterCtxloom *bool` | `mcp.go:23` | `auto_register_ctxloom` | tri-state: unset / true / false. Read only by `ShouldAutoRegisterCtxloom`; written by `MergeMCPConfig` |
| `Servers map[string]MCPServer` | `mcp.go:26` | `servers` | unified servers |
| `Plugins map[string]map[string]MCPServer` | `mcp.go:30` | `plugins` | per-backend passthrough servers |

## `internal/shared/wire` — functions

| Function | file:line | Purpose | Call sites |
|---|---|---|---|
| `(HooksConfig).HasAny() bool` | `internal/shared/wire/hooks.go:59` | True if any of the six unified slices (`:61`) or any plugin event slice is non-empty. "Plugin present but empty" is a distinguished case | 1 production: `internal/config/config_save.go:288`. Note `internal/config/config_bundles.go:650` calls a *different* method, `bundles.BundleHooks.HasAny` (`internal/bundles/bundles.go:165`) |
| `(*UnifiedHooks).Append(other UnifiedHooks)` | `internal/shared/wire/hooks.go:79` | Concatenates each of the six per-event slices from `other` onto the receiver (`:80-85`) | 5 production: `internal/config/config_bundles.go:262,270,289,468`, `internal/lm/backends/managed.go:279` |
| `(*MCPConfig).ShouldAutoRegisterCtxloom() bool` | `internal/shared/wire/mcp.go:35` | Resolves the tri-state, defaulting true; **nil-receiver safe** | 7 production: `internal/codex/settings.go:443`, `internal/claude/claude.go:695`, `internal/opencode/settings.go:445`, `internal/operations/manage.go:125`, `internal/operations/mcp_servers.go:57`, `internal/shared/agent/chat_mcp.go:38`, `internal/shared/agent/mcpfile.go:86` |
| `MergeMCPConfig(dest, src *MCPConfig)` | `internal/shared/wire/mcp.go:49` | Merges `src` into `dest` — later wins per server name — deep-copying each server via `cloneMCPServer` (`:64`, `:76`). Guard at `:50`; `AutoRegisterCtxloom` assigned at `:56`; maps allocated at `:60-70` | 6 production: `internal/profiles/profiles.go:852,935`, `internal/config/config_resolve.go:164`, `internal/lm/backends/managed.go:128,134,146`, `internal/shared/agent/base_lifecycle.go:55` |
| `cloneMCPServer(s MCPServer) MCPServer` | `internal/shared/wire/mcp.go:84` | Copies an `MCPServer`, duplicating `Args` and `Env` so the copy never aliases | 2, both in-file. Semantically identical twins exist at `internal/config/accessors.go:120` and, as proto converters, at `internal/lm/grpc/managed.go:204-227` |

## Invariants and contracts

**Direction of flow**

- One way, always: `internal/config` + `internal/profiles` + `internal/bundles` parse user YAML into these structs → `internal/lm/backends.AssembleManagedConfig` folds them into one `ManagedConfig` → `internal/lm/grpc` serializes that onto `RunStart` → `internal/shared/agent.BaseLifecycle` re-merges it agent-side → the five engine packages translate it into each engine's native settings file.
- **The host-side assembly (`ApplyHooks`) and the agent-side assembly (run) must produce identical hooks**, or the remove-all-then-re-add reconcile drops them; this is documented at `internal/shared/agent/base_lifecycle.go:26-38`.
- `Hook.SCM` (`_ctxloom`) is the key that reconcile identifies ctxloom-managed entries by. `MCPServer.SCM` is its MCP twin.

**Serialization**

- These types carry **no version field**. Schema evolution happens one layer out, in the document-level upgraders (`internal/config/upgrade.go`, `internal/bundles/upgrade.go`, `internal/profiles/upgrade.go`) plus the hand-authored JSON Schema.
- **Unknown YAML keys are not rejected here** — yaml.v3 without `KnownFields` ignores them silently. They are caught only by `additionalProperties: false` in `resources/schema/input/config-schema.json`, whose drift gate (`internal/config/schema_drift_test.go`) covers **top-level keys only**. Round-tripping is total only for the fields the outer schema happens to know about.
- Schema asymmetry to know about: the `mcpServer` def is `additionalProperties: false` and does **not** list `_ctxloom`, while the `hook` def is `additionalProperties: true` (which is how the same marker is tolerated on hooks). No production writer currently persists an SCM-marked server, so this is latent.
- Tag sets are inconsistent: `Hook` and `MCPServer` carry `json` tags; `UnifiedHooks`, `HooksConfig`, `BackendHooks`, and `MCPConfig` carry none, so a `json.Marshal` of any container would emit Go field names (`"PreTool"`, `"AutoRegisterCtxloom"`) while its elements emit snake/marker names. No production code marshals the containers today.
- Real vs documented: every `mapstructure` tag in this package (~13) is dead metadata. viper was deliberately removed (`internal/shared/confload/confload.go:90`, `internal/config/config.go:1400`), the only non-test `mapstructure.Decode` (`internal/lm/backends/config_registry.go:37`) decodes `agent.BackendConfig` implementations rather than wire types, and docs are generated from the JSON Schema (`internal/docsgen/config.go:13-18`) rather than from tags. `hooks.go:30`'s `mapstructure:"-"` is an exclusion tag for a decoder that never runs.
- Real vs documented: `Hook.PreToolFallback` **does not survive the gRPC round-trip**. The proto `Hook` message (`internal/lm/grpc/llm.proto:447-455`) has fields 1-7 (`matcher, command, type, prompt, timeout, async, scm`) and no `pre_tool_fallback`; `hookToProto` (`internal/lm/grpc/managed.go:91-101`) and `hookFromProto` (`:104-115`) both omit it. Every `ctxloom run` ships this path (`internal/cli/run.go:984`, `internal/operations/oneshot.go:405`). The flag is set from bundles at `internal/config/config_bundles.go:685`, is the only thing `internal/antigravity/antigravity.go:388` keys on, and is part of the **signed** executable preimage (`internal/bundles/bundles.go:632-641`, `internal/lm/backends/managed.go:466`), so a delivered hook no longer matches the bytes its trust grant signed.

**Merging**

- `MergeMCPConfig` semantics: **later wins per server name**, and each merged server is deep-copied so `dest` never aliases `src`'s `Args` or `Env` slices/maps. The aliasing this prevents is real — callers inject env vars post-merge (`internal/agentcoord/coord/enginehost.go:665-681`).
- `MergeMCPConfig(dest, nil)` is a deliberate no-op ("no MCP declared"), and every production caller guards it anyway (`internal/shared/agent/base_lifecycle.go:54`).
- `MergeMCPConfig(nil, src)` **silently discards the entire src payload** rather than panicking — the same `if src == nil || dest == nil { return }` guard covers both. No live call site passes a nil dest (five take `&struct`; the sixth is guarded by `ensureMCP()` at `base_lifecycle.go:42`), and `mcp_test.go:29-32` enshrines the behaviour as "must not panic".
- Real vs documented: the doc promises `dest` is independent of `src`, but `AutoRegisterCtxloom` is copied as a **shared `*bool`** (`mcp.go:56`). `internal/config/accessors.go:149` (`cloneBoolPtr`) does duplicate it; the only in-tree mutator assigns a fresh pointer (`internal/operations/mcp_servers.go:412`), so there is no live write-through today.
- `dest.Servers` and `dest.Plugins` are allocated **unconditionally** (`mcp.go:60-70`), so merging an empty-but-non-nil `src` converts "nil = nothing declared" into "empty map = declared, empty". That distinction is read elsewhere: `internal/config/config_resolve.go:247`, `internal/operations/mcp_servers.go:263,276`, and `internal/lm/grpc/managed.go:228-232` (whose doc explicitly preserves nil to match the host's "no bundle servers" shape).
- `ShouldAutoRegisterCtxloom`'s **nil-receiver safety is load-bearing**: `internal/shared/agent/chat_mcp.go:38` calls it before its own `if mcp != nil` guard at line 41.
- `UnifiedHooks.Append(UnifiedHooks{})` is a correct no-op; whether zero hooks is an error is decided by the caller (`ResolveBundleHooks`), not here.
- **The hooks half has no merge primitive in this package.** `MergeHooksConfig` lives at `internal/shared/agent/context_hooks.go:89-114` and its lines 95-100 duplicate `Append`'s six `append` calls verbatim, while the MCP twin (`MergeMCPConfig`) lives here. `MergeHooksConfig` has no config coupling — it references only `wire` types.

**Shape limits**

- `MCPServer` can express **only a stdio (command) server**. Remote MCP exists end-to-end through a second, parallel type — `internal/shared/agent.ChatMCPServer` (`chat.go:248-267`) with `Transport`/`URL`/`Headers`, consumed by `internal/opencode/settings.go:133-135` and `internal/acp/session.go:524` — but everything sourced from config, profiles, or bundles goes through `ComposeChatMCPServers` (`internal/shared/agent/chat_mcp.go:33-35`), which always builds the stdio form. Two MCP representations coexist with asymmetric capability.
- `ComposeChatMCPServers` passes `s.Env` through **without cloning**, bypassing the aliasing protection `MergeMCPConfig` provides. `internal/agentcoord/coord/enginehost.go:665-681` then writes into `servers[i].Env`; it is safe only because the mutation targets the entry named `agent.MCPServerName`, which is constructed with a nil `Env` and gets a fresh map.
- Three of eight `Hook` fields are silently ignored by most consumers, and **no consumer declares which fields it honours**. Adding a seventh unified event means editing roughly eight places: this type, the proto `UnifiedHooks` (`internal/lm/grpc/llm.proto:464-471`), the JSON Schema `unifiedHooks` def, and the six-way switch in every engine writer.
- `Hook`'s field set is connascent-by-algorithm with `bundles.BundleHook`, which hashes `Matcher+Type+Command+Prompt+PreToolFallback` as the signed preimage a trust grant binds to (`internal/bundles/bundles.go:632-641`), and with the proto `Hook` message. All three must gain a field together and nothing enforces it.
