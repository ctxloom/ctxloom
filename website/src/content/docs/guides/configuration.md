---
title: "Configuration"
---

ctxloom uses YAML configuration files stored in the `.ctxloom/` directory.

## Directory Structure

```
.ctxloom/
├── config.yaml              # Main configuration
├── bundles/                 # Bundle YAML files
│   ├── my-bundle.yaml
│   └── remote-name/         # Pulled remote bundles
│       └── bundle.yaml
├── profiles/                # Profile YAML files
│   ├── developer.yaml
│   └── team/
│       └── backend.yaml
├── remotes.yaml             # Remote registry
├── lock.yaml                # Dependency lockfile
└── .auth/                   # Git authentication
```

## Config Hierarchy

ctxloom uses a single source (no merging):

1. **Project**: `.ctxloom/` at git repository root
2. **Home**: `~/.ctxloom/` (fallback if no project .ctxloom)

## config.yaml Reference

```yaml
# Editor configuration
editor:
  command: "vim"                 # Editor command
  args: ["-c", "set number"]     # Additional arguments
  # Fallback: VISUAL env → EDITOR env → nano

# Language model configuration — every LLM setting lives under `llm:`
llm:
  default: claude-code           # Default LLM backend
  model: opus                    # Default model (optional)
  configs:                       # Per-LLM overrides (optional)
    claude-code:
      binary_path: "/path/to/bin"
      model: "claude-opus-4-5"
      args: []                   # Extra CLI arguments
      env:                       # Environment variables
        CUSTOM_VAR: "value"
    antigravity:
      type: antigravity          # Antigravity CLI (binary: agy)
      model: "gemini-3-pro"      # Optional; agy's own default when unset
  compaction:                    # LLM used for distillation (optional)
    llm: claude-code
    model: haiku
    chunks: 8000

# Default settings
defaults:
  profiles:                      # Default profiles to load
    - ctxloom-default/go-developer
    - ctxloom-default/code-reviewer
  use_distilled: true            # Prefer distilled versions (default: true)

# Sync configuration
sync:
  auto_sync: true                # Auto-sync on startup (default: true)

# Hooks configuration
hooks:
  unified:                       # Backend-agnostic hooks
    pre_tool: []
    post_tool: []
    session_start: []
    session_end: []
    pre_shell: []
    post_file_edit: []
  plugins:                       # Backend-specific hooks
    claude-code:
      EventName: []

# MCP Server configuration
mcp:
  auto_register_ctxloom: true        # Auto-register ctxloom's MCP server
  servers:                       # Unified MCP servers (all backends)
    my-server:
      command: "npx my-mcp"
      args: ["--flag"]
      env:
        ENV_VAR: "value"
  plugins:                       # Backend-specific servers
    claude-code:
      server-name:
        command: "..."

# Inline profiles (alternative to .ctxloom/profiles/)
profiles:
  my-profile:
    description: "Inline profile"
    parents: []
    tags: []
    bundles: []
    variables:
      VARIABLE: "value"

# To make a profile load by default, list it under defaults.profiles above.
```

## LLMs

Available LLM backends:

| LLM | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude (default) |
| `antigravity` | [Antigravity CLI](https://antigravity.google) (`agy`) | Google's Antigravity |
| `codex` | [Codex CLI](https://github.com/openai/codex) | OpenAI (provisional) |

Select the default with `llm.default` (or per-run with `--llm <name>`). Override a
backend's binary, model, args, or environment under `llm.configs`:

```yaml
llm:
  default: claude-code
  configs:
    claude-code:
      model: "claude-opus-4-5"
      args: ["--dangerously-skip-permissions"]
      env:
        ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
```

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
| `use_distilled` | `true` | Prefer distilled content |
| `auto_sync` | `true` | Sync remotes on startup |
| `llm.default` | `claude-code` | Default LLM backend |
| `auto_register_ctxloom` | `true` | Register ctxloom MCP server |
| `statusline` | `true` | Manage the ctxloom HUD statusline (set `false` to keep your own) |

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

Context is written to `.ctxloom/context/[hash].md` and injected via hook. For
the Antigravity backend (which has no SessionStart event), context is delivered
via a ctxloom-managed section in `.agents/AGENTS.md` instead. See
[Hooks and Context Injection](/guides/hooks) for details.

## Sync Configuration

```yaml
sync:
  auto_sync: true      # Sync on MCP server startup
  lock: true           # Update lock.yaml after sync
  apply_hooks: true    # Apply hooks after sync
```

### Lockfile

The `lock.yaml` records the resolved remote items for reproducible pulls. It is
updated automatically whenever you pull or update:

```bash
ctxloom remote pull        # Fetch referenced content and update lock.yaml
```

## Memory Configuration

Session memory is always enabled. Compaction settings live under `llm.compaction`:

```yaml
llm:
  compaction:
    llm: claude-code   # LLM used for distillation
    model: haiku       # Model (fast + cheap)
    chunks: 8000       # Tokens per chunk
```

See [Session Memory Guide](/getting-started/memory) for usage details.
