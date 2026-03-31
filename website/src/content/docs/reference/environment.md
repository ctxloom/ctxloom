---
title: "Environment Variables"
---

# Environment Variables

Environment variables that affect ctxloom behavior.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_VERBOSE` | Enable verbose logging | `0` (disabled) |

```bash
CTXLOOM_VERBOSE=1 ctxloom run -p developer "help"
```

## Editor

| Variable | Description |
|----------|-------------|
| `VISUAL` | Preferred editor for editing content |
| `EDITOR` | Fallback editor if VISUAL is not set |

ctxloom checks `VISUAL` first, then `EDITOR`. Used by commands like:

```bash
ctxloom bundle fragment edit my-bundle coding-standards
ctxloom bundle prompt edit my-bundle review
```

## Template Variables

These are available within fragment templates (not shell environment):

| Variable | Description |
|----------|-------------|
| `CTXLOOM_ROOT` | Project root directory (parent of .ctxloom) |
| `CTXLOOM_DIR` | Full path to .ctxloom directory |

See [Templating](/guides/templating) for usage.
