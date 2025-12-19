## Context

### Inversion of Control Pattern

**Default Factory Pattern:**

Every component requiring dependencies uses two constructors:

1. **Testing constructor**: Explicit dependency injection, accepts mocks
2. **Default factory**: Factory method that creates real dependencies

**Example (Go):**

```go
// Testing constructor - accepts dependencies
func NewUserService(repo UserRepository, logger *zap.Logger) *UserService {
    return &UserService{repo: repo, logger: logger}
}

// Default factory - creates real dependencies
func NewUserServiceDefault(db *Database) *UserService {
    logger, _ := zap.NewProduction()
    return NewUserService(NewSQLUserRepository(db), logger)
}
```

**Key Principles:**

- Always define interfaces/protocols for dependencies
- Never instantiate dependencies inside testing constructor
- Testing constructor accepts all dependencies
- Default factory creates real dependencies
- Keep default factory logic simple (just wiring)
- Exclude default factory from coverage (tested via integration tests)

