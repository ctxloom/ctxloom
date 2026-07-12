---
title: "Configuration"
---

Every `ctxloom run -f go-development -f testing-patterns -p backend-developer` is a flag set you'd otherwise have to remember and retype each session. Put the same choices in `.ctxloom/config.yaml` instead and commit it: everyone on the project runs plain `ctxloom run` and gets the same fragments and profile, without needing to know which flags this repo requires.

ctxloom's configuration lives in YAML files under the `.ctxloom/` directory.

## Directory Structure

```
.ctxloom/
├── config.yaml              # Main configuration
├── remotes.yaml             # Remote registry (and custom forges)
├── lock.yaml                # Dependency lockfile
├── trust.yaml               # Trust grants and blacklist
├── profiles/                # Profile YAML files
│   └── developer.yaml
├── agents/                  # Agent bindings (alternative to config.yaml agents:)
│   └── dev.yaml
├── local/                   # Committed local bundle overrides
│   └── bundles/
├── cache/                   # Fetched and generated state (gitignored)
│   ├── bundles/             # Local + pulled bundle YAML files
│   ├── repos/               # Remote git clones
│   └── context/             # Assembled context files
└── sessions/                # Distilled session summaries
```

## Config Hierarchy

ctxloom uses a single source (no merging):

1. **Project**: `.ctxloom/` at git repository root
2. **Home**: `~/.ctxloom/` (fallback if no project .ctxloom)

## config.yaml Reference

The current schema is version 4. The canonical commented example ships as `resources/example-config.yaml` in the repo; `ctxloom manage config init` scaffolds one.

```yaml
version: 4

# Language model configuration.
# `llm.configs` is a registry of arbitrarily-labeled backend configs — the
# backend is determined ONLY by each entry's `type`. `llm.defaults` maps a
# role to the label that plays it. ctxloom ships a built-in primary
# (claude-code) and fast config (claude-code on haiku), so this block is
# only needed to change models, binaries, or role pairings.
llm:
  configs:
    big:   { type: claude-code, model: claude-opus-4-8 }
    quick: { type: claude-code, model: claude-haiku-4-5-20251001 }
    g:     { type: antigravity }         # Antigravity CLI (agy); model optional
  defaults:
    primary: big      # coding/interactive role → label
    fast: quick       # compression role (distill, compaction) → label

# Behavioral settings
config:
  use_distilled: true         # prefer distilled fragment versions (default true)
  compaction_chunks: 8000     # target tokens per compaction chunk
  statusline: true            # let ctxloom manage the HUD statusline

# Editor (fallback: VISUAL env → EDITOR env → nano)
editor:
  command: "vim"
  args: []

# Profiles: the default list to load, plus inline definitions
profiles:
  defaults:                   # a LIST — multiple defaults compose
    - developer
  definitions:                # inline profiles (alternative to .ctxloom/profiles/)
    my-profile:
      description: "Inline profile"
      parents: []
      bundles: []
      variables:
        VARIABLE: "value"

# Agents: local engine↔profile bindings (see the Agents concept page)
agents:
  dev:
    engine: claude-code
    profiles: [developer]
    runtime: container        # optional; host|container

# Project-wide isolation defaults
workspace: none               # session workspace axis: none|worktree
runtime: host                 # agent runtime axis: host|container

# Container-image overrides for containerized agents
isolation_base_containerfile: .ctxloom/base.Containerfile   # your base stage
isolation_images:             # fully user-provided images, run as-is
  claude-code: my-registry/claude-agent:latest

# Sync configuration
sync:
  auto_sync: true             # sync referenced remotes on startup (default true)

# Hooks configuration
hooks:
  unified:                    # backend-agnostic hooks
    pre_tool: []
    post_tool: []
    session_start: []
    session_end: []
    pre_shell: []
    post_file_edit: []
  plugins:                    # backend-specific hooks
    claude-code:
      EventName: []

# MCP Server configuration
mcp:
  auto_register_ctxloom: true # auto-register ctxloom's own MCP server
  servers:                    # unified MCP servers (all backends)
    my-server:
      command: "npx my-mcp"
      args: ["--flag"]
      env:
        ENV_VAR: "value"
  plugins:                    # backend-specific servers
    claude-code:
      server-name:
        command: "..."
```

## LLMs

Registered LLM backends:

