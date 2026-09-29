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

Every claude that ctxloom launches, on the host or in a container, top-level or delegated, authenticates in the mode its agent declares (`auth:` — `login`, `token`, `api-key` or `cloud`; undeclared is `token`). Only that mode's credential reaches claude: a value you export for the declared mode wins over the stored one, and the other credential variables below are removed from the run's environment, along with any cloud-provider switch (`CLAUDE_CODE_USE_BEDROCK` and its siblings) for every mode but `cloud`. Nothing is copied into a session home. What a container run additionally mounts and rewrites is under [Containerized Agents](#containerized-agents).

| Variable | Description |
|----------|-------------|
| `CLAUDE_SECURESTORAGE_CONFIG_DIR` | Set for an `auth: login` agent, so the run shares your login and its refresh: on the host, to exactly where your own claude keeps its credential; in a container, to empty, pointing claude at the store mounted under its `$HOME` (see [Containerized Agents](#containerized-agents)). Removed for every other mode |
| `CLAUDE_CODE_OAUTH_TOKEN` | An `auth: token` agent's credential: the long-lived token `claude setup-token` prints. ctxloom mints it at your terminal the first time a run needs it (or with `ctxloom auth mint --mode token`) and keeps it owner-only at `~/.ctxloom/auth/claude-code.token`. A run with no terminal and nothing stored is refused, naming that command |
| `ANTHROPIC_API_KEY` | An `auth: api-key` agent's credential. Store it with `ctxloom auth set --mode api-key` (read from stdin, never argv); it lives at `~/.ctxloom/auth/claude-code.api-key` |
| `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` | A gateway's bearer and endpoint. Passed through for an `auth: cloud` agent; the bearer is removed for every other mode |
| `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_FOUNDRY` and the provider's own variables | An `auth: cloud` agent's configuration, passed through from your shell as claude's provider pages document them. For a container run, see [Containerized Agents](#containerized-agents) |

`ctxloom auth status` lists what is stored and who can read it (the file mode on unix, the ACL verdict on Windows), never the value. A binding selecting `engine_home: host` runs a host claude against your real `~/.claude` in place, still in the mode it declares; in a container it gets the container's own `$HOME`.

## Containerized Agents

Agents with `runtime: container-rootless` or `runtime: container-rootful` start the engine with a fresh `$HOME` and a scoped environment; your shell's environment does not cross wholesale.

The credential is whatever the agent's `auth:` mode resolves to on the host, exactly as under [Engine Authentication](#engine-authentication), and rides the engine's environment into the container, never the command line. The variables that section removes are absent. On top of that, each mode adds, for claude:

| `auth:` | In the container |
|---------|------------------|
| `login` | The directory your claude keeps its credential in (the `CLAUDE_SECURESTORAGE_CONFIG_DIR` or `CLAUDE_CONFIG_DIR` your shell sets, else `~/.claude`) is mounted **read-write** at the container's `$HOME/.claude`, and `CLAUDE_SECURESTORAGE_CONFIG_DIR` is set empty so claude reads it there. The whole directory crosses: the agent can use and refresh your login, and a `/logout` inside it signs you out. Refused when that directory does not exist, and refused on macOS, where the login lives in the Keychain and no container can reach it; the error suggests `auth: token` or `runtime: host` |
| `token` | `CLAUDE_CODE_OAUTH_TOKEN` only. Nothing is mounted |
| `api-key` | `ANTHROPIC_API_KEY` only. Nothing is mounted |
| `cloud` | The provider or gateway variables your shell exports. Each of `~/.aws` and `~/.config/gcloud` that exists is mounted **read-only** at the same place under `$HOME`, with `~/.aws/sso/cache` mounted read-write inside it so an SSO login can refresh. A file named by `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE` or `GOOGLE_APPLICATION_CREDENTIALS` is mounted read-only on its own and the variable is rewritten to its path inside the container; the run is refused when the variable does not name an existing regular file by its absolute path (a host run does not check) |

`ANTHROPIC_MODEL` and `ANTHROPIC_SMALL_FAST_MODEL` are not forwarded from your shell, and `ANTHROPIC_BASE_URL` only as part of `auth: cloud`.

| Variable | Description |
|----------|-------------|
| `TERM`, `COLORTERM` | Forwarded so the engine renders with the host terminal's actual capabilities instead of the image default (or `dumb`, which drops color and cursor control) |
| `PUID`, `PGID` | *Not* read from your environment. The isolation runtime sets them and passes them into the container: your own uid/gid on Linux and macOS, and on Windows (which has no POSIX uid) the image's own `ctxloom` user. The image's entrypoint remaps its baked-in `ctxloom` user to them and drops privileges before the engine starts, so files the engine writes into the bind-mounted project are owned by you, not by root. If the remap can't be performed (no usable `gosu`/`setpriv` in the image) the entrypoint refuses to run the engine as root and the launch fails; `--degraded` does not change that. Rootless Docker never sets these, because container-root there already is the launching user |

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
