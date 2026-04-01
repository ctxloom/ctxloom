---
title: "Ad-Hoc Context Assembly"
---

Build context on the fly without creating profile files. Perfect for one-off tasks, experimentation, and quick context changes.

## The Basics

Instead of creating a profile YAML file, use command-line flags to assemble context dynamically:

```bash
# Using bundles
ctxloom run -f go-development -f testing-patterns "help me write tests"

# Using tags
ctxloom run -t golang -t testing "help me write tests"

# Combining both
ctxloom run -f security -t best-practices "review this code"
```

## Building Faux Profiles

### Scenario: Quick Go Development Session

Instead of creating `profiles/go-dev.yaml`:

```bash
# Ad-hoc "profile" with multiple bundles
ctxloom run \
  -f go-development \
  -f testing-patterns \
  -f error-handling \
  "implement the user service"
```

### Scenario: Security Review

Instead of creating `profiles/security-review.yaml`:

```bash
# Ad-hoc security context
ctxloom run \
  -f security#fragments/owasp-top-10 \
  -f security#fragments/auth-patterns \
  -f security#fragments/input-validation \
  -t security \
  "review this authentication code"
```

### Scenario: Multi-Language Project

```bash
# Frontend work
ctxloom run -f typescript -f react -f css-patterns "build the dashboard component"

# Backend work
ctxloom run -f go-development -f postgres -f api-design "implement the API endpoint"

# Full-stack context
ctxloom run \
  -f typescript \
  -f go-development \
  -f api-design \
  "design the data flow between frontend and backend"
```

## Fragment Reference Syntax

### By Name (searches all bundles)

```bash
ctxloom run -f error-handling "help with errors"
```

### Fully Qualified

```bash
ctxloom run -f go-development#fragments/error-handling "help with errors"
```

### Multiple Fragments from Same Bundle

```bash
ctxloom run \
  -f go-development#fragments/testing \
  -f go-development#fragments/error-handling \
  -f go-development#fragments/concurrency \
  "review this code"
```

### Remote Fragments (without pulling)

```bash
ctxloom run \
  -f ctxloom-default/security#fragments/owasp \
  -f ctxloom-default/testing#fragments/tdd \
  "help me write secure tests"
```

## Tag-Based Assembly

Tags let you pull in all fragments with matching tags:

```bash
# All fragments tagged "security"
ctxloom run -t security "review for vulnerabilities"

# Multiple tags (OR logic - matches any)
ctxloom run -t security -t authentication "review auth code"

# Combine with specific fragments
ctxloom run -t testing -f go-development#fragments/mocking "write unit tests"
```

## Combining with Profiles

Start with a profile, add more context:

```bash
# Base profile + extra fragments for this task
ctxloom run -p developer -f security -f performance "optimize this endpoint"

# Profile + tags
ctxloom run -p go-dev -t database -t caching "implement data layer"
```

## Preview Before Running

Always preview complex ad-hoc assemblies:

```bash
# See what would be assembled
ctxloom run \
  -f go-development \
  -f testing-patterns \
  -f security \
  --dry-run

# See the actual content
ctxloom run \
  -f go-development \
  -f testing-patterns \
  --dry-run --print
```

## Real-World Examples

### Code Review with Specific Focus

```bash
# Performance-focused review
ctxloom run \
  -f performance#fragments/profiling \
  -f performance#fragments/optimization \
  -f go-development#fragments/concurrency \
  "review this code for performance issues"

# Security-focused review
ctxloom run \
  -f security#fragments/owasp-top-10 \
  -f security#fragments/injection \
  -f security#fragments/auth \
  "security review this authentication handler"
```

### Learning a New Topic

```bash
# Pull in comprehensive learning context
ctxloom run \
  -t kubernetes \
  -t containers \
  -f devops#fragments/k8s-patterns \
  "explain how to set up a deployment"
```

### Debugging Session

```bash
# Context for debugging
ctxloom run \
  -f go-development#fragments/debugging \
  -f go-development#fragments/profiling \
  -f logging-patterns \
  "help me debug this memory leak"
```

### Writing Documentation

```bash
# Documentation-focused context
ctxloom run \
  -f documentation#fragments/api-docs \
  -f documentation#fragments/readme-patterns \
  -t documentation \
  "write API documentation for this service"
```

### Database Work

```bash
# Database context
ctxloom run \
  -f postgres#fragments/queries \
  -f postgres#fragments/optimization \
  -f postgres#fragments/migrations \
  "optimize this slow query"
```

## Tips for Ad-Hoc Assembly

### 1. List Available Fragments First

```bash
# See what's available
ctxloom fragment list

# Filter by bundle
ctxloom fragment list --bundle go-development

# Search by name
ctxloom fragment list | grep security
```

### 2. Check Fragment Content

```bash
# Preview a fragment before using
ctxloom fragment show go-development#fragments/testing
```

### 3. Use Shell Aliases for Common Combinations

```bash
# In your .bashrc/.zshrc
alias ctxloom-go='ctxloom run -f go-development -f testing-patterns'
alias ctxloom-security='ctxloom run -f security -t security'
alias ctxloom-review='ctxloom run -f code-review -f best-practices'

# Then use:
ctxloom-go "implement the handler"
ctxloom-security "review this code"
```

### 4. Create Shell Functions for Complex Assemblies

```bash
# In your .bashrc/.zshrc
ctxloom-fullstack() {
  ctxloom run \
    -f typescript \
    -f react \
    -f go-development \
    -f postgres \
    -f api-design \
    "$@"
}

# Use:
ctxloom-fullstack "design the user registration flow"
```

### 5. Save Successful Combinations as Profiles

If you find yourself using the same ad-hoc combination repeatedly:

```bash
# This works well - save it!
ctxloom run -f go-development -f testing -f security "..."

# Create a profile for future use
ctxloom profile create go-secure \
  -b go-development \
  -b testing \
  -b security \
  -d "Go development with security focus"
```

## When to Use Ad-Hoc vs Profiles

### Use Ad-Hoc When:
- One-off tasks
- Experimenting with different context combinations
- Quick additions to your base profile
- Task requires unusual fragment combination

### Use Profiles When:
- Same combination used repeatedly
- Team needs consistent context
- Complex inheritance chains
- Want to version control the configuration

## MCP Tool Equivalent

The same ad-hoc assembly works via MCP:

```json
{
  "tool": "assemble_context",
  "arguments": {
    "bundles": ["go-development", "testing-patterns", "security"],
    "tags": ["best-practices"]
  }
}
```

This lets AI assistants dynamically assemble context based on the current task.
