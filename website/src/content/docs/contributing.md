---
title: "Contributing"
---

Guide for contributing to ctxloom development.

## Prerequisites

- Go 1.26+
- [just](https://github.com/casey/just) command runner
- Docker or Podman — the standard recipes build and run inside a devcontainer image, which carries the pinned tooling (golangci-lint, [buf](https://buf.build) for protobuf codegen) so you don't install it on the host

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
| `just test-acceptance` | Run acceptance tests (requires built binary) |
| `just dev test` | Run all tests in the devcontainer (matches CI) |

## Code Quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code |
| `just lint` | Lint code |

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
