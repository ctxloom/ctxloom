---
sidebar_position: 6
---

# Contributing

Guide for contributing to SCM development.

## Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- [protoc](https://grpc.io/docs/protoc-installation/) for plugin protocol
- [golangci-lint](https://golangci-lint.run/) for linting

## Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, generate proto, build binary |
| `just validate` | Validate fragment YAML against JSON schema |
| `just build-scm` | Build only main binary |
| `just build-static` | Build static binaries (stripped, no CGO) |
| `just proto` | Generate protobuf code |

## Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |
| `just test-acceptance` | Run acceptance tests (requires built binary) |
| `just test-container` | Run all tests in Docker (matches CI) |

## Code Quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code |
| `just lint` | Lint code |

## Development Guidelines

### Fault Tolerance

SCM should be **fault tolerant** above all else. Even through most misconfigurations, the user should still end up in their defined LLM at the end of startup.

#### Core Principles

1. **Never block startup** - Configuration errors, missing files, network failures should produce warnings but never prevent the LLM from starting.

2. **Degrade gracefully** - If a feature fails to initialize, disable that feature and continue.

3. **Log, don't crash** - All errors should be logged to stderr with clear "SCM: warning:" prefixes.

4. **Sensible defaults** - When configuration is missing or invalid, fall back to reasonable defaults.

5. **Partial success is success** - If 9 out of 10 bundles sync successfully, report the failure but continue.

### Error Handling Pattern

```go
// Good: warn and continue
result, err := operations.SyncOnStartup(ctx, cfg)
if err != nil {
    fmt.Fprintf(os.Stderr, "SCM: warning: sync failed: %v\n", err)
    // Continue - don't return error
}

// Bad: fail on error
if err != nil {
    return fmt.Errorf("sync failed: %w", err)
}
```
