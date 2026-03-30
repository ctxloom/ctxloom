---
sidebar_position: 1
---

# Configuration

SCM uses YAML configuration files stored in the `.scm/` directory.

## Directory Structure

```
.scm/
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

SCM uses a single source (no merging):

1. **Project**: `.scm/` at git repository root
2. **Home**: `~/.scm/` (fallback if no project .scm)

## config.yaml Reference

```yaml
# Editor configuration
editor:
  command: "vim"                 # Editor command
  args: ["-c", "set number"]     # Additional arguments
  # Fallback: VISUAL env → EDITOR env → nano

# Language model plugins
llm:
  plugin_paths: []               # Additional plugin directories
  plugins:
    claude-code:
      model: "claude-opus-4-5"   # Default model
      binary_path: "/path/to/bin"
      args: []                   # Plugin-specific arguments
      env:                       # Environment variables
        CUSTOM_VAR: "value"
    gemini:
      model: "gemini-2.0-flash"

# Default settings
defaults:
  llm_plugin: "claude-code"      # Default LLM plugin
  profiles:                      # Default profiles to load
    - scm-main/go-developer
    - scm-main/code-reviewer
  use_distilled: true            # Prefer distilled versions (default: true)

# Sync configuration
sync:
  auto_sync: true                # Auto-sync on startup (default: true)
  lock: true                     # Update lockfile after sync (default: true)
  apply_hooks: true              # Apply hooks after sync (default: true)

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
  auto_register_scm: true        # Auto-register SCM's MCP server
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

# Inline profiles (alternative to .scm/profiles/)
profiles:
  my-profile:
    description: "Inline profile"
    default: false
    parents: []
    tags: []
    bundles: []
    variables:
      VARIABLE: "value"
```

## LM Plugins

Available plugins:

| Plugin | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude (default) |
| `gemini` | [Gemini CLI](https://github.com/google/generative-ai-cli) | Google's Gemini |
| `codex` | [Codex CLI](https://github.com/openai/codex) | OpenAI (provisional) |

### Plugin Configuration

```yaml
llm:
  plugins:
    claude-code:
      model: "claude-opus-4-5"
      args: ["--dangerously-skip-permissions"]
      env:
        ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}"
```

## Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| `use_distilled` | `true` | Prefer distilled content |
| `auto_sync` | `true` | Sync remotes on startup |
| `llm_plugin` | `claude-code` | Default AI backend |
| `auto_register_scm` | `true` | Register SCM MCP server |

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

The `claude-code` plugin:

1. Writes context to `.scm/context/[hash].md`
2. Updates `CLAUDE.md` with managed section

The managed section is delimited by:
```markdown
<!-- SCM:BEGIN -->
...generated content...
<!-- SCM:END -->
```

SCM only modifies content within these markers.

## Sync Configuration

```yaml
sync:
  auto_sync: true      # Sync on MCP server startup
  lock: true           # Update lock.yaml after sync
  apply_hooks: true    # Apply hooks after sync
```

### Lockfile

The `lock.yaml` records installed remote items for reproducible installations:

```bash
scm remote lock        # Generate lockfile
scm remote sync        # Sync from lockfile
```

## Memory Configuration

Session memory preserves context across conversations:

```yaml
memory:
  enabled: true                # Enable session memory
  mode: lazy                   # lazy or eager

  compaction:
    plugin: claude-code        # LLM plugin for distillation
    model: haiku               # Model (fast + cheap)
    chunk_size: 8000           # Tokens per chunk

  load_on_start: false         # Auto-load on session start (eager mode)

  vectors:
    enabled: true              # Enable vector search
    model_path: ~/.scm/models/all-MiniLM-L6-v2.onnx
```

### Memory Modes

| Mode | Behavior |
|------|----------|
| `lazy` | Manual compaction via `/save`. Vector DB for retrieval. |
| `eager` | Same as lazy, but auto-loads distilled memory on session start. |

See [Memory Guide](/guides/memory) for usage details.
