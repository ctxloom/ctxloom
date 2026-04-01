---
title: "Common Workflows"
---

Practical workflows for using ctxloom effectively in your daily development.

## Getting Started Workflow

### 1. Initialize ctxloom

```bash
# In your project directory
ctxloom init

# Or initialize globally
ctxloom init --home
```

### 2. Discover and Add Bundles

```bash
# Find relevant bundles
ctxloom remote discover golang

# Add a remote
ctxloom remote add community alice/ctxloom-golang

# Browse what's available
ctxloom remote browse community

# Pull bundles you want
ctxloom fragment install community/go-development
ctxloom fragment install community/testing-patterns
```

### 3. Create a Profile

```bash
# Create a development profile
ctxloom profile create go-dev \
  -b go-development \
  -b testing-patterns \
  -d "Go development environment"

# Set as default
ctxloom remote default go-dev
```

### 4. Start Coding

```bash
# Your context is now automatically injected
ctxloom run  # or just start Claude Code
```

## Daily Development Workflow

### Morning Setup

```bash
# Sync any remote updates
ctxloom remote sync

# Check your current profile
ctxloom profile show default
```

### During Development

Your context is automatically available. For specific tasks:

```bash
# Add security context for a security review
ctxloom run -f security#fragments/owasp "review this authentication code"

# Use a specific profile for frontend work
ctxloom run -p frontend-dev "help with React component"

# Preview what context will be used
ctxloom run --dry-run --print
```

### End of Day

```bash
# If you created new fragments, commit them
git add .ctxloom/
git commit -m "Update ctxloom configuration"
```

## Team Onboarding Workflow

### For Team Leads

1. **Create team bundles repository**:

```bash
mkdir team-ctxloom && cd team-ctxloom
mkdir -p ctxloom/v1/bundles ctxloom/v1/profiles
```

2. **Add team standards**:

```yaml
# ctxloom/v1/bundles/team-standards.yaml
version: "1.0"
description: Team coding standards
fragments:
  code-style:
    content: |
      # Team Code Style
      - Use gofmt for all Go code
      - 100 character line limit
      - Descriptive variable names
```

3. **Create team profile**:

```yaml
# ctxloom/v1/profiles/team-developer.yaml
description: Standard team development environment
bundles:
  - team-standards
  - security-basics
```

4. **Publish**:

```bash
git init && git add . && git commit -m "Initial team ctxloom"
git remote add origin https://github.com/myorg/ctxloom-team.git
git push -u origin main
```

### For New Team Members

```bash
# Add team remote
ctxloom remote add team myorg/ctxloom-team

# Sync team bundles
ctxloom remote sync

# Use team profile
ctxloom profile create my-dev --parent team/team-developer
ctxloom profile default my-dev
```

## Project-Specific Workflow

### Setting Up a New Project

```bash
cd my-project
ctxloom init

# Create project-specific profile
ctxloom profile create project \
  --parent go-dev \
  -b project-specific \
  -d "This project's development context"

ctxloom profile default project
```

### Project Bundle

Create a bundle specific to your project:

```yaml
# .ctxloom/bundles/project-specific.yaml
version: "1.0"
description: Project-specific context

fragments:
  architecture:
    content: |
      # Project Architecture

      This project uses:
      - Clean architecture with domain/usecase/infrastructure layers
      - PostgreSQL for persistence
      - Redis for caching
      - gRPC for internal services

  conventions:
    content: |
      # Project Conventions

      - All handlers in internal/handlers/
      - Domain models in internal/domain/
      - Use structured logging with zap
```

## Multi-Language Workflow

### Switching Contexts

```bash
# Create language-specific profiles
ctxloom profile create go-work -b go-development -b go-testing
ctxloom profile create python-work -b python-development -b python-testing
ctxloom profile create frontend-work -b typescript -b react

# Switch based on current task
ctxloom profile default go-work      # Working on Go
ctxloom profile default python-work  # Switching to Python
```

### Per-Directory Configuration

Use different `.ctxloom/` configurations in different project directories:

```
~/projects/
├── go-api/
│   └── .ctxloom/
│       └── profiles/default.yaml  # Go-focused
├── python-ml/
│   └── .ctxloom/
│       └── profiles/default.yaml  # Python/ML-focused
└── react-app/
    └── .ctxloom/
        └── profiles/default.yaml  # Frontend-focused
```

## Security Review Workflow

### Setup

```bash
# Add security bundles
ctxloom fragment install ctxloom-default/security
ctxloom fragment install ctxloom-default/owasp
```

### Conducting Reviews

```bash
# General security review
ctxloom run -t security "review this code for security issues"

# OWASP-focused review
ctxloom run -f security#fragments/owasp-top-10 "check for OWASP top 10 vulnerabilities"

# Authentication-specific
ctxloom run -f security#fragments/auth-patterns "review authentication implementation"
```

## Code Review Workflow

### Preparing Context

```bash
# Create a code review profile
ctxloom profile create reviewer \
  -b code-quality \
  -b testing-patterns \
  -b security-basics \
  -d "Code review context"
```

### During Review

```bash
# Use review profile
ctxloom run -p reviewer "review this PR for code quality"

# Add specific concerns
ctxloom run -p reviewer -f performance#fragments/optimization \
  "review for performance issues"
```

## CI/CD Integration Workflow

### In CI Pipeline

```yaml
# .github/workflows/ci.yml
jobs:
  lint:
    steps:
      - uses: actions/checkout@v4
      - name: Setup ctxloom
        run: |
          go install github.com/ctxloom/ctxloom@latest
          ctxloom remote sync

      - name: AI Code Review
        run: |
          ctxloom run -p code-reviewer "review changes in this PR" \
            --output review.md
```

### Lockfile for Reproducibility

```bash
# Generate lockfile
ctxloom remote lock

# Commit lockfile
git add .ctxloom/lock.yaml
git commit -m "Lock ctxloom dependencies"
```

In CI:

```bash
# Install exact versions
ctxloom remote install
```

## Troubleshooting Workflow

### When Context Isn't Working

```bash
# Check current configuration
ctxloom profile show default

# Preview assembled context
ctxloom run --dry-run --print

# Check hooks are applied
cat .claude/settings.json | jq '.hooks'

# Reapply hooks
ctxloom hooks apply
```

### When Bundles Are Missing

```bash
# Check what's installed
ctxloom fragment list

# Check what's available remotely
ctxloom remote browse ctxloom-default

# Sync missing dependencies
ctxloom remote sync
```

## Tips and Best Practices

### Keep Context Focused

```bash
# Instead of one huge profile
ctxloom profile create everything -b bundle1 -b bundle2 -b bundle3...

# Create task-specific profiles
ctxloom profile create api-dev -b go-development -b api-patterns
ctxloom profile create testing -b testing-patterns -b mocking
ctxloom profile create security -b security -b owasp
```

### Use Tags Effectively

```yaml
# In your bundles
fragments:
  quick-reference:
    tags: [quick, cheatsheet]
    content: ...

  detailed-guide:
    tags: [detailed, learning]
    content: ...
```

```bash
# Quick reference only
ctxloom run -t quick "remind me of the syntax"

# Detailed learning
ctxloom run -t detailed "explain this concept"
```

### Version Control Your Configuration

```bash
# Always commit ctxloom configuration
git add .ctxloom/
git commit -m "Update ctxloom configuration"
```

### Regular Maintenance

```bash
# Weekly: sync remote updates
ctxloom remote sync

# Monthly: review and clean up profiles
ctxloom profile list
ctxloom fragment list

# As needed: update lockfile
ctxloom remote lock
```
