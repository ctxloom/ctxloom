---
sidebar_position: 1
---

# Configuration

SCM uses YAML configuration files stored in the `.scm/` directory.

## Config Hierarchy

SCM uses a single source (no merging):

1. **Project**: `.scm/` at git repository root (if exists)
2. **Home**: `~/.scm/` (fallback if no project .scm)

When in a project with `.scm/`, only that project's config and bundles are used.

## config.yaml

The main configuration file at `.scm/config.yaml`:

```yaml
lm:
  plugins:
    claude-code:
      default: true
      args: ["--dangerously-skip-permissions"]
    gemini:
      args: ["--yolo"]

defaults:
  profile: developer
  use_distilled: true
```

## LM Plugins

SCM uses plugins to interface with language models:

| Plugin | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude CLI (default) |
| `gemini` | [Gemini CLI](https://github.com/google/generative-ai-cli) | Google's Gemini CLI |
| `codex` | [Codex CLI](https://github.com/openai/codex) | OpenAI's Codex CLI (**provisional**) |

### Switch Plugins

```bash
# Use Gemini instead of Claude
scm run -l gemini "help with this code"
```

## Claude Code Integration

The `claude-code` plugin writes assembled context to files that Claude Code reads:

1. Writes context to `.scm/context/[hash].md`
2. Updates `CLAUDE.md` with a managed section containing the include reference

The managed section is delimited by `<!-- SCM:BEGIN -->` and `<!-- SCM:END -->`. SCM only modifies content within these markers.

## Directory Structure

```
.scm/
├── config.yaml          # Main configuration
├── bundles/             # Bundle YAML files
│   ├── my-bundle.yaml
│   └── another.yaml
├── profiles/            # Profile YAML files
│   └── developer.yaml
├── context/             # Generated context files
│   └── [hash].md
├── remotes.yaml         # Configured remote sources
└── lock.yaml            # Dependency lock file
```

## Defaults

Set default profile and behavior:

```yaml
defaults:
  profile: developer        # Default profile when none specified
  use_distilled: true      # Use distilled content by default
```

## Plugin Arguments

Pass arguments to LM CLI tools:

```yaml
lm:
  plugins:
    claude-code:
      args: ["--dangerously-skip-permissions"]
    gemini:
      args: ["--yolo"]
```
