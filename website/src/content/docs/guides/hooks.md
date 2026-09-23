---
title: "Hooks and Context Injection"
---

You never paste your standards into a new session again. Start a session with `ctxloom run`, and your fragments and profile are already in the conversation before you type a word.

There are two delivery paths for Claude Code. A `ctxloom run` session passes the assembled context to Claude Code as an appended system prompt file in the session's own home. A Claude Code you start directly gets context only if you ran `ctxloom manage hooks install`, which writes a **SessionStart hook** into the project that injects it when the session starts. This guide explains both and how to configure them.

## How Context Injection Works

### The Flow

1. ctxloom assembles context from the profiles your agent composes (the default agent for a bare `ctxloom run`), their bundles, and tags
2. Context is written to a content-addressed file in `.ctxloom/cache/context/`
3. For a `ctxloom run` session, Claude Code reads that context as an appended system prompt; nothing is written to your project's `CLAUDE.md` or `.claude/settings.json`
4. For an engine you launch directly after `ctxloom manage hooks install`, the SessionStart hook injects the context file when the session starts
5. The context file is left in place. It is a cache, reused across sessions with unchanged context and, when context is too large for one hook, across the multiple ordered chunk hooks that read it

`ctxloom run --dry-run` prints the route each surface takes (context, MCP, settings, hooks, commands) and where it lands.

## Automatic Hook Setup

A `ctxloom run` session delivers its hooks into the session's own home, so nothing in your project tree is needed for them. `ctxloom init` and `ctxloom manage install` write no engine file; an engine you launch directly in the project gets no ctxloom hooks unless you wired them in explicitly with `ctxloom manage hooks install`. Where they are written, they take the engine's own shape:

### Claude Code

`ctxloom manage hooks install` adds a hook to `.claude/settings.json`. Each event maps to an **array** of matcher entries, not a single object, because Claude Code's settings schema rejects the object shape:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "'ctxloom' hook inject-context <hash>",
            "timeout": 60
          }
        ]
      }
    ]
  }
}
```

The command names the bare `ctxloom` executable and carries no project path. The settings file is usually committed, and an absolute path would be one developer's path that no other clone can satisfy. The hook resolves the project when it fires: `CTXLOOM_ROOT` if set, otherwise the git root containing the working directory.

## Manual Hook Management

### Apply Hooks

A `ctxloom run` session carries its own hooks; you never apply them for it. `ctxloom init` and `ctxloom manage install` scaffold ctxloom's configuration and write nothing into your engine's files.

To write ctxloom's hooks into the project tree explicitly — for an engine you launch directly rather than through `ctxloom run` — apply them yourself:

```bash
# Write hooks and regenerate context (the engines this project configures)
ctxloom manage hooks install

# Target one backend
ctxloom manage hooks install --backend claude-code

# Take them back out
ctxloom manage uninstall
```

Applying hooks also writes the command files exported from commands, the MCP servers your bundles declare, and the HUD statusline (honoring `config.statusline`). ctxloom's own MCP server is not among them: it is served by a running `ctxloom run` session and registered only for that session, so nothing ctxloom-owned is left executable at rest. There is no MCP tool for this; hook management is CLI-only.

`ctxloom manage hooks list` shows every hook that will fire, per event, in order, with the profile, bundle or companion each came from.

## Context Assembly

### What Gets Included

Context is assembled from:

1. **Agent Profiles** - The profiles your agent composes (`default_agent` for a bare `ctxloom run`)
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
'ctxloom' hook inject-context <hash>
```

- `<hash>` - Content hash identifying the context file
- `--project` - Optional project directory for invoking the hook by hand; generated hooks omit it and resolve the project at fire time
- Reads from `.ctxloom/cache/context/<hash>.md`
- Outputs context to stdout for the AI to consume
- Does not delete the context file — it's a cache, and oversized context is split into multiple ordered hooks (`--part k --of N`) that all read it

### Environment Variables

The SessionStart hook itself takes the hash and project directory as command-line arguments, not environment variables:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_VERBOSE` | Enable verbose output for debugging |
| `CTXLOOM_CONTEXT_FILE` | Path to the assembled context file, set on the launched engine's environment whenever context was assembled — not read by the SessionStart hook |

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
            "command": "'ctxloom' hook inject-context abc123"
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

**Note:** Claude Code's settings schema rejects unrecognized fields on hook entries, so ctxloom can't tag its own hooks with a marker field. Instead it keeps an ownership record of the hook commands it wrote and removes only those, plus its own callback verbs (`hook inject-context` and the other `ctxloom hook` subcommands). A custom hook of yours stays intact even when it invokes `ctxloom`.

Hooks you author yourself belong in a profile or bundle under `hooks:` (see [Configuration → Hooks](/guides/configuration/#hooks)). ctxloom then delivers them with its own, into the session home for `ctxloom run` or into `.claude/settings.json` for `manage hooks install`.

## Troubleshooting

### Context Not Injected

1. For a `ctxloom run` session: `ctxloom run --dry-run` shows the fragments loaded, the assembled context and its delivery route
2. For an engine you start directly: check hooks are applied with `cat .claude/settings.json` or `ctxloom manage hooks check`
3. Verify context file exists: `ls .ctxloom/cache/context/`
4. Run with verbose: `CTXLOOM_VERBOSE=1 ctxloom run`

### Stale Context

A `ctxloom run` session assembles fresh context at every launch. If context from an installed hook seems outdated, regenerate context and reapply hooks:

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

When your default agent composes this profile (`agents.<name>.profiles`), every session automatically gets these bundles and tagged fragments injected.

## Best Practices

1. **Keep context focused** - Include only what's relevant to your current work
2. **Use profiles** - Create different profiles for different tasks
3. **Monitor size** - Watch for size warnings and optimize as needed
4. **Test changes** - Use `--dry-run` to preview context changes
5. **Version control** - Commit your `.ctxloom/` configuration
