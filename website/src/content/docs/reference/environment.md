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
| `CTXLOOM_DEGRADED` | Set to `1` for the environment-variable form of `--degraded`: relaxed strictness (warn-and-continue instead of a hard fail on findings that would otherwise abort). Read before cobra dispatch, so it also covers the pre-command window (config discovery, project-root resolution). There is deliberately no config-file equivalent — a broken config can't excuse itself. As config decoding becomes stricter, this is the escape hatch that unblocks a session a strict decode would otherwise refuse to start | unset |
| `CTXLOOM_NO_COMPANIONS` | Set to `1` to skip companion discovery (the pre-dispatch probe that executes whatever companion binaries, like `ltk` or `taskloom`, are on `PATH`). Same purpose as `--no-companions`: a subprocess or CI run that must not depend on what the host happens to have installed | unset |

```bash
CTXLOOM_VERBOSE=1 ctxloom run -p developer "help"
CTXLOOM_DEGRADED=1 ctxloom run -p developer "help"
```

## Remotes and Forges

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Token for the `github` forge (GitHub API reads, `remote discover`, PR publish) |
| `GH_TOKEN` | Read after `GITHUB_TOKEN` and, when set, overwrites it — **`GH_TOKEN` takes precedence over `GITHUB_TOKEN`**, not the other way around. If both are set and you're getting an unexpected 403, check `GH_TOKEN` first |

A custom forge configured in `remotes.yaml` (e.g. a GitHub Enterprise instance) can name its own token variable via `token_env`; that variable takes precedence over `GITHUB_TOKEN` for remotes bound to it. The generic `git` forge uses ambient git auth (credential helper, ssh-agent, `~/.ssh/config`) and needs no token.

## Editor

| Variable | Description |
|----------|-------------|
| `VISUAL` | Preferred editor for editing content |
| `EDITOR` | Fallback editor if VISUAL is not set |

ctxloom checks `VISUAL` first, then `EDITOR` (falling back to `nano`). The `editor.command` config key takes precedence over both. Used by commands like:

```bash
ctxloom fragment edit my-bundle#fragments/coding-standards
ctxloom command edit my-bundle#commands/review
```

## Containerized Agents

Agents with `runtime: container-rootless` or `runtime: container-rootful` pass a scoped set of host variables through to the engine inside the image:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Passed through for token-based Claude auth (subscription auth, via mounted OAuth credentials, is the default when this is unset) |
| `ANTHROPIC_AUTH_TOKEN` | Forwarded alongside `ANTHROPIC_API_KEY` when present |
| `ANTHROPIC_BASE_URL` | Forwarded alongside `ANTHROPIC_API_KEY` when present |
| `ANTHROPIC_MODEL` | Forwarded alongside `ANTHROPIC_API_KEY` when present. This is not a convenience — `claude-code-acp` 0.16.2 silently ignores the driver's `--model` argument, so this env var is the *only* way to select a model for a containerized (or generic ACP-driven) claude run |
| `ANTHROPIC_SMALL_FAST_MODEL` | Forwarded alongside `ANTHROPIC_API_KEY` when present |
| `KIRO_API_KEY` | Passed through for the kiro backend (triggers headless auth, skipping the browser login) |
| `TERM`, `COLORTERM` | Forwarded so the engine renders with the host terminal's actual capabilities instead of the image default (or `dumb`, which drops color and cursor control) |
| `PUID`, `PGID` | *Not* read from your environment — set by the isolation runtime from `os.Getuid()`/`os.Getgid()` and passed into the container. Under a rootful daemon (rootful Docker, Podman) the entrypoint uses them to remap the image's baked-in `ctxloom` user to your uid/gid and drop privileges to it before the engine starts, so files the engine writes into the bind-mounted project are owned by you, not by the container's generic user or by root. If the remap can't be performed (no usable `gosu`/`setpriv` in the image) the entrypoint refuses to run the engine as root and fails the launch loudly, unless `--degraded` (or `CTXLOOM_DEGRADED=1`) is in effect, which downgrades the refusal to a warning and lets the engine run as root. Rootless Docker never sets these — container-root there already is the launching user |
| `CLAUDECODE` | Claude's own nested-session guard. When driving claude, ctxloom strips this from the spawned child's environment unconditionally, because claude 2.x refuses to start with it set — it would otherwise leak in as pure process-tree lineage under delegation |

Status: the `kiro`, `codex`, and `antigravity` backends are **experimental** — implemented and hermetically tested, but live operation is not fully verified. `kiro` and `antigravity` additionally cannot authenticate under container isolation with a subscription login (kiro's credential is a sqlite store, antigravity's is the OS keyring — neither mounts into a container), so a containerized run of either requires an API key / token (`KIRO_API_KEY` for kiro). `claude-code` is the exercised default.