| Backend | CLI | Description |
|---------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude (default) |
| `antigravity` | [Antigravity CLI](https://antigravity.google) (`agy`) | Google's Antigravity |
| `codex` | [Codex CLI](https://github.com/openai/codex) | OpenAI Codex |
| `kiro` | Kiro | AWS Kiro (chat rides its ACP adapter) |
| `acp` | any ACP agent | Generic Agent Client Protocol backend descriptor |

**Status:** `codex` and `kiro` are implemented and hermetically tested; live operation is untested (requires a codex/kiro account, which the maintainers do not currently have). Model selection is accepted by kiro-cli but its honoring is unverified.

A **config label** is an arbitrary name for a fully-specified backend config; the backend is chosen by the entry's `type`. Two labels can point at the same backend with different models (e.g. a `big` and a `quick` claude-code). Set the interactive default with `llm.defaults.primary` (or per-run with `--llm <label>`), and the compression role with `llm.defaults.fast`:

```yaml
llm:
  configs:
    claude-code:
      type: claude-code
      model: "claude-opus-4-8"
      binary_path: "/path/to/bin"   # optional
      args: []                      # extra CLI arguments
      env:
        CUSTOM_VAR: "value"
  defaults:
    primary: claude-code
```

`ctxloom llm list` shows the available backends; `ctxloom llm default <label>` sets the primary.

### Antigravity

The `antigravity` backend wraps Google's Antigravity CLI (`agy`,
`curl -fsSL https://antigravity.google/cli/install.sh | bash`). Authentication
is Google OAuth handled by `agy` itself — there are no API key environment
variables to configure. Supported fields are `model` (optional; agy's own
default is used when unset), `binary_path` (default `agy`), `args`, and `env`:

```yaml
llm:
  configs:
    antigravity:
      type: antigravity
      model: "gemini-3-pro"    # Optional
      binary_path: "agy"       # Optional
```

:::note[Migrating from the gemini backend]
Google discontinued Gemini CLI in June 2026, and ctxloom's `gemini` backend was
replaced by `antigravity` (config version 4). Older v3 configs with
`type: gemini` are auto-migrated on load: the type flips to `antigravity`, and
the gemini-only `trust_workspace` and `approval_mode` knobs (which have no
antigravity equivalent) are dropped.
:::

## Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| `config.use_distilled` | `true` | Prefer distilled content |
| `config.compaction_chunks` | `8000` | Tokens per compaction chunk |
| `config.statusline` | `true` | Manage the ctxloom HUD statusline (set `false` to keep your own) |
| `sync.auto_sync` | `true` | Sync remotes on startup |
| `llm.defaults.primary` | `claude-code` | Default LLM backend |
| `mcp.auto_register_ctxloom` | `true` | Register ctxloom's MCP server |
| `workspace` | `none` | Session workspace axis default |
| `runtime` | `host` | Agent runtime axis default |

## Agents and Isolation

The `agents:`, `workspace:`, `runtime:`, `isolation_images:`, and
`isolation_base_containerfile:` keys configure local agent bindings and where
they execute. See [Agents & Isolation](/concepts/agents/) for the model, and
prefer `ctxloom agent set` over hand-editing the `agents:` key.

## Hooks

Hook types available:

| Hook | When |
|------|------|
| `pre_tool` | Before tool execution |
| `post_tool` | After tool execution |
| `session_start` | Session initialization |
| `session_end` | Session cleanup |
| `pre_shell` | Before shell execution |
| `post_file_edit` | After file edit |

Hook structure:

```yaml
hooks:
  unified:
    session_start:
      - matcher: ".*"           # Regex pattern
        command: "echo hello"   # Shell command
        type: "command"         # command, prompt, or agent
        timeout: 30             # Seconds
        async: false            # Run in background
```

## Claude Code Integration

ctxloom injects context via **SessionStart hooks** rather than editing `CLAUDE.md`. This approach:

- Keeps `CLAUDE.md` clean for your own project documentation
- Injects fresh context at the start of each session

Context is written to `.ctxloom/cache/context/[hash].md` and injected via hook. For
the Antigravity backend (which has no SessionStart event), context is delivered
via a ctxloom-managed section in `.agents/AGENTS.md` instead. See
[Hooks and Context Injection](/guides/hooks) for details.

## Sync Configuration

```yaml
sync:
  auto_sync: true      # Sync on startup (the only sync setting)
```

### Lockfile

The `lock.yaml` records the resolved remote items for reproducible pulls. It is
updated automatically whenever you pull or upgrade:

```bash
ctxloom remote pull        # Fetch referenced content and update lock.yaml
```

## Memory Configuration

Session memory is always enabled. The compaction/distillation model is the
**fast role** (`llm.defaults.fast`), and chunking is a behavioral setting:

```yaml
llm:
  defaults:
    fast: quick              # config label used for distillation
config:
  compaction_chunks: 8000    # tokens per chunk
```

See [Session Memory Guide](/getting-started/memory) for usage details.
