---
title: "Contributing"
---

Guide for contributing to ctxloom development.

## Prerequisites

- Go 1.26+
- [just](https://github.com/casey/just) command runner
- Docker or Podman — the standard recipes build and run inside a devcontainer image, which carries the pinned tooling (golangci-lint, [govulncheck](https://go.dev/doc/security/vuln/), [buf](https://buf.build) for protobuf codegen, Node.js for the opencode plugin tests) so you don't install it on the host

Every recipe that starts a container or builds an image (`dev-image`, and through it most of the table below) first runs a preflight that fails fast, rather than hanging, when the container engine cannot run containers: a timeout-bounded `docker info` (`version` for other engines), and for rootless docker, the runtime sockets a wedged daemon loses while still answering its API. The fix it names, `systemctl --user restart docker`, also restarts every other container on that daemon. It is skipped inside the devcontainer and under CI; `CTXLOOM_SKIP_CONTAINER_PREFLIGHT=1` bypasses it, and `CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT` (seconds, default 10) bounds the probe.

## Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, generate proto, build binary (in the devcontainer) |
| `just validate` | Validate config YAML against its JSON schema, and the version stamp the build will bake in |
| `just dev build-static` | Build static binaries (stripped, no CGO) in the devcontainer |
| `just proto` | Generate protobuf code (`buf generate`, in the devcontainer) |

`just dev <recipe>` runs any container-side recipe inside the devcontainer.

## Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |
| `just test-pkg <pkg> [go test args]` | Run one package's tests (with `-race`). The `tests/acceptance` and `tests/integration` trees get a 30m timeout unless you pass `-timeout` yourself |
| `just test-acceptance` | Run acceptance tests (requires built binary) |
| `just dev test` | Run all tests in the devcontainer (matches CI) |

### Mutation testing

[gomutants](https://github.com/gomutants/gomutants) is the one mutation backend; its settings live in `.gomutants.yml`, and `scripts/mutshard` plans, shards and judges its runs. Install it with `just test-mutation-install` (the version pinned in `.devcontainer/tool-versions.env`). The acceptance suite is not mutation-tested: gomutants mutates source and runs `go test`, so code reachable only through the built binary reports NOT COVERED.

| Command | Description |
|---------|-------------|
| `just test-mutation-diff BASE` | Mutate the lines changed since `BASE`, judged against the project thresholds (efficacy 80%) — the per-push gate CI runs, sharded, and what the pre-commit hook runs over the staged change (see [Git hooks](#git-hooks)) |
| `just test-mutation-pkg PKG [--diff BASE]` | The same, narrowed to one package directory and everything under it |
| `just test-mutation-ratchet PKG` | Mutate `PKG` whole and judge its survivor count (LIVED + NOT COVERED) against its row in `tests/mutation/survivor_baseline.txt`; `CTXLOOM_MUTATION_BASELINE=update` records the measured count as the row |
| `just test-mutation` | The whole module, on this machine (slow) |

### Live engine tests

`@live` acceptance cells drive a real engine and are skipped by the default
recipes. Each recipe below runs one cell, on purpose: a cell is a real model
turn, and a container cell may build an agent image first. `ENGINE` is a
registered engine name, `claude` or `opencode`.

| Command | Description |
|---------|-------------|
| `just engine-matrix ENGINE RUNTIME WORKSPACE` | The simplest round trip for one engine under one isolation cell (`host`, `container-rootless` or `container-rootful`; `none` or `worktree`) |
| `just live-delegation ENGINE` | A real delegated child whose own context comes back to the coordinator over `agent_send` |
| `just capability-probe PROBE FEATURE ENGINE RUNTIME WORKSPACE [VARIANT]` | One rung of the capability-probe ladder |
| `just plan-sentinel ENGINE [POSTURE]` | Whether the engine's plan posture really refuses a write |
| `just isolation-probe ENGINE AXIS` | One engine on one isolation axis |
| `just test-acceptance-live-container` | The whole live suite in a container; it requires claude's token and skips opencode's cells, because its image installs no opencode |

claude's cells run on Haiku and need `CLAUDE_CODE_OAUTH_TOKEN` exported (from
`claude setup-token`). opencode's run keyless on the free hosted
`opencode/big-pickle` model and need only the `opencode` binary and network. A
cell whose engine is missing or cannot authenticate skips loudly, saying why.

## Code Quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format the repository: gofmt over the Go, `buf format` over the protos (in the devcontainer), shfmt over the shell scripts. `scripts/fmt` defines which files each formatter owns; files under `testdata/` are left alone |
| `just fmt-check` | The same formatters as a gate: prints each one's drift and fails on any, writing nothing |
| `just lint` | Lint code. golangci-lint's cache is per checkout, under the gitignored `.cache/golangci-lint/<hash>`, so a worktree never reads another's cached findings; an exported `GOLANGCI_LINT_CACHE` overrides it |
| `just govulncheck` | Scan every package for known Go vulnerabilities its code can actually reach (govulncheck, pinned in `.devcontainer/tool-versions.env`), and fail on any. It queries the live database at vuln.go.dev, so it can turn red without a change to the tree. CI runs it as the Vulnerability scan job. Extra flags pass through, e.g. `just govulncheck -show traces` for full call paths |

## Git hooks

Hooks are managed by [lefthook](https://github.com/evilmartians/lefthook): `lefthook install` wires them into `.git/hooks`, and `lefthook.yml` declares each one with the reason it exists. A hook whose tool is missing fails the commit rather than skipping, so install the tools the hooks run:

| Command | Description |
|---------|-------------|
| `just build` | Also builds `bin/archlint`, which the architectural pre-commit hook runs. On a fresh checkout use this rather than a bare `just build-archlint`: archlint type-checks the whole module, so it needs the generated code (`just proto`) that `just build` produces first |
| `just gitleaks-install` | Installs [gitleaks](https://github.com/gitleaks/gitleaks) at the version pinned in `.devcontainer/tool-versions.env`, for the pre-commit secret scan |
| `just shfmt-install` | Installs [shfmt](https://github.com/mvdan/sh) at the version pinned in `.devcontainer/tool-versions.env`, for `just fmt` and the pre-commit shell formatter |
| `just test-mutation-install` | Installs gomutants, for the pre-commit mutation step |

The pre-commit hook runs its steps one at a time, in this order, and stops at the first failure:

1. **Formatters**, over the staged files of their kind: gofmt, `buf format` (only when a `.proto` is staged; it starts the devcontainer) and shfmt. A fully staged file is formatted and re-staged, so the commit carries the formatted bytes. A partially staged file (some of its changes staged, some not) is only checked: if its staged content is not formatted, the commit is refused with "run `just fmt`". The hook never rewrites it, because that would move the lines your unstaged changes apply to.
2. **Checks**: reprise (duplicate detection), archlint (architectural rules), lint-pins (no pinned version moves backwards) and gitleaks (secret scan).
3. **Mutation**, over the staged change only. The hook checks `HEAD` out into a throwaway worktree, applies your staged diff there, and runs `just test-mutation-diff HEAD` inside it. Unstaged edits and untracked files never reach it, and it never writes your working tree, index or stash. Only the lines the commit changes are mutated, never a whole package or tree, so a bigger diff takes longer. That is expected: the cost follows the size of the change. A commit with no staged production Go skips this step. Below the project threshold (80% efficacy) the commit is refused, listing the surviving mutants. gomutants runs the covering tests first, so a test that fails on what you staged fails here too, before any verdict.

The pre-commit secret scan (`just secrets-scan-staged`, which you can also run by hand) checks staged changes only, against `.gitleaks.toml` (gitleaks' default rules plus allowlists for known false positives). On a finding: a real credential must be unstaged and rotated; a deliberate test fixture gets a trailing `gitleaks:allow` comment on its line.

## Documentation Pipeline

The CLI reference is generated: the cobra command definitions in `internal/adapters/cli` (the `Short`/`Long`/`Example` fields) are the single source of truth. `just gen-docs` regenerates the man pages and the per-command website pages under `/reference/cli/`; CI fails on drift (`gen-docs-check`). Never hand-edit the generated `ctxloom_*.md` pages — edit the command definitions and regenerate. When adding or changing a command, write good `Long` and `Example` fields: they *are* the docs.

## Development Guidelines

### Startup faults fail loudly

A startup fault is **fatal by default**. Report it through package `strictness`
(`strictness.Fail`, `FailOnce`, `Record`) with a failure class and a fix-it
command. The warning still streams to stderr as it happens, and the startup
owner (`ctxloom run`) collects every fatal finding and aborts before launch,
listing each one with its fix, never just the first.

`--degraded` (or `CTXLOOM_DEGRADED=1`) is the escape hatch: findings are still
warned and recorded, but nothing aborts. Some findings refuse even then; a
missing engine credential for an isolated session home is one. There is no
config key for the mode, because a broken config cannot excuse itself.

```go
// Good: record the fault with its class and fix; strict mode aborts pre-launch
if syncErr != nil {
    strictness.Fail(strictness.ClassSync,
        "check the remote/network, or pass --degraded to launch anyway",
        "sync failed: %v", syncErr)
}

// Bad: a bare stderr line that strict mode never sees
if syncErr != nil {
    fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", syncErr)
}
```

### Partial failure exits non-zero

A command that collects per-item errors while still doing everything it could
returns `clidiag.WarnErrors(prog, errs)`: one warning per item and a non-zero
exit. Printing the warnings and returning `nil` reports a real failure as
success.
