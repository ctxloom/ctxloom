## Context

### Security Fundamentals

**Secrets Management:**

- Never commit secrets (use environment variables)
- No hardcoded credentials or API keys
- Use secret management tools (vault, 1password, etc.)

**Input Validation:**

- Validate all external input
- Sanitize before use
- Reject invalid input early

**Database Security:**

- Use parameterized queries (never string concatenation)
- Apply principle of least privilege for DB users
- Encrypt sensitive data at rest

**General Practices:**

- Keep dependencies updated
- Follow principle of least privilege
- Audit logging for sensitive operations
- Validate file paths to prevent traversal attacks

**Code Review Security Checks:**

- User input used directly in queries/commands?
- Missing authentication/authorization checks?
- Sensitive data in logs?
- Overly permissive permissions?

