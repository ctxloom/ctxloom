---
title: "Session Memory"
---

# Session Memory

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
5. **Persistent storage** - Distilled summaries are saved to `.ctxloom/memory/`
6. **Reliable recovery** - Process ID tracking ensures you can always find the previous session

The workflow is simple: when you hit context limits, `/clear` and `/recover`. No timing anxiety.

## Overview

When working on long sessions, you'll eventually approach context window limits. Session memory lets you:

1. **Clear** the context window when you hit limits
2. **Recover** context from the previous session after `/clear`
3. **Browse** session history to find and load specific sessions

## Usage

Session memory is always enabled - no configuration required.

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

ctxloom uses a session registry to track conversations across `/clear`:

1. On session start, a hook registers the session transcript path with the ctxloom wrapper PID
2. The PID remains stable across `/clear` (even though the AI process restarts)
3. When you ask to recover, ctxloom looks up the previous session by PID

This allows seamless recovery without manually specifying session IDs.

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
4. **Store** - Result saved to `.ctxloom/memory/distilled/`

The distilled output is typically 10-20% of the original size while preserving actionable information.

### Storage

Memory is stored in:
```
.ctxloom/memory/
└── distilled/           # Compacted session summaries
    └── session-id.md
```

### Cross-Agent Workflows

**Distilled memory** is portable across agents - it's stored as plain markdown. **Raw session history** is currently backend-specific (Claude and Gemini use different transcript formats).

```bash
# Morning: Write code with Claude
ctxloom run --plugin claude-code "implement the auth module"
# When done, compact the session (or let it auto-compact on context limit)

# Afternoon: Review with Gemini
ctxloom run --plugin gemini
"Load the distilled session from this morning"
# Gemini loads the markdown summary, continues the work
```

Use cases:
- **Development → Review** - Write with one model, review with another
- **Fast → Thorough** - Draft with Haiku, refine with Opus
- **Specialist models** - Use different models for different task types

The distilled markdown captures decisions, progress, and next steps - everything the next agent needs to continue the work.

:::note
Cross-backend session history (not just distilled summaries) is not yet implemented. If you need to browse Claude sessions from Gemini or vice versa, [open an issue](https://github.com/ctxloom/ctxloom/issues).
:::

## MCP Tools

Session memory provides these MCP tools:

| Tool | Description |
|------|-------------|
| `compact_session` | Compact current or specified session |
| `list_sessions` | List available sessions with compaction status |
| `load_session` | Distill and load a specific session by ID |
| `recover_session` | Recover context after `/clear` using process tracking |
| `get_previous_session` | Get the previous session's content by PID lookup |
| `browse_session_history` | Browse recent sessions with AI summaries |

### Example: Manual Compaction

```
Session's getting long, compact it
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

Compaction settings can be customized in `defaults:`:

```yaml
defaults:
  compaction_plugin: claude-code  # LLM plugin for distillation
  compaction_model: haiku         # Model to use (fast + cheap)
  compaction_chunks: 8000         # Tokens per chunk
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

## Best Practices

1. **Just `/clear` when needed** - Don't overthink it; ctxloom tracks your session automatically
2. **Use `/recover` after clearing** - Distillation happens on-demand, no pre-saving required
3. **Use `/loadctx` for older sessions** - Browse history when you need context from days ago
4. **Review recovered content** - Check that important details were captured

## Troubleshooting

### Recovery Shows "No Previous Session"

If recovery can't find the previous session:
- Ensure you started the session with `ctxloom run` (not raw `claude`)
- The session registry tracks by PID; if ctxloom wasn't the wrapper, it won't be tracked
- Try `browse_session_history` to manually find and load the session

### Compaction Fails

If compaction fails:
- Check that the LLM plugin is configured correctly
- Ensure you have API access for the compaction model
- Try with a smaller `chunk_size` if sessions are very large

### Memory Not Loading on Start

In eager mode, if memory isn't loading:
- Check `memory.load_on_start` isn't set to `false`
- Verify a distilled session exists in `.ctxloom/memory/distilled/`
- Check for errors in session start hooks
