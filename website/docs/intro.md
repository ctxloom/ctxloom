---
slug: /
sidebar_position: 1
---

# SCM - Sophisticated Context Manager

## Why Use SCM?

**Stop repeating yourself to AI.** Every session you re-explain your coding standards, patterns, and preferences. SCM solves this:

- **Save hours per week** - Write context once, reuse across all sessions
- **Consistent AI behavior** - Same standards enforced every time
- **Share across team** - Pull bundles from GitHub, everyone gets the same context
- **Never lose progress** - Session memory survives `/clear`

## What SCM Does

| Capability | Description |
|------------|-------------|
| **Context Assembly** | Combine fragments into profiles, inject into Claude/Gemini via MCP |
| **Slash Commands** | Prompts become `/skills` in Claude Code automatically |
| **Session Memory** | `/save` to persist context, recover after `/clear` |
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

Prompts in bundles become Claude Code skills:

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

Then in Claude Code:
```
/code-review src/auth.go
```

SCM includes built-in commands:
- `/save` - Compact session to memory, prepare for `/clear`

## Session Memory

Preserve context across `/clear`:

```bash
# Before hitting context limits
/save

# Clear context window
/clear

# Recover previous session automatically
"What were we working on?"
```

SCM tracks sessions by process ID and recovers seamlessly.

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
# Homebrew
brew install benjaminabbitt/tap/scm

# Go
go install github.com/benjaminabbitt/scm@latest

# Or download from releases
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
  vectors:
    enabled: true
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
