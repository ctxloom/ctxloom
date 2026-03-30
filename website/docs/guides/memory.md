---
sidebar_position: 8
---

# Session Memory

Session memory preserves context across conversations by compacting session history and storing it for future retrieval. This helps maintain continuity when you hit context limits or need to clear your session.

## Overview

When working on long sessions, you'll eventually approach context window limits. Session memory lets you:

1. **Compact** the current session into a distilled summary
2. **Clear** the context window for a fresh start
3. **Recover** context from the previous session after `/clear`
4. **Browse** session history to find and load specific sessions
5. **Search** across sessions using vector similarity (optional)

## Configuration

Enable memory in your `config.yaml`:

```yaml
memory:
  enabled: true
  mode: lazy          # or "eager"

  # Optional: vector search for retrieval
  vectors:
    enabled: true
```

### Memory Modes

| Mode | Behavior |
|------|----------|
| **lazy** (default) | Manual compaction via `/save`. Uses vector DB for retrieval. |
| **eager** | Same as lazy, but auto-loads distilled memory on session start. |

## Usage

### The `/save` Command

When you're approaching context limits or want to save your progress:

```
/save
```

This slash command instructs the AI to:

1. Compact the session using `compact_session` MCP tool
2. Index the distilled content to vector DB using `index_session` (if vectors enabled)
3. Inform you that you can run `/clear` for a fresh start

### Recovering After `/clear`

SCM tracks sessions across `/clear` using stable process IDs. After clearing, you can recover your previous context:

```
Recover context from the previous session
```

or

```
What were we working on before the clear?
```

The AI will use `get_previous_session` or `recover_session` to automatically find and load your previous session's distilled content.

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

### Semantic Search (Vector Mode)

With vectors enabled, you can search across all sessions:

```
Find sessions where we discussed authentication
```

The AI will use `query_memory` to find semantically similar content across your session history.

## How It Works

### Session Tracking

SCM uses a session registry to track conversations across `/clear`:

1. On session start, a hook registers the session transcript path with the SCM wrapper PID
2. The PID remains stable across `/clear` (even though the AI process restarts)
3. When you ask to recover, SCM looks up the previous session by PID

This allows seamless recovery without manually specifying session IDs.

### Compaction

Session compaction uses an LLM to distill your conversation into key information:

- **Decisions made** - What was decided and why
- **Context established** - Important background information
- **Progress achieved** - What was completed
- **Next steps** - What was planned but not done

The compaction process:

1. Reads the session transcript (JSONL from Claude/Gemini)
2. Chunks large sessions (default: 8000 tokens per chunk)
3. Distills each chunk using a fast model (default: Haiku)
4. Stores the result in `.scm/memory/`

### Vector Indexing

When using lazy mode with vectors enabled:

1. Distilled content is embedded using a local ONNX model
2. Embeddings are stored in a local vector database
3. Future queries use semantic similarity to find relevant sessions

### Storage

Memory is stored in:
```
.scm/memory/
├── distilled/           # Compacted session summaries
│   └── session-id.md
└── vectors/             # Vector database (if enabled)
    └── embeddings.db
```

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
| `index_session` | Index a session to vector DB |
| `query_memory` | Semantic search across session history |

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

### Vector Settings

```yaml
memory:
  vectors:
    enabled: true
    model_path: ~/.scm/models/all-MiniLM-L6-v2.onnx
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

1. **Use `/save` proactively** - Don't wait until you hit context limits
2. **Let SCM handle recovery** - After `/clear`, just ask to recover; SCM tracks the session automatically
3. **Browse before loading** - Use `browse_session_history` to see summaries before loading a session
4. **Be specific when querying** - "Find our database schema discussion" works better than "what did we do"
5. **Review compacted content** - Check that important details were preserved

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

### Vector Search Returns Nothing

If `query_memory` returns no results:
- Verify vectors are enabled: `memory.vectors.enabled: true`
- Check that sessions were indexed with `index_session`
- Try broader search terms

### Memory Not Loading on Start

In eager mode, if memory isn't loading:
- Check `memory.load_on_start` isn't set to `false`
- Verify a distilled session exists in `.scm/memory/distilled/`
- Check for errors in session start hooks
