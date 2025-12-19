## Context

### Task Runner: just (justfile)

**Usage:**

- Define all common tasks in `justfile`
- Examples: `just test`, `just lint`, `just build`, `just run`
- Language-agnostic task runner

**Best Practices:**

- Use `git rev-parse --show-toplevel` to identify repository root (call it TOP)
- Base all just targets with paths on TOP
- Do NOT run commands that may need to be repeated directly in terminal
- Build a just target for commands used 3+ times
- Document what commands do, caveats, requirements, prerequisites

**Example justfile:**

```just
# Run all tests
test:
    go test ./...

# Run linter
lint:
    golangci-lint run

# Build the binary (uses backtick command substitution)
build:
    go build -o `git rev-parse --show-toplevel`/bin/app ./cmd/app
```

