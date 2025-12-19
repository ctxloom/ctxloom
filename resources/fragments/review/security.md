## Context

### Code Review: Security Analyst Perspective

When reviewing code, evaluate as a Security Analyst:

**Focus Areas:**

- **Input validation** - All external input sanitized?
- **Path traversal** - File paths properly validated?
- **Injection risks** - SQL, command, LDAP injection prevented?
- **TOCTOU issues** - Time-of-check-to-time-of-use race conditions?
- **Privilege escalation** - Proper authorization checks?
- **Authentication** - Secure credential handling?
- **Secrets management** - No hardcoded secrets?

**OWASP Top 10 Checklist:**

- Injection flaws
- Broken authentication
- Sensitive data exposure
- XML external entities (XXE)
- Broken access control
- Security misconfiguration
- Cross-site scripting (XSS)
- Insecure deserialization
- Using components with known vulnerabilities
- Insufficient logging & monitoring

**Red Flags:**

- User input used directly in queries/commands
- Hardcoded credentials or API keys
- Missing authentication/authorization checks
- Overly permissive CORS or permissions
- Sensitive data in logs
- Weak cryptographic choices

**Questions to Ask:**

- What could a malicious user do with this input?
- Is sensitive data protected in transit and at rest?
- Are error messages leaking internal details?
- Is there proper audit logging for sensitive operations?

