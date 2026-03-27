---
sidebar_position: 8
---

# Session Memory

Session memory preserves context across conversations by compacting session history and storing it for future retrieval. This helps maintain continuity when you hit context limits or need to clear your session.

:::note Build Requirement
Session memory requires building SCM with the `memory` tag. Vector search additionally requires the `vectors` tag:
```bash
go build -tags memory ./...           # Basic memory
go build -tags "memory,vectors" ./... # Memory + vector search
```
:::

## Overview

When working on long sessions, you'll eventually approach context window limits. Session memory lets you:

1. **Compact** the current session into a distilled summary
2. **Clear** the context window for a fresh start
3. **Retrieve** relevant context when you need it later

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

**In Lazy Mode:**
1. Compact the session using `compact_session` MCP tool
2. Index the distilled content to vector DB using `index_session`
3. Inform you that you can run `/clear` if you want a fresh start

**In Eager Mode:**
1. Compact the session using `compact_session` MCP tool
2. Inform you that distilled memory will auto-load on next session start
3. You can run `/clear` manually if you want a fresh start now

### Retrieving Context (Lazy Mode)

After clearing, if you want to continue previous work:

```
What were we working on before?
```

or

```
Continue from where we left off
```

The AI will use the `query_memory` MCP tool to search the vector database for relevant context from your previous sessions.

## How It Works

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
4. Stores the result in `~/.scm/memory/`

### Vector Indexing

When using lazy mode with vectors enabled:

1. Distilled content is embedded using a local ONNX model
2. Embeddings are stored in a local ChromaDB instance
3. Future queries use semantic similarity to find relevant sessions

### Storage

Memory is stored in:
```
~/.scm/memory/
├── distilled/           # Compacted session summaries
│   └── session-id.md
└── vectors/             # Vector database (if enabled)
    └── chroma.db
```

## MCP Tools

Session memory adds these MCP tools:

| Tool | Description |
|------|-------------|
| `compact_session` | Compact current or specified session |
| `get_session_memory` | Get distilled content from a session |
| `index_session` | Index a session to vector DB |
| `query_memory` | Semantic search across session history |
| `list_sessions` | List available sessions |

### Example: Manual Compaction

```
Use the compact_session tool to save our current progress
```

### Example: Querying Memory

```
Use query_memory to find what we discussed about authentication
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
2. **Be specific when querying** - "Find our database schema discussion" works better than "what did we do"
3. **Review compacted content** - Check that important details were preserved
4. **Clean up old sessions** - Periodically remove irrelevant session history

## Troubleshooting

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
- Verify a distilled session exists in `~/.scm/memory/distilled/`
- Check for errors in session start hooks
