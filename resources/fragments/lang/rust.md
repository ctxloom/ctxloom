## Context

### Rust Development

**Environment & Tooling:**

- **Rust Version**: Specify in Cargo.toml
- **Testing Framework**: Built-in with `#[cfg(test)]`
- **Acceptance Testing**: Gherkin (using cucumber-rs)
- **Code Quality**: clippy, rustfmt, cargo-audit
- **Logging**: tracing (structured, async-aware)

**Test Structure:**

- **Unit Tests**: Inline `#[cfg(test)]` modules
- **Integration Tests**: `tests/*.rs`
- **Acceptance Tests**: `tests/acceptance/features/*.feature`

**Logging with tracing:**
```rust
// logmsg.rs
pub const USER_CREATED: &str = "user_created";

// Usage
info!(message = logmsg::USER_CREATED, username = %username);
```

**Error Message Constants:**
```rust
pub mod errmsg {
    pub const DIVIDE_BY_ZERO: &str = "cannot divide by zero";
}

#[derive(Error, Debug)]
pub enum AppError {
    #[error("{}", errmsg::DIVIDE_BY_ZERO)]
    DivideByZero,
}
```

**IoC Pattern:**
```rust
impl<R: UserRepository, E: EmailService> UserService<R, E> {
    pub fn new(user_repo: R, email_service: E) -> Self {
        Self { user_repo, email_service }
    }
}

pub type DefaultUserService = UserService<SqlUserRepository, SmtpEmailService>;

impl DefaultUserService {
    pub fn create(db: Database) -> Self {
        Self::new(SqlUserRepository::new(db), SmtpEmailService::new())
    }
}
```

**Best Practices:**

- **Memory:** Use `Arc<RwLock<T>>` for read-heavy, `Arc<Mutex<T>>` for write-heavy, accept `&str` parameters
- **Concurrency:** Thread-safe operations, minimal lock hold times, consistent lock order, document ordering
- **Error Handling:** `Result<T, E>`, `?` operator, NEVER `unwrap()`/`expect()` in library code
- **Code Style:** `cargo fmt`, fix `clippy` warnings, `///` doc comments explain WHY not WHAT

## Variables

```yaml
language: rust
test_framework: builtin
linter: clippy
logger: tracing
```
