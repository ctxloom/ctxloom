# Environment Variables

This is the complete set of environment variables ctxloom reads. Per its fault-tolerance philosophy, every one of these is optional: when unset or invalid, ctxloom falls back to a sensible default rather than failing.

The canonical list of host and session variables read in production lives in `internal/testsupport/testsupport.go` (`EnvKeys`), and a test enforces that every `CTXLOOM_*` variable read in production appears there. Keep this document in sync with that list.

## Project root

| Variable | Default | Purpose |
|----------|---------|---------|
| `CTXLOOM_ROOT` | unset | Explicit, authoritative project root. When set to a valid directory it supersedes both git-root detection and the cwd walk-up, so config resolves at `$CTXLOOM_ROOT/.ctxloom` and the session work directory is `$CTXLOOM_ROOT` itself. |

`CTXLOOM_ROOT` is a single, predictable knob for layouts where the working directory is not the project root: monorepo subtrees, git worktrees, CI checkouts, containers, and projects with no `.git`. It is purely an override at the top of the existing resolution chain — removing it restores prior behavior exactly.

Resolution precedence (highest wins):

1. An explicit per-call override (`--project` flag, or `config.WithAppDir`).
2. `CTXLOOM_ROOT`, when set and a valid directory.
3. Git root (`git rev-parse`-style walk-up).
4. cwd walk-up for a `.ctxloom` directory, else the current directory.
5. `~/.ctxloom` (config only — the home fallback when no project directory is found).

Behavior:

- **Valid directory:** authoritative. ctxloom resolves config at `$CTXLOOM_ROOT/.ctxloom` and **creates** that directory if it does not exist (mirroring the way the home fallback creates `~/.ctxloom`). A missing `config.yaml` inside it is tolerated — defaults fill in so you still land in a working LLM.
- **Set but invalid** (path missing or not a directory): ctxloom emits `ctxloom: warning: CTXLOOM_ROOT ...` once to stderr and falls through to git-root / cwd as if it were unset. A bad value never blocks startup.
- **Unset:** no warning; the prior mechanisms apply unchanged.

A relative value is anchored to the launching working directory. The variable is inherited by the LLM subprocess and MCP server ctxloom launches; ctxloom does not synthesize one when you haven't set it.

## Diagnostics

| Variable | Values | Default | Purpose |
|----------|--------|---------|---------|
| `CTXLOOM_DEBUG_HTTP` | `1` to enable | off | Log every outbound HTTP request, plus errors and `4xx`/`5xx` responses, to stderr. |
| `CTXLOOM_VERBOSE` | `1` to enable | off | Enable verbose logging at process startup. Also turns on delegated-child launch diagnostics: the coordinator's spawner forwards the child's `llm serve` plugin stderr (which carries the ACP adapter's stderr) through its own process stderr at trace level. |

`CTXLOOM_DEBUG_HTTP` is the single switch for **all** HTTP debugging in ctxloom. It currently only instruments the GitHub API client (`internal/remote/github.go`), because the cached-clone path has eliminated most other API traffic, but any HTTP transport added later should honor the same variable rather than introducing a parallel one. When enabled you'll see lines like:

```
ctxloom: GitHub API call: GET https://api.github.com/repos/owner/repo
ctxloom: GitHub API status: 404 for /repos/owner/repo
```

## Delegated child launch retry

| Variable | Values | Default | Purpose |
|----------|--------|---------|---------|
| `CTXLOOM_LAUNCH_MAX_ATTEMPTS` | positive integer | `4` | Number of CONSECUTIVE failed launch attempts the coordinator tolerates for one delegated child (harp) before giving up loudly and telling the parent. Raise it to ride out a slow or cold container daemon; lower it to fail faster and surface a genuinely broken launch (bad image, no auth, unreachable daemon) sooner. |
| `CTXLOOM_LAUNCH_BACKOFF_BASE` | Go duration (e.g. `500ms`) | `200ms` | Delay before the FIRST retry; each further consecutive failure doubles it. Raise it to space attempts out further against a daemon that's slow to recover. |
| `CTXLOOM_LAUNCH_BACKOFF_MAX` | Go duration (e.g. `1m`) | `30s` | Ceiling the doubling backoff is capped at. Raise it to let the backoff keep growing across a longer cold-daemon recovery. |

Each is read once, at coordinator startup. An unset or empty value falls back to the default silently (the ordinary, unconfigured case); a set-but-invalid value (unparseable, zero, or negative) falls back to the default too, but LOUDLY — a warning names the variable and its bad value — because a zero or negative override here would silently reopen the unbounded-retry bug this budget exists to close (a broken launch spun for 49 minutes at ~2 attempts/sec before this gate existed).

## Authentication

| Variable | Default | Purpose |
|----------|---------|---------|
| `GITHUB_TOKEN` | none | Auth token for GitHub forges (deps pull, discover, push). |
| `GH_TOKEN` | none | Alternate GitHub token. Checked after `GITHUB_TOKEN`, so it wins when both are set. |

These supply the token for the built-in `github` forge. A custom forge can name a different variable through its `token_env` config field; the value of that variable is then used as the clone and API token. The generic `git` forge carries no token here and relies on ambient git credentials.

## Editor and pager

| Variable | Default | Purpose |
|----------|---------|---------|
| `VISUAL` | none | Editor used to edit bundles, fragments, prompts, and config. |
| `EDITOR` | `nano` | Editor used when `VISUAL` is unset. |
| `PAGER` | none | Pager used to display long content such as deps pull diffs. |

Editor resolution order: the `editor` setting in config, then `VISUAL`, then `EDITOR`, then `nano`.

> `PAGER` is user-controlled and ctxloom executes it. This is standard Unix behavior, but be aware that `PAGER` can run an arbitrary command.

## External tools and paths

| Variable | Default | Purpose |
|----------|---------|---------|
| `CODEX_HOME` | `~/.codex` | Home directory for the Codex backend's config and state. |
| `HOME` | OS default | Standard home directory; roots `~/.ctxloom` and similar paths. |

> Status: the Codex backend is implemented and hermetically tested; live operation is untested (no codex account on any dev host).

## Set by ctxloom for child processes

ctxloom exports these into the LLM subprocess and the MCP server it launches. You don't normally set them yourself; they're documented so the behavior is traceable.

| Variable | Purpose |
|----------|---------|
| `CTXLOOM_SESSION_HARP` | The active session (harp) name. Scopes session resolution, task logs, and HUD output to this session. |
| `CTXLOOM_PROJECT_ID` | Project identifier that scopes the task store. When unset, the task store degrades rather than blocking. |
| `CTXLOOM_RESUMED_FROM` | When resuming, the harp name the session was resumed from. |
| `CTXLOOM_RESUMED_PARTS` | When resuming, the number of context parts carried over. |

## Testing and development

These are read only by test and mock code paths, not by normal operation.

| Variable | Purpose |
|----------|---------|
| `CTXLOOM_BINARY` | Explicit path to the ctxloom binary under test. When unset, the harness probes the repo root, `$GOPATH/bin`, `$HOME/go/bin`, `$HOME/.local/bin`, then `PATH`. |
| `GOPATH` | Read only by the integration test harness as one candidate location (`$GOPATH/bin/ctxloom`) for the binary under test. The shipped CLI never reads it. |
| `CTXLOOM_MOCK_RESPONSE` | Canned response for the mock LLM backend. |
| `CTXLOOM_MOCK_RECORD_FILE` | File the mock backend records its input to. |
| `CTXLOOM_MOCK_EXIT_CODE` | Exit code the mock backend returns. |
