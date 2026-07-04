---
title: "Session Memory"
---

Session memory preserves context across conversations by compacting session history and storing it for future retrieval. This helps maintain continuity when you hit context limits or need to clear your session.

## Why Not Just Use `/compact`?

Claude Code's `/compact` has a fundamental design flaw: **it needs context space to run, but you only think to use it when context is almost full**.

The timing problem:
- `/compact` runs *inside* the current context window
- When your context is nearly exhausted, there's no room left to run the compaction
- You try `/compact`, it fails, and now you're stuck with a full window and no recovery

This isn't a bug - it's an inherent limitation of in-context compaction. You have to remember to run it *before* you need it, which defeats the purpose.

The alternative is worse: `/clear` then manually copy-paste chunks of your chat history back in, hoping you grabbed the right parts, fighting token limits, losing formatting. It works, but it's tedious and error-prone.

**ctxloom automates this properly:**

1. **Works after exhaustion** - `/clear` then `/recover` operates outside the full window
2. **External processing** - ctxloom reads the raw session transcript from disk (JSONL files)
3. **Separate LLM** - A dedicated model (configurable, default: Haiku) distills the content
4. **Controlled compression** - Extractive strategy preserves decisions, code, and next steps
5. **Persistent storage** - Distilled summaries are saved to `.ctxloom/sessions/`
6. **Reliable recovery** - Sessions are read from disk at request time, so the previous session is always findable

The workflow is simple: when you hit context limits, `/clear` and `/recover`. No timing anxiety.

## Overview

When working on long sessions, you'll eventually approach context window limits. Session memory lets you:

1. **Clear** the context window when you hit limits
2. **Recover** context from the previous session after `/clear`
3. **Browse** session history to find and load specific sessions

For durable, cross-session work items (distinct from the agent's ephemeral
to-dos), see [Sessions and Tasks](/concepts/sessions-and-tasks/).

## Usage

Session memory is always enabled - no configuration required.

### The `/recover` Command

When you hit context limits and need to clear:

```
/clear
/recover
```

The `/recover` skill:

1. Reads the previous session for this project from disk (read-time — no process tracking)
2. Distills the raw JSONL transcript using a separate LLM (default: Haiku)
3. Returns the essence so you can continue working

Behind the skill, two tools do the work: `recover_session` recovers the
most-recent session, and `get_previous_session` returns the previous session for
the current project.

### Alternative Recovery

You can also recover naturally:

```
What were we working on before the clear?
```

The AI will use `get_previous_session` to find and distill your previous session.

### Browsing Session History

To see recent sessions with short summaries, read the `ctxloom://sessions/recent`
resource — or just ask:

```
Show me recent sessions
```

Then load a specific one to continue it:

```
Load the distilled session from this morning
```

## How It Works

### Session Tracking

ctxloom records each session on disk under the project, in a harp-named session
directory. Recovery is **read-time**: when you ask to recover, ctxloom reads the
previous (or most-recent) session for the current project straight from disk. No
live process or PID tracking is involved, so recovery works even after the AI
process has fully restarted across `/clear` — and without manually specifying
session IDs.

### Compaction

Session compaction happens **outside your session** using a separate LLM call. This is critical - the compaction LLM has access to the full raw transcript, not a degraded context window.

The compaction process:

1. **Read transcript** - ctxloom reads the raw JSONL session log from disk
2. **Chunk** - Large sessions are split (default: 8000 tokens per chunk)
3. **Distill** - A fast model (default: Haiku) extracts key information:
   - Decisions made and why
   - Context established
   - Progress achieved
   - Next steps planned
4. **Store** - Result saved to `.ctxloom/sessions/` in the project

The distilled output is typically 10-20% of the original size while preserving actionable information.

### Storage

Distilled session summaries live in the project under `.ctxloom/sessions/`.
Sessions themselves are tracked in a harp-keyed index in your home directory:

```
~/.ctxloom/sessions/
├── index.yaml               # Session index (harp name → session metadata)
└── <harp-name>/
    └── essence.md           # Distilled essence for that session
```

### Cross-Agent Workflows

**Distilled memory** is portable across agents - it's stored as plain markdown. **Raw session history** is currently backend-specific (Claude and Antigravity use different transcript formats).

