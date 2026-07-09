---
title: "Quick Start"
---

Get up and running with ctxloom in minutes.

## What ctxloom Does

| Capability | Description |
|------------|-------------|
| **Context Assembly** | Combine fragments into profiles, inject into Claude/Antigravity via MCP |
| **Slash Commands** | Skills become `/commands` in Claude Code and Antigravity automatically |
| **Session Memory** | Persist context across `/clear`, recover seamlessly |
| **Remote Pull** | Pull bundles from GitHub/GitLab, lockfile for reproducibility |
| **Token Optimization** | AST-aware distillation compresses code/prose 70-90% |

## Initialize Your Project

```bash
# Create .ctxloom directory in your project
ctxloom init

# Or create a global config at ~/.ctxloom
ctxloom init --home
```

`init` scaffolds a local default profile (`.ctxloom/profiles/default.yaml`,
inheriting the ctxloom-default baseline) and wires the trusted `ctxloom-default`
remote. Run interactively, it walks you through one merged interview: pick an AI
engine, optionally add a personal remote, then launch your AI for an
agent-assisted setup. Useful flags: `--engine` to pre-select the engine,
`--remote` to add a personal repo as a trusted remote (repeatable), `--forge` to
bind those remotes to a specific forge, `--non-interactive` to skip all prompts,
and `--skip-launch` to skip the auto-launch.

## Browse Available Content

After initialization, explore what's available:

### List Fragments

```bash
# List all fragments from installed bundles
ctxloom fragment list

# Filter by bundle
ctxloom fragment list --bundle go-development
```

Example output:
```
Fragments (4):

  go-development:
    - error-handling [golang, patterns]
    - testing [golang, testing]
    - project-structure [golang, organization]

  security:
    - owasp-top-10 [security, web]
```

### View Fragment Content

```bash
# Show a specific fragment
ctxloom fragment show go-development#fragments/testing

# Show the distilled (compressed) version
ctxloom fragment show go-development#fragments/testing --distilled
```

### List Skills (Slash Commands)

```bash
# List all skills
ctxloom skill list

# Filter by bundle
ctxloom skill list --bundle my-tools
```

Example output:
```
Skills (3):

  core:
    - commit [git]

  my-tools:
    - code-review [review]
    - refactor [refactoring]
```

### View Skill Content

```bash
# Show a specific skill
ctxloom skill show my-tools#skills/code-review
```

## Run with Context

```bash
# Include fragments when running AI
ctxloom run -f go-development "Help me with this code"

# Combine multiple fragments
ctxloom run -f go-development -f testing-patterns -f security \
  "implement user authentication with tests"

# Use a profile (pre-configured fragment set)
ctxloom run -p backend-developer "review this PR"

# Preview what context would be sent
ctxloom run -f go-development --dry-run --print
```

## Use Slash Commands

Skills in bundles become slash commands in Claude Code and Antigravity CLI:

```yaml
# .ctxloom/cache/bundles/my-tools.yaml
skills:
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

## Discover Community Bundles

```bash
# Find ctxloom repositories on GitHub/GitLab
ctxloom remote discover golang

# Add a remote
ctxloom remote add community alice/ctxloom-golang

# Browse remote content
ctxloom remote browse community

# Use remote content directly
ctxloom run -f community/go-testing "help with tests"

# Or author a local profile that references the remote bundle, then pull
ctxloom profile create go-testing -b community/go-testing
ctxloom remote pull
```

## Next Steps

- [Authoring Bundles](/getting-started/authoring) - Create your own bundles
- [Session Memory](/getting-started/memory) - Preserve context across sessions
- [Discovery](/guides/discovery) - Find community bundles
- [Profiles](/concepts/profiles) - Save common fragment combinations
