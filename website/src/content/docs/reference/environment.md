---
title: "Environment Variables"
---

Environment variables that affect ctxloom behavior.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_VERBOSE` | Enable verbose logging. Also turns on a delegated child's launch trace (a container runner reports its auth route) | `0` (disabled) |
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

## Engine Authentication

Every claude that ctxloom launches, on the host or in a container, top-level or delegated, authenticates in the mode its agent declares (`auth:` — `login`, `token`, `api-key` or `cloud`; undeclared is `token`). Only that mode's credential reaches claude: a value you export for the declared mode wins over the stored one, and the other credential variables below are removed from the run's environment, along with any cloud-provider switch (`CLAUDE_CODE_USE_BEDROCK` and its siblings) for every mode but `cloud`. Nothing is copied into a session home or mounted into a container.

| Variable | Description |
|----------|-------------|
| `CLAUDE_SECURESTORAGE_CONFIG_DIR` | Set for an `auth: login` agent on the host, to exactly where your own claude keeps its credential, so the run shares your login and its refresh. Refused for a container agent |
| `CLAUDE_CODE_OAUTH_TOKEN` | An `auth: token` agent's credential: the long-lived token `claude setup-token` prints. ctxloom mints it at your terminal the first time a run needs it (or with `ctxloom auth mint --mode token`) and keeps it owner-only at `~/.ctxloom/auth/claude-code.token`. A run with no terminal and nothing stored is refused, naming that command |
| `ANTHROPIC_API_KEY` | An `auth: api-key` agent's credential. Store it with `ctxloom auth set --mode api-key` (read from stdin, never argv); it lives at `~/.ctxloom/auth/claude-code.api-key` |
| `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` | A gateway's bearer and endpoint. Passed through for an `auth: cloud` agent; the bearer is removed for every other mode |
| `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_FOUNDRY` and the provider's own variables | An `auth: cloud` agent's configuration, passed through from your shell as claude's provider pages document them. Provider credential files (`~/.aws`, gcloud's application-default credentials) are not carried into a container |

`ctxloom auth status` lists what is stored and who can read it (the file mode on unix, the ACL verdict on Windows), never the value. A binding selecting `engine_home: host` runs claude against your real `~/.claude` in place, still in the mode it declares.

## Containerized Agents

Agents with `runtime: container-rootless` or `runtime: container-rootful` pass a scoped set of host variables through to the engine inside the image:

| Variable | Description |
|----------|-------------|
| `CLAUDE_CODE_OAUTH_TOKEN` | Selects setup-token auth and is passed through by name; the stored token counts (see Engine Authentication). When none of this, `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set, the container run is refused |
| `ANTHROPIC_API_KEY` | Selects API-key auth and is passed through |
| `ANTHROPIC_AUTH_TOKEN` | Also selects token-based auth on its own, for a gateway that authenticates with `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` and no API key. Passed through |
| `ANTHROPIC_BASE_URL` | Forwarded when present, if `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set |
| `ANTHROPIC_MODEL` | Forwarded when present, if `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set. Selects the model for a containerized claude run |
| `ANTHROPIC_SMALL_FAST_MODEL` | Forwarded when present, if `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set |
| `TERM`, `COLORTERM` | Forwarded so the engine renders with the host terminal's actual capabilities instead of the image default (or `dumb`, which drops color and cursor control) |
| `PUID`, `PGID` | *Not* read from your environment — set by the isolation runtime from `os.Getuid()`/`os.Getgid()` and passed into the container. Under a rootful daemon (rootful Docker, Podman) the entrypoint uses them to remap the image's baked-in `ctxloom` user to your uid/gid and drop privileges to it before the engine starts, so files the engine writes into the bind-mounted project are owned by you, not by the container's generic user or by root. If the remap can't be performed (no usable `gosu`/`setpriv` in the image) the entrypoint refuses to run the engine as root and fails the launch loudly, unless `--degraded` (or `CTXLOOM_DEGRADED=1`) is in effect, which downgrades the refusal to a warning and lets the engine run as root. Rootless Docker never sets these — container-root there already is the launching user |

## Host and Engine Integration

These are read on the host (or inside the launched engine process) rather than crossing into a container — they don't appear in the scoped passthrough list above.

| Variable | Description |
|----------|-------------|
| `SSH_AUTH_SOCK` | ssh-agent socket used when signing a bundle with an ssh-agent-held key |

## Delegated Agents

A child session spawned under agent delegation (`agent_run` / agentcoord) receives a reach-back trio that lets it talk back to its coordinator. These are set for you by the coordinator, not something you export by hand — listed here for debugging a delegated child:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_COORD_URL` | The coordinator's MCP endpoint URL (`http://host:port/mcp`) |
| `CTXLOOM_COORD_CRED` | The child's bearer credential for authenticating back to the coordinator |
| `CTXLOOM_RUN_ID` | The coordinator-minted run id correlating this child to the run it was spawned for |

