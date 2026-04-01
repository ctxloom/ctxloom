---
title: "Quick Start"
---

Get up and running with ctxloom in minutes.

## What ctxloom Does

| Capability | Description |
|------------|-------------|
| **Context Assembly** | Combine fragments into profiles, inject into Claude/Gemini via MCP |
| **Slash Commands** | Prompts become `/commands` in Claude Code and Gemini automatically |
| **Session Memory** | Persist context across `/clear`, recover seamlessly |
| **Remote Sync** | Pull bundles from GitHub/GitLab, lockfile for reproducibility |
| **Token Optimization** | AST-aware distillation compresses code/prose 70-90% |

## Initialize Your Project

```bash
# Create .ctxloom directory in your project
ctxloom init

# Or create a global config at ~/.ctxloom
ctxloom init --home
```

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
BUNDLE          FRAGMENT            TAGS
go-development  error-handling      [golang, patterns]
go-development  testing             [golang, testing]
go-development  project-structure   [golang, organization]
security        owasp-top-10        [security, web]
```

### View Fragment Content

```bash
# Show a specific fragment
ctxloom fragment show go-development#fragments/testing

# Show the distilled (compressed) version
ctxloom fragment show go-development#fragments/testing --distilled
```

### List Prompts (Slash Commands)

```bash
# List all prompts
ctxloom prompt list

# Filter by bundle
ctxloom prompt list --bundle my-tools
```

Example output:
```
BUNDLE      PROMPT        DESCRIPTION
my-tools    code-review   Review code for issues
my-tools    refactor      Suggest refactoring improvements
core        commit        Generate commit message
```

### View Prompt Content

```bash
# Show a specific prompt
ctxloom prompt show my-tools#prompts/code-review
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

Prompts in bundles become slash commands in Claude Code and Gemini CLI:

```yaml
# .ctxloom/bundles/my-tools.yaml
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

# Or install locally
ctxloom fragment install community/go-testing
```

## Next Steps

- [Authoring Bundles](/getting-started/authoring) - Create your own bundles
- [Session Memory](/getting-started/memory) - Preserve context across sessions
- [Discovery](/guides/discovery) - Find community bundles
- [Profiles](/concepts/profiles) - Save common fragment combinations