```bash
# Morning: Write code with Claude
ctxloom run --llm claude-code "implement the auth module"
# When done, compact the session (or let it auto-compact on context limit)

# Afternoon: Review with Antigravity
ctxloom run --llm antigravity
"Load the distilled session from this morning"
# Antigravity loads the markdown summary, continues the work
```

Use cases:
- **Development → Review** - Write with one model, review with another
- **Fast → Thorough** - Draft with Haiku, refine with Opus
- **Specialist models** - Use different models for different task types

The distilled markdown captures decisions, progress, and next steps - everything the next agent needs to continue the work.

:::note
Cross-backend session history (not just distilled summaries) is not yet implemented. If you need to browse Claude sessions from Antigravity or vice versa, [open an issue](https://github.com/ctxloom/ctxloom/issues).
:::

## MCP Tools

Session memory provides these MCP tools:

| Tool | Description |
|------|-------------|
| `compact_session` | Compact current or specified session |
| `load_session` | Distill and load a session by backend session ID or harp name (harp wins) |
| `recover_session` | Recover the most-recent session's context after `/clear` |
| `get_previous_session` | Get the previous session for this project (read-time) |

Browsing recent sessions is a **resource**, not a tool — read
`ctxloom://sessions/recent`.

### Example: Manual Compaction

```
Session's getting long, compact it
```

### Example: Load Specific Session

```
Load session swift-amber-falcon
```

`load_session` accepts either a backend session ID (UUID) or a harp name; harp
names are listed in the `ctxloom://sessions/recent` resource.

### Example: Recover Previous Session

```
Use get_previous_session to recover what we were working on
```

### Example: Browse History

Recent sessions are exposed as the `ctxloom://sessions/recent` resource:

```
Show me recent sessions
```

## Advanced Configuration

The compaction/distillation model is the `fast` role in `llm.defaults`; the
chunk size lives under `config`:

```yaml
llm:
  defaults:
    fast: claude-fast      # config label used for compaction/distillation
config:
  compaction_chunks: 8000  # target tokens per compaction chunk
```

## CLI Commands

### Manual Compaction

```bash
ctxloom memory compact
```

Compacts the current session directly from the command line.

### List Sessions

```bash
ctxloom memory list
```

Shows all sessions with their compaction status.

### Managing the Session Index

Sessions are harp-named (e.g. `swift-amber-falcon`) and recorded in the index
at `~/.ctxloom/sessions/index.yaml` automatically once launched with
`ctxloom run`. The `ctxloom session` family reads and manages that index:

```bash
ctxloom session list                    # Sessions for the current project
ctxloom session list --all              # Sessions for every project
ctxloom session show <harp>             # Print a session's distilled essence
ctxloom session rename <old> <new>      # Rename an index entry
ctxloom session forget <harp>           # Drop an entry (files stay on disk)
ctxloom session distill <harp>          # Force-distill a session
```

`session distill` is useful for sessions that ended before auto-compact ran —
distill first, then load with `load_session` or `ctxloom run --session <harp>`.

## Best Practices

1. **Just `/clear` when needed** - Don't overthink it; ctxloom tracks your session automatically
2. **Use `/recover` after clearing** - Distillation happens on-demand, no pre-saving required
3. **Browse older sessions** - Read `ctxloom://sessions/recent` (or ask "show me recent sessions") when you need context from days ago
4. **Review recovered content** - Check that important details were captured

## Troubleshooting

### Recovery Shows "No Previous Session"

If recovery can't find the previous session:
- Ensure you started the session with `ctxloom run` (not raw `claude`), so the session was recorded
- Browse `ctxloom://sessions/recent` (or ask "show me recent sessions") to find and load a specific one

### Compaction Fails

If compaction fails:
- Check that the LLM is configured correctly
- Ensure you have API access for the compaction model
- Try a smaller `config.compaction_chunks` if sessions are very large

### Distilled Session Missing

If a session you expect to load has no distilled content:
- Check `.ctxloom/sessions/` in the project for distilled summaries
- Run `ctxloom session list` to confirm the session is in the index
- Force-distill it with `ctxloom session distill <harp>` (for sessions that ended before auto-compact ran)