## Delegated Launch Retry

Tunables for the bounded launch-retry budget that gates a delegated child's (`agent_run`) launch attempts. Unlike the reach-back trio above, these are operator-settable — export them yourself to tune the budget without a rebuild:

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_LAUNCH_MAX_ATTEMPTS` | Number of consecutive failed launch attempts tolerated for one delegated child before the coordinator gives up loudly and tells the parent. Raise it to ride out a slow/cold container daemon; lower it to fail faster. | `4` |
| `CTXLOOM_LAUNCH_BACKOFF_BASE` | Delay (Go duration syntax, e.g. `500ms`) before the first retry; each further consecutive failure doubles it. | `200ms` |
| `CTXLOOM_LAUNCH_BACKOFF_MAX` | Ceiling (Go duration syntax, e.g. `1m`) the doubling backoff is capped at. | `30s` |

An unset or empty value keeps the default silently. A set-but-invalid value (unparseable, zero, or negative) also falls back to the default, but with a loud warning naming the variable — never silently to zero, which would reopen unbounded retry.

## Runner Owner-Loss Window

How long a delegated child's runner waits on an unreachable coordinator. Operator-settable, forwarded onto every runner (container runners included):

| Variable | Description | Default |
|----------|-------------|---------|
| `CTXLOOM_RUNNER_OWNER_LOSS_WINDOW` | How long (Go duration syntax, e.g. `5m`) a runner waits on an unreachable coordinator before exiting on its own, which lets a container child's `--rm` remove its container. Only waiting counts: a turn in progress runs to its end and the wait starts there. The runner keeps redialling and starts no new turn until its coordinator is back. | `2m` |

Read once when the runner starts. An unset or empty value keeps the default silently; a set-but-invalid value (unparseable, zero, or negative) keeps the default with a loud warning naming the variable.

## Session Variables

`ctxloom run` exports these into the launched backend's environment. They are set for you — listed here for debugging:

| Variable | Description |
|----------|-------------|
| `CTXLOOM_SESSION_HARP` | The session's harp name (e.g. `swift-amber-falcon`). Read back by ctxloom's own hooks and MCP server, and by taskloom |
| `CTXLOOM_PROJECT_ID` | Project identifier for session/task keying. Read back by ctxloom and taskloom (it's the second-priority rule in taskloom's project-id resolution, after `--project`) |
| `CTXLOOM_RESUMED_FROM` | Harp name of the session this one resumed from, if any. Read back by ctxloom's hooks and MCP server |
| `CTXLOOM_RESUMED_PARTS` | Companion to `CTXLOOM_RESUMED_FROM`: which parts of the prior session were carried into the resume. Read back alongside it |
| `CTXLOOM_CONTEXT_FILE` | Path to the assembled-context file for this session. This one is *not* read back by ctxloom — it's written into the launched engine's environment for the engine itself to consume; nothing under `internal/` reads it back |

## Template Variables

Fragment templates have no built-in variables: the mustache data comes entirely from the resolved profile's `variables:` map, and undefined variables render empty with a warning. See [Templating](/guides/templating) for usage.
