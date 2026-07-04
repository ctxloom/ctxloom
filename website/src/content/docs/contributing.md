---
title: "Contributing"
---

Guide for contributing to ctxloom development.

## Prerequisites

- Go 1.25+
- [just](https://github.com/casey/just) command runner
- Docker or Podman — the standard recipes build and run inside a devcontainer image, which carries the pinned tooling (golangci-lint, [buf](https://buf.build) for protobuf codegen) so you don't install it on the host

## Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, generate proto, build binary (in the devcontainer) |
| `just validate` | Validate fragment YAML against JSON schema |
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

The CLI reference is generated: the cobra command definitions in `internal/cli` (the `Short`/`Long`/`Example` fields) are the single source of truth. `just gen-docs` regenerates the man pages and the per-command website pages under `/reference/cli/`; CI fails on drift (`gen-docs-check`). Never hand-edit the generated `ctxloom_*.md` pages — edit the command definitions and regenerate. When adding or changing a command, write good `Long` and `Example` fields: they *are* the docs.

## Development Guidelines

### Fault Tolerance

ctxloom should be **fault tolerant** above all else. Even through most misconfigurations, the user should still end up in their defined LLM at the end of startup.

#### Core Principles

1. **Never block startup** - Configuration errors, missing files, network failures should produce warnings but never prevent the LLM from starting.

2. **Degrade gracefully** - If a feature fails to initialize, disable that feature and continue.

3. **Log, don't crash** - All errors should be logged to stderr with clear "ctxloom: warning:" prefixes.

4. **Sensible defaults** - When configuration is missing or invalid, fall back to reasonable defaults.

5. **Partial success is success** - If 9 out of 10 bundles sync successfully, report the failure but continue.

### Error Handling Pattern

```go
// Good: warn and continue
result, err := operations.SyncOnStartup(ctx, cfg)
if err != nil {
    fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", err)
    // Continue - don't return error
}

// Bad: fail on error
if err != nil {
    return fmt.Errorf("sync failed: %w", err)
}
```
