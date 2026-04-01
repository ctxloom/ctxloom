---
title: "Hooks and Context Injection"
---

ctxloom uses hooks to automatically inject context into your AI coding sessions. This guide explains how the hook system works and how to configure it.

## How Context Injection Works

When you start a Claude Code session, ctxloom automatically injects your configured context (fragments, profiles) into the conversation. This happens through a **SessionStart hook** that runs before each session.

### The Flow

1. You run `ctxloom run` or start Claude Code in a project with ctxloom configured
2. ctxloom assembles context from your default profile, bundles, and tags
3. Context is written to a temporary file in `.ctxloom/context/`
4. The SessionStart hook injects this context into the AI session
5. The context file is deleted after injection (one-time use)

## Automatic Hook Setup

When you run `ctxloom init` or `ctxloom mcp serve`, ctxloom automatically configures hooks in your AI tool's settings:

### Claude Code

ctxloom adds a hook to `.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": {
      "hooks": [
        {
          "type": "command",
          "command": "ctxloom hook inject-context <hash>",
          "timeout": 60
        }
      ]
    }
  }
}
```

### Gemini

Similar configuration in `.gemini/settings.json`.

## Manual Hook Management

### Apply Hooks

Hooks are applied automatically when you run `ctxloom init` or start `ctxloom mcp serve`.

To manually reapply hooks, you can:

1. **Re-run init** (simplest approach):
```bash
ctxloom init
```

2. **Use the MCP tool** (if ctxloom is running as MCP server):

### Via MCP

```json
{
  "tool": "apply_hooks",
  "arguments": {
    "backend": "claude-code",
    "regenerate_context": true
  }
}
```

## Context Assembly

### What Gets Included

Context is assembled from:

1. **Default Profile** - Your configured default profile
2. **Profile Parents** - Any parent profiles inherited
3. **Bundles** - All bundles referenced by the profile
4. **Tagged Fragments** - Fragments matching profile tags

### Assembly Order

Fragments are ordered using a "bookend" strategy to address the [Lost in the Middle](https://arxiv.org/abs/2307.03172) problem where LLMs attend poorly to middle content:

| Position | Content | Why |
|----------|---------|-----|
| **Start** | Highest priority | Primacy effect - best attention |
| **End** | Second highest priority | Recency effect - good attention |
| **Middle** | Remaining (descending) | Weaker attention area |

Fragments without explicit priority default to 0. See [Fragment Priority](/concepts/profiles#fragment-priority) for setting priorities.

### Deduplication

ctxloom automatically deduplicates content:
- Same fragment from multiple sources appears once
- Content-hash based deduplication catches identical content even from different bundles

## Context Size Management

### Size Warning

ctxloom warns when assembled context exceeds 16KB:

```
ctxloom: warning: assembled context is 24KB (recommended max: 16KB)
ctxloom: warning: large context may reduce LLM effectiveness; consider using fewer/smaller fragments
```

[Research shows](https://arxiv.org/abs/2307.03172) that LLM performance degrades with larger context, particularly for middle-positioned content. See the [Distillation Guide](/guides/distillation#context-size-research) for details.

### Reducing Context Size

If you see size warnings:

1. **Use distillation** - Distill verbose fragments to compressed versions
2. **Be selective** - Only include fragments relevant to current work
3. **Split profiles** - Create task-specific profiles instead of one large profile
4. **Review bundles** - Remove unused bundles from profiles

## Hook Commands

### inject-context

The primary hook command that injects context:

```bash
ctxloom hook inject-context <hash>
```

- `<hash>` - Content hash identifying the context file
- Reads from `.ctxloom/context/<hash>.md`
- Outputs context to stdout for the AI to consume
- Deletes the context file after reading

### Environment Variables

The hook system uses:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_CONTEXT_FILE` | Path to the context file to inject |
| `CTXLOOM_VERBOSE` | Enable verbose output for debugging |

## Debugging Hooks

### Check Hook Configuration

```bash
# View Claude Code settings
cat .claude/settings.json | jq '.hooks'

# View current context file
ls -la .ctxloom/context/
```

### Test Context Assembly

```bash
# Preview what would be injected
ctxloom run --dry-run --print

# Assemble and show context
ctxloom run --print
```

### Verbose Mode

Enable verbose output to see hook execution:

```bash
CTXLOOM_VERBOSE=1 ctxloom run
```

## Custom Hooks

While ctxloom manages its own hooks, you can add custom hooks alongside ctxloom's:

```json
{
  "hooks": {
    "SessionStart": {
      "hooks": [
        {
          "type": "command",
          "command": "ctxloom hook inject-context abc123"
        },
        {
          "type": "command",
          "command": "my-custom-hook.sh"
        }
      ]
    }
  }
}
```

**Note:** ctxloom identifies its hooks by an internal marker (`_ctxloom` field) and only updates its own hooks, leaving your custom hooks intact.

## Troubleshooting

### Context Not Injected

1. Check hooks are applied: `cat .claude/settings.json`
2. Verify context file exists: `ls .ctxloom/context/`
3. Run with verbose: `CTXLOOM_VERBOSE=1 ctxloom run`

### Stale Context

If context seems outdated, re-run init to regenerate context and reapply hooks:

```bash
ctxloom init
```

### Hook Timeout

If hooks timeout, increase the timeout in settings or optimize your context assembly (reduce fragments, use distillation).

## Integration with Profiles

Hooks work seamlessly with profiles:

```yaml
# .ctxloom/profiles/default.yaml
description: My default development context
bundles:
  - go-development
  - testing-patterns
tags:
  - best-practices
```

When this is your default profile, every session automatically gets these bundles and tagged fragments injected.

## Best Practices

1. **Keep context focused** - Include only what's relevant to your current work
2. **Use profiles** - Create different profiles for different tasks
3. **Monitor size** - Watch for size warnings and optimize as needed
4. **Test changes** - Use `--dry-run` to preview context changes
5. **Version control** - Commit your `.ctxloom/` configuration
