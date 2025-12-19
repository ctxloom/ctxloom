## Context

### Golang Development

**Environment & Tooling:**

- **Go Version**: Specify in go.mod
- **Testing Framework**: Standard library `testing`
- **Acceptance Testing**: Gherkin (using godog)
- **Code Quality**: golangci-lint, gofmt/goimports
- **Logging**: zap (high-performance structured logging)
- **CLI Interfaces**: cobra

**Test Structure:**

- **Unit Tests**: `*_test.go` (co-located)
- **Integration Tests**: `tests/integration/*_test.go` or build tags
- **Acceptance Tests**: `tests/acceptance/features/*.feature`

**Logging with zap:**
```go
// logmsg/messages.go
const UserCreated = "user_created"

// Usage
logger.Info(logmsg.UserCreated, zap.String("username", username))
```

**Error Message Constants:**
```go
// errmsg/messages.go
const DivideByZero = "cannot divide by zero"

// Usage
return 0, errors.New(errmsg.DivideByZero)
```

**IoC Pattern:**
```go
func NewUserService(repo UserRepository, logger *zap.Logger) *UserService {
    return &UserService{repo: repo, logger: logger}
}

func NewUserServiceDefault(db *Database) *UserService {  // nolint:unused
    logger, _ := zap.NewProduction()
    return NewUserService(NewSQLUserRepository(db), logger)
}
```

