---
title: "Environment Variables"
---

Environment variables that affect ctxloom behavior.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_VERBOSE` | Enable verbose logging (including delegated-child launch diagnostics: the child plugin's and ACP adapter's stderr) | `0` (disabled) |
| `CTXLOOM_ROOT` | Override project-root resolution (normally the git root or the directory containing `.ctxloom`) | unset |
| `CTXLOOM_DEBUG_HTTP` | Log HTTP requests made to remote forges | `0` (disabled) |

```bash
CTXLOOM_VERBOSE=1 ctxloom run -p developer "help"
```

## Remotes and Forges

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Token for the `github` forge (GitHub API reads, `remote discover`, PR publish) |
| `GH_TOKEN` | Fallback when `GITHUB_TOKEN` is not set |
| `PAGER` | Pager used to display the security-review output during `remote pull` |

A custom forge configured in `remotes.yaml` (e.g. a GitHub Enterprise instance) can name its own token variable via `token_env`; that variable takes precedence over `GITHUB_TOKEN` for remotes bound to it. The generic `git` forge uses ambient git auth (credential helper, ssh-agent, `~/.ssh/config`) and needs no token.

## Editor

| Variable | Description |
|----------|-------------|
| `VISUAL` | Preferred editor for editing content |
| `EDITOR` | Fallback editor if VISUAL is not set |

ctxloom checks `VISUAL` first, then `EDITOR` (falling back to `nano`). The `editor.command` config key takes precedence over both. Used by commands like:

```bash
ctxloom fragment edit my-bundle#fragments/coding-standards
ctxloom skill edit my-bundle#skills/review
```

## Containerized Agents

Agents with `runtime: container` pass authentication through to the engine inside the image:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Passed through for token-based Claude auth (subscription auth is the default) |
| `KIRO_API_KEY` | Passed through for the kiro backend |
| `CODEX_HOME` | Codex configuration directory, honored when materializing codex command files |

Status: the codex and kiro backends are implemented and hermetically tested; live operation is untested (no codex/kiro account on any dev host).

## Session Variables

`ctxloom run` exports these into the launched backend's environment; hooks, the MCP server, and taskloom read them. They are set for you — listed here for debugging:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_SESSION_HARP` | The session's harp name (e.g. `swift-amber-falcon`) |
| `CTXLOOM_PROJECT_ID` | Project identifier for session/task keying |
| `CTXLOOM_CONTEXT_FILE` | Path to the assembled-context file for this session |
| `CTXLOOM_RESUMED_FROM` | Harp name of the session this one resumed from, if any |

## Template Variables

Fragment templates have no built-in variables: the mustache data comes entirely from the resolved profile's `variables:` map, and undefined variables render empty with a warning. See [Templating](/guides/templating) for usage.
