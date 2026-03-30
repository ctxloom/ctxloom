---
sidebar_position: 8
---

# Session Memory

Session memory preserves context across conversations by compacting session history and storing it for future retrieval. This helps maintain continuity when you hit context limits or need to clear your session.

## Why Not Just Use `/compact`?

Claude Code has a built-in `/compact` command, but it's unreliable:
- It runs inside the same context window it's trying to compress
- The LLM may lose important details or hallucinate
- You can't control the compression strategy
- No persistence - if something goes wrong, context is lost

**SCM's approach is different:**

1. **External processing** - SCM reads the raw session transcript from disk (JSONL files)
2. **Separate LLM** - A dedicated model (configurable, default: Haiku) distills the content
3. **Controlled compression** - Extractive strategy preserves decisions, code, and next steps
4. **Persistent storage** - Distilled summaries are saved to `.scm/memory/`
5. **Reliable recovery** - Process ID tracking ensures you can always find the previous session

## Overview

When working on long sessions, you'll eventually approach context window limits. Session memory lets you:

1. **Clear** the context window when you hit limits
2. **Recover** context from the previous session after `/clear`
3. **Browse** session history to find and load specific sessions

## Configuration

Enable memory in your `config.yaml`:

```yaml
memory:
  enabled: true
  mode: lazy          # or "eager"
```

### Memory Modes

| Mode | Behavior |
|------|----------|
| **lazy** (default) | Distillation on demand when recovering. |
| **eager** | Same as lazy, but auto-loads distilled memory on session start. |

## Usage

### The `/recover` Command

When you hit context limits and need to clear:

```
/clear
/recover
```

The `/recover` command:

1. Finds the previous session using process ID tracking
2. Reads the raw JSONL transcript from disk
3. Distills it using a separate LLM (default: Haiku)
4. Returns the essence to continue working

### Alternative Recovery

You can also recover naturally:

```
What were we working on before the clear?
```

The AI will use `get_previous_session` to find and distill your previous session.

### Browsing Session History

Use `/loadctx` to browse and load from any recent session:

```
/loadctx
```

This shows sessions from the last 3 days with AI-generated summaries.

### Browsing Session History

To see recent sessions with AI-generated summaries:

```
Show me recent sessions
```

or

```
Browse my session history
```

The AI will use `browse_session_history` to show sessions from the last 3 days with a brief summary of each, then you can load a specific one:

```
Load session abc123def
```

## How It Works

### Session Tracking

SCM uses a session registry to track conversations across `/clear`:

1. On session start, a hook registers the session transcript path with the SCM wrapper PID
2. The PID remains stable across `/clear` (even though the AI process restarts)
3. When you ask to recover, SCM looks up the previous session by PID

This allows seamless recovery without manually specifying session IDs.

### Compaction

Session compaction happens **outside your session** using a separate LLM call. This is critical - the compaction LLM has access to the full raw transcript, not a degraded context window.

The compaction process:

1. **Read transcript** - SCM reads the raw JSONL session log from disk
2. **Chunk** - Large sessions are split (default: 8000 tokens per chunk)
3. **Distill** - A fast model (default: Haiku) extracts key information:
   - Decisions made and why
   - Context established
   - Progress achieved
   - Next steps planned
4. **Store** - Result saved to `.scm/memory/distilled/`

The distilled output is typically 10-20% of the original size while preserving actionable information.

### Storage

Memory is stored in:
```
.scm/memory/
└── distilled/           # Compacted session summaries
    └── session-id.md
```

### Cross-Agent Workflows

**Distilled memory** is portable across agents - it's stored as plain markdown. **Raw session history** is currently backend-specific (Claude and Gemini use different transcript formats).

```bash
# Morning: Write code with Claude
scm run --plugin claude-code "implement the auth module"
/save  # Distills to .scm/memory/distilled/

# Afternoon: Review with Gemini
scm run --plugin gemini
"Load the distilled session from this morning"
# Gemini loads the markdown summary, continues the work
```

Use cases:
- **Development → Review** - Write with one model, review with another
- **Fast → Thorough** - Draft with Haiku, refine with Opus
- **Specialist models** - Use different models for different task types

The distilled markdown captures decisions, progress, and next steps - everything the next agent needs to continue the work.

:::note
Cross-backend session history (not just distilled summaries) is not yet implemented. If you need to browse Claude sessions from Gemini or vice versa, [open an issue](https://github.com/SophisticatedContextManager/scm/issues).
:::

## MCP Tools

Session memory provides these MCP tools:

| Tool | Description |
|------|-------------|
| `compact_session` | Compact current or specified session |
| `get_session_memory` | Get distilled content loaded at startup |
| `list_sessions` | List available sessions with compaction status |
| `load_session` | Distill and load a specific session by ID |
| `recover_session` | Recover context after `/clear` using process tracking |
| `get_previous_session` | Get the previous session's content by PID lookup |
| `browse_session_history` | Browse recent sessions with AI summaries |

### Example: Manual Compaction

```
Use compact_session to save our current progress
```

### Example: Load Specific Session

```
Use load_session to load session abc123def456
```

### Example: Recover Previous Session

```
Use get_previous_session to recover what we were working on
```

### Example: Browse History

```
Use browse_session_history to show me recent sessions
```

## Advanced Configuration

### Compaction Settings

```yaml
memory:
  enabled: true
  mode: lazy

  compaction:
    plugin: claude-code     # LLM plugin for distillation
    model: haiku            # Model to use (fast + cheap)
    chunk_size: 8000        # Tokens per chunk

  # Control session start behavior
  load_on_start: false      # Don't auto-load (lazy mode default)
```

## CLI Commands

### Check Session Size

```bash
scm memory check
```

Shows current session size and whether it's approaching the threshold.

### Manual Compaction

```bash
scm memory compact
```

Compacts the current session directly from the command line.

### List Sessions

```bash
scm memory list
```

Shows all sessions with their compaction status.

## Best Practices

1. **Just `/clear` when needed** - Don't overthink it; SCM tracks your session automatically
2. **Use `/recover` after clearing** - Distillation happens on-demand, no pre-saving required
3. **Use `/loadctx` for older sessions** - Browse history when you need context from days ago
4. **Review recovered content** - Check that important details were captured

## Troubleshooting

### Recovery Shows "No Previous Session"

If recovery can't find the previous session:
- Ensure you started the session with `scm run` (not raw `claude`)
- The session registry tracks by PID; if SCM wasn't the wrapper, it won't be tracked
- Try `browse_session_history` to manually find and load the session

### Compaction Fails

If compaction fails:
- Check that the LLM plugin is configured correctly
- Ensure you have API access for the compaction model
- Try with a smaller `chunk_size` if sessions are very large

### Memory Not Loading on Start

In eager mode, if memory isn't loading:
- Check `memory.load_on_start` isn't set to `false`
- Verify a distilled session exists in `.scm/memory/distilled/`
- Check for errors in session start hooks
