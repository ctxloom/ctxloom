---
slug: /
sidebar_position: 1
---

# SCM - Sophisticated Context Manager

## Why Use SCM?

**Stop repeating yourself to AI.** Every session you re-explain your coding standards, patterns, and preferences. SCM solves this:

- **Save hours per week** - Write context once, reuse across all sessions
- **Consistent across projects** - Personal repo (`~/.scm`) follows you everywhere
- **Share across team** - Pull bundles from GitHub, everyone gets the same context
- **Never lose progress** - Session memory survives `/clear`
- **Portable context** - Write code with Claude, review with Gemini, same context

:::note Why not just CLAUDE.md?
Claude Code has `CLAUDE.md`, Gemini has `.gemini/settings.yaml` - but these are **project-level only** and encourage intermingling project-specific context with general best practices. SCM separates concerns: personal standards in `~/.scm`, project context in `.scm/`, team standards via remote repositories.
:::

## What SCM Does

| Capability | Description |
|------------|-------------|
| **Context Assembly** | Combine fragments into profiles, inject into Claude/Gemini via MCP |
| **Slash Commands** | Prompts become `/commands` in Claude Code and Gemini automatically |
| **Session Memory** | Persist context across `/clear`, recover seamlessly |
| **Remote Sync** | Pull bundles from GitHub/GitLab, lockfile for reproducibility |
| **Token Optimization** | AST-aware distillation compresses code/prose 70-90% |

## Quick Example

```bash
# Initialize
scm init

# Run with context fragments
scm run -f go-development -f security "implement auth"

# Use a profile (pre-configured fragment set)
scm run -p backend-developer "review this PR"

# Pull from remote
scm fragment install scm-main/testing
```

## Slash Commands

Prompts in bundles become slash commands in Claude Code and Gemini CLI:

```yaml
# .scm/bundles/my-tools.yaml
prompts:
  code-review:
    description: "Review code for issues"
    content: |
      Review this code for:
      - Security vulnerabilities
      - Performance issues
      - Best practice violations
```

Then in your AI CLI:
```
/code-review src/auth.go
```

SCM includes built-in commands:
- `/recover` - Recover context from previous session after `/clear`
- `/loadctx` - Browse and load from any recent session

## Session Memory

Claude Code's built-in `/compact` is unreliable. SCM takes a different approach:

1. **Track** - SCM registers sessions by process ID on startup
2. **Clear** - Run `/clear` when you hit context limits
3. **Recover** - Ask to recover and SCM distills the previous session transcript
4. **Continue** - The distilled summary is injected, you continue working

```bash
# When you hit context limits
/clear

# Recover previous session automatically
"What were we working on?"
# SCM finds the transcript, distills it, returns the summary
```

SCM reads the raw JSONL transcript from disk and uses a separate LLM (default: Haiku) to distill it - not the degraded context window.

## How It Works

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Bundles   │────▶│  Profiles   │────▶│   Context   │
│ (fragments) │     │  (combos)   │     │  (assembled)│
└─────────────┘     └─────────────┘     └─────────────┘
       │                                       │
       ▼                                       ▼
┌─────────────┐                         ┌─────────────┐
│   Remotes   │                         │ Claude/Gemini│
│ (GitHub/GL) │                         │   (via MCP)  │
└─────────────┘                         └─────────────┘
```

1. **Bundles** contain fragments (context), prompts (skills), and MCP servers
2. **Profiles** combine bundles with inheritance and exclusions
3. **Context** is assembled and injected via MCP protocol
4. **Remotes** provide team sharing and community bundles

## Core Concepts

| Concept | What It Is |
|---------|------------|
| **Fragment** | A piece of context (guidelines, patterns, examples) |
| **Prompt** | A saved prompt template, exposed as a slash command |
| **Bundle** | A versioned YAML containing fragments + prompts + MCP servers |
| **Profile** | A named configuration referencing bundles/tags |
| **Remote** | A GitHub/GitLab repository containing bundles |

## Installation

```bash
# Go install
go install github.com/SophisticatedContextManager/scm@latest

# Or download from GitHub releases
```

## Running

```bash
# Standalone (wraps Claude/Gemini)
scm run -p developer "help with code"

# As MCP server (integrate with existing Claude Code)
scm mcp serve
```

## Configuration

```yaml
# .scm/config.yaml
defaults:
  llm_plugin: claude-code
  profiles:
    - scm-main/go-developer

memory:
  enabled: true
  mode: lazy
```

## Next Steps

- [Installation](/getting-started/installation) - Full installation guide
- [Quick Start](/getting-started/quickstart) - Create your first bundle
- [Bundles](/concepts/bundles) - Fragment and prompt structure
- [Profiles](/concepts/profiles) - Context assembly with inheritance
- [Session Memory](/guides/memory) - Preserve context across sessions
- [Prompts](/concepts/prompts) - Slash commands and skills

---

:::note Pre-release
Active development. Core features stable, API may evolve.
:::
