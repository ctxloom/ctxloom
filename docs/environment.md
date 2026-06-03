# Environment Variables

This is the complete set of environment variables ctxloom reads. Per its fault-tolerance philosophy, every one of these is optional: when unset or invalid, ctxloom falls back to a sensible default rather than failing.

The canonical list of host and session variables read in production lives in `internal/testsupport/testsupport.go` (`EnvKeys`), and a test enforces that every `CTXLOOM_*` variable read in production appears there. Keep this document in sync with that list.

## Diagnostics

| Variable | Values | Default | Purpose |
|----------|--------|---------|---------|
| `CTXLOOM_DEBUG_HTTP` | `1` to enable | off | Log every outbound HTTP request, plus errors and `4xx`/`5xx` responses, to stderr. |
| `CTXLOOM_VERBOSE` | `1` to enable | off | Enable verbose logging at process startup. |

`CTXLOOM_DEBUG_HTTP` is the single switch for **all** HTTP debugging in ctxloom. It currently only instruments the GitHub API client (`internal/remote/github.go`), because the cached-clone path has eliminated most other API traffic, but any HTTP transport added later should honor the same variable rather than introducing a parallel one. When enabled you'll see lines like:

```
ctxloom: GitHub API call: GET https://api.github.com/repos/owner/repo
ctxloom: GitHub API status: 404 for /repos/owner/repo
```

## Authentication

| Variable | Default | Purpose |
|----------|---------|---------|
| `GITHUB_TOKEN` | none | Auth token for GitHub forges (remote sync, discover, push). |
| `GH_TOKEN` | none | Alternate GitHub token. Checked after `GITHUB_TOKEN`, so it wins when both are set. |

These supply the token for the built-in `github` forge. A custom forge can name a different variable through its `token_env` config field; the value of that variable is then used as the clone and API token. The generic `git` forge carries no token here and relies on ambient git credentials.

## Editor and pager

| Variable | Default | Purpose |
|----------|---------|---------|
| `VISUAL` | none | Editor used to edit bundles, fragments, prompts, and config. |
| `EDITOR` | `nano` | Editor used when `VISUAL` is unset. |
| `PAGER` | none | Pager used to display long content such as remote pull diffs. |

Editor resolution order: the `editor` setting in config, then `VISUAL`, then `EDITOR`, then `nano`.

> `PAGER` is user-controlled and ctxloom executes it. This is standard Unix behavior, but be aware that `PAGER` can run an arbitrary command.

## External tools and paths

| Variable | Default | Purpose |
|----------|---------|---------|
| `CODEX_HOME` | `~/.codex` | Home directory for the Codex backend's config and state. |
| `HOME` | OS default | Standard home directory; roots `~/.ctxloom` and similar paths. |

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