## Host and Engine Integration

These are read on the host (or inside the launched engine process) rather than crossing into a container — they don't appear in either scoped passthrough list above.

| Variable | Description |
|----------|-------------|
| `CODEX_HOME` | Codex's home directory. It **is** the `.codex` directory itself, not its parent (default `~/.codex`). This is a host-side lookup only — `CODEX_HOME` is honored when ctxloom resolves Codex's prompts directory, MCP registrar config path, and sessions directory, all of which key off it as the single source of truth for Codex-home precedence, but it never crosses into a container (it's in neither the claude nor the kiro auth passthrough list). ctxloom itself sets it (alongside `CLAUDE_CONFIG_DIR` and `KIRO_HOME`) for a worktree-isolated agent run, to give each concurrent agent its own global config layer rather than sharing yours |
| `KIRO_HOME` | Kiro's home directory, used to locate kiro's session-history store (default `~/.kiro`). Like `CODEX_HOME`, ctxloom itself sets this for a worktree-isolated agent run |
| `SSH_AUTH_SOCK` | ssh-agent socket used when signing a bundle with an ssh-agent-held key |
| `XDG_RUNTIME_DIR` | Preferred base directory for the MCP runner's local unix-socket dir (`$XDG_RUNTIME_DIR/ctxloom`), tried after `/run/ctxloom/local` and before a `MkdirTemp` fallback |

## Delegated Agents

A child session spawned under agent delegation (`agent_run` / agentcoord) receives a reach-back trio that lets it talk back to its coordinator. These are set for you by the coordinator, not something you export by hand — listed here for debugging a delegated child:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_COORD_URL` | The coordinator's MCP endpoint URL (`http://host:port/mcp`) |
| `CTXLOOM_COORD_CRED` | The child's bearer credential for authenticating back to the coordinator |
| `CTXLOOM_RUN_ID` | The coordinator-minted run id correlating this child to the run it was spawned for |
| `CTXLOOM_MCP_SOCKET` | The runner's local MCP-endpoint socket path; a `ctxloom mcp` shim finding this forwards the whole tool surface there over HTTP-over-unix |

## Delegated Launch Retry

Tunables for the bounded launch-retry budget that gates a delegated child's (`agent_run`) launch attempts. Unlike the reach-back trio above, these are operator-settable — export them yourself to tune the budget without a rebuild:

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_LAUNCH_MAX_ATTEMPTS` | Number of consecutive failed launch attempts tolerated for one delegated child before the coordinator gives up loudly and tells the parent. Raise it to ride out a slow/cold container daemon; lower it to fail faster. | `4` |
| `CTXLOOM_LAUNCH_BACKOFF_BASE` | Delay (Go duration syntax, e.g. `500ms`) before the first retry; each further consecutive failure doubles it. | `200ms` |
| `CTXLOOM_LAUNCH_BACKOFF_MAX` | Ceiling (Go duration syntax, e.g. `1m`) the doubling backoff is capped at. | `30s` |

An unset or empty value keeps the default silently. A set-but-invalid value (unparseable, zero, or negative) also falls back to the default, but with a loud warning naming the variable — never silently to zero, which would reopen unbounded retry.

## Session Variables

`ctxloom run` exports these into the launched backend's environment. They are set for you — listed here for debugging:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_SESSION_HARP` | The session's harp name (e.g. `swift-amber-falcon`). Read back by ctxloom's own hooks and MCP server, and by taskloom |
| `CTXLOOM_PROJECT_ID` | Project identifier for session/task keying. Read back by ctxloom and taskloom (it's the second-priority rule in taskloom's project-id resolution, after `--project`) |
| `CTXLOOM_RESUMED_FROM` | Harp name of the session this one resumed from, if any. Read back by ctxloom's hooks and MCP server |
| `CTXLOOM_RESUMED_PARTS` | Companion to `CTXLOOM_RESUMED_FROM`: which parts of the prior session were carried into the resume. Read back alongside it |
| `CTXLOOM_CONTEXT_FILE` | Path to the assembled-context file for this session. This one is *not* read back by ctxloom — it's written into the launched engine's environment for the engine itself to consume (e.g. codex keys its context materialization off it); nothing under `internal/` reads it back |

## Template Variables

Fragment templates have no built-in variables: the mustache data comes entirely from the resolved profile's `variables:` map, and undefined variables render empty with a warning. See [Templating](/guides/templating) for usage.
