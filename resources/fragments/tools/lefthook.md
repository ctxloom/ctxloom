## Context

### Git Hooks: lefthook

**Configuration:**

- Configure in `lefthook.yml` at repository root
- Language-agnostic, fast parallel execution
- Runs linting, formatting, tests on git hooks

**Example lefthook.yml:**

```yaml
pre-commit:
  parallel: true
  commands:
    lint:
      run: just lint
    format:
      run: just format
    test:
      run: just test-fast

pre-push:
  commands:
    full-test:
      run: just test
```

**Pre-commit Rules:**

- Run linters (ruff, clippy, golangci-lint)
- Run formatters (ruff format, cargo fmt, gofmt)
- Run fast tests
- Fix all issues before committing - do not commit broken code

**Pre-commit Errors:**

- Fix automatically without asking for verification
- If hook fails, address the issue before retrying

**Bypass:**

- Only for WIP on feature branches
- Use `--no-verify` with documented reasoning

