---
title: "Hooks and Context Injection"
---

You never paste your standards into a new session again. Start Claude Code (or Antigravity) in a project with ctxloom configured, and your fragments and profile are already in the conversation before you type a word.

For Claude Code, this rides a **SessionStart hook**: ctxloom assembles your configured context, writes it to disk, and the hook injects it when the session starts. Antigravity has no SessionStart event, so it gets the same context a different way (below). This guide explains both flows and how to configure them.

## How Context Injection Works

### The Flow

1. You run `ctxloom run` or start Claude Code in a project with ctxloom configured
2. ctxloom assembles context from your default profile, bundles, and tags
3. Context is written to a content-addressed file in `.ctxloom/cache/context/`
4. The SessionStart hook injects this context into the AI session
5. The context file is left in place — it's a cache, reused across sessions with unchanged context and, when context is too large for one hook, across the multiple ordered chunk hooks that read it

## Automatic Hook Setup

When you run `ctxloom init` or `ctxloom mcp serve`, ctxloom automatically configures hooks in your AI tool's settings:

### Claude Code

ctxloom adds a hook to `.claude/settings.json`. Each event maps to an **array** of matcher entries, not a single object — Claude Code's settings schema rejects the object shape:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "'/home/user/.local/bin/ctxloom' hook inject-context --project '/home/user/project' <hash>",
            "timeout": 60
          }
        ]
      }
    ]
  }
}
```

### Antigravity

For the Antigravity CLI (`agy`), hooks live in the workspace `.agents/hooks.json`, using the same Claude-style nested shape (`PreToolUse`/`PostToolUse`).

Antigravity has **no SessionStart event**, so context injection works differently:

- Assembled context is delivered via a ctxloom-managed section in `.agents/AGENTS.md`, which agy reads at the start of each session.
- The session-bind hook fires on **PreToolUse** instead. Bundle hooks can declare `pre_tool_fallback: true` to run an idempotent `session_start` hook on PreToolUse for this backend.

## Manual Hook Management

### Apply Hooks

Hooks are applied automatically when you run `ctxloom init` or start `ctxloom mcp serve`.

To manually reapply hooks:

```bash
# Reapply hooks and regenerate context (all backends)
ctxloom manage hooks install

# Target one backend
ctxloom manage hooks install --backend claude-code

# Or run the full one-shot setup (hooks, MCP, statusline, gitignore)
ctxloom manage install
```

Applying hooks also writes the command files exported from commands, the MCP server config, and the HUD statusline (honoring `config.statusline`). There is no MCP tool for this — hook management is CLI-only.

## Context Assembly

### What Gets Included

Context is assembled from:

1. **Default Profile** - Your configured default profile
2. **Profile Parents** - Any parent profiles inherited
3. **Bundles** - All bundles referenced by the profile
4. **Tagged Fragments** - Fragments matching the profile's `select_tags` (its `tags:` field is descriptive-only and doesn't select content)

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
ctxloom: warning: large context may reduce LLM effectiveness; consider distillation or fewer fragments
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
'<path-to-ctxloom>' hook inject-context --project '<project-dir>' <hash>
```

- `<hash>` - Content hash identifying the context file
- `--project` - Absolute project directory, so the hook can find the file regardless of the shell's cwd
- Reads from `.ctxloom/cache/context/<hash>.md`
- Outputs context to stdout for the AI to consume
- Does not delete the context file — it's a cache, and oversized context is split into multiple ordered hooks (`--part k --of N`) that all read it

### Environment Variables

The SessionStart hook itself takes the hash and project directory as command-line arguments, not environment variables:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_VERBOSE` | Enable verbose output for debugging |
| `CTXLOOM_CONTEXT_FILE` | Path to the assembled context file, set on the launched process for backends with no hook mechanism (codex, antigravity, kiro) — not read by the SessionStart hook |

## Debugging Hooks

### Check Hook Configuration

```bash
# View Claude Code settings
cat .claude/settings.json | jq '.hooks'

# View current context file
ls -la .ctxloom/cache/context/
```

### Test Context Assembly

```bash
# Assemble context and show it, without launching the model
ctxloom run --dry-run
```

`--one-shot` is a separate flag that launches the model in non-interactive mode and prints its response — it doesn't preview context, and combining it with `--dry-run` has no additional effect since `--dry-run` never launches the model.

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
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "'/home/user/.local/bin/ctxloom' hook inject-context --project '/home/user/project' abc123"
          },
          {
            "type": "command",
            "command": "my-custom-hook.sh"
          }
        ]
      }
    ]
  }
}
```

**Note:** Claude Code's settings schema rejects unrecognized fields on hook entries, so ctxloom can't tag its own hooks with a marker field the way it does for MCP servers (`_ctxloom`). Instead it recognizes its own hook entries by the ctxloom executable in the command line, and only touches those, leaving your custom hooks intact.

## Troubleshooting

### Context Not Injected

1. Check hooks are applied: `cat .claude/settings.json`
2. Verify context file exists: `ls .ctxloom/cache/context/`
3. Run with verbose: `CTXLOOM_VERBOSE=1 ctxloom run`

### Stale Context

If context seems outdated, regenerate context and reapply hooks:

```bash
ctxloom manage hooks install
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
select_tags:
  - best-practices
```

When this is your default profile, every session automatically gets these bundles and tagged fragments injected.

## Best Practices

1. **Keep context focused** - Include only what's relevant to your current work
2. **Use profiles** - Create different profiles for different tasks
3. **Monitor size** - Watch for size warnings and optimize as needed
4. **Test changes** - Use `--dry-run` to preview context changes
5. **Version control** - Commit your `.ctxloom/` configuration
