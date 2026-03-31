---
title: "Environment Variables"
---

# Environment Variables

Environment variables that affect SCM behavior.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `SCM_VERBOSE` | Enable verbose logging | `0` (disabled) |

```bash
SCM_VERBOSE=1 ctxloom run -p developer "help"
```

## Editor

| Variable | Description |
|----------|-------------|
| `VISUAL` | Preferred editor for editing content |
| `EDITOR` | Fallback editor if VISUAL is not set |

SCM checks `VISUAL` first, then `EDITOR`. Used by commands like:

```bash
ctxloom bundle fragment edit my-bundle coding-standards
ctxloom bundle prompt edit my-bundle review
```

## Template Variables

These are available within fragment templates (not shell environment):

| Variable | Description |
|----------|-------------|
| `SCM_ROOT` | Project root directory (parent of .scm) |
| `SCM_DIR` | Full path to .ctxloom directory |

See [Templating](/guides/templating) for usage.
