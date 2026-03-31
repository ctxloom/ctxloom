---
title: "Sharing Bundles"
---

# Sharing Bundles

Share your context bundles with your team or the community by creating an SCM repository.

## Repository Structure

An SCM repository follows this structure:

```
my-scm-repo/
├── scm/
│   └── v1/
│       ├── bundles/
│       │   ├── my-bundle.yaml
│       │   └── another-bundle.yaml
│       └── profiles/
│           └── my-profile.yaml
└── README.md
```

The `scm/v1/` directory is required for SCM to recognize the repository as a valid remote.

## Creating a Bundle

### Bundle File Structure

```yaml
# scm/v1/bundles/go-development.yaml
version: "1.0"
description: Go development context and best practices
author: your-name
tags:
  - golang
  - development

fragments:
  testing:
    tags:
      - testing
    content: |
      # Go Testing Best Practices

      - Use table-driven tests
      - Use testify/assert for assertions
      - Name tests descriptively: TestFunction_Scenario_Expected

  error-handling:
    tags:
      - errors
    content: |
      # Go Error Handling

      - Always check errors immediately
      - Wrap errors with context: fmt.Errorf("operation: %w", err)
      - Use sentinel errors sparingly

prompts:
  code-review:
    description: Review Go code for best practices
    tags:
      - review
    content: |
      Review this Go code for:
      - Error handling completeness
      - Test coverage
      - Idiomatic patterns
```

### Bundle Fields

| Field | Required | Description |
|-------|----------|-------------|
| `version` | Yes | Semantic version (e.g., `1.0`, `2.1.3`) |
| `description` | No | Human-readable description |
| `author` | No | Author name or organization |
| `tags` | No | Bundle-level tags (inherited by all items) |
| `fragments` | No | Map of fragment definitions |
| `prompts` | No | Map of prompt definitions |
| `mcp` | No | Map of MCP server configurations |

### Fragment Fields

| Field | Required | Description |
|-------|----------|-------------|
| `content` | Yes | The fragment content (markdown) |
| `tags` | No | Additional tags (merged with bundle tags) |
| `variables` | No | Template variables this fragment uses |
| `notes` | No | Human-readable notes (not sent to AI) |
| `no_distill` | No | Prevent automatic distillation |

## Creating a Profile

```yaml
# scm/v1/profiles/go-developer.yaml
description: Complete Go development environment
parents:
  - base-developer  # Inherit from another profile
bundles:
  - go-development
  - testing-patterns
tags:
  - golang
  - best-practices
```

## Publishing to GitHub

### 1. Create Repository

```bash
# Create new repo
mkdir my-scm-bundles
cd my-scm-bundles
git init

# Create structure
mkdir -p scm/v1/bundles scm/v1/profiles
```

### 2. Add Your Content

Create your bundle and profile YAML files in the appropriate directories.

### 3. Add README

```markdown
# My SCM Bundles

Context bundles for [description].

## Installation

```bash
ctxloom remote add mybundles username/my-scm-bundles
ctxloom fragment install mybundles/go-development
```

## Available Bundles

- **go-development** - Go best practices and patterns
- **testing-patterns** - Testing strategies and examples
```

### 4. Push to GitHub

```bash
git add .
git commit -m "Initial SCM bundles"
git remote add origin https://github.com/username/my-scm-bundles.git
git push -u origin main
```

## Making Your Repository Discoverable

### Naming Convention

Name your repository `scm` or `scm-*` for automatic discovery:

- `scm` - General SCM content
- `scm-golang` - Go-specific bundles
- `scm-security` - Security-focused content
- `scm-team-standards` - Team standards

### GitHub Topics

Add relevant topics to your repository:

- `scm-bundles`
- `claude-code`
- `ai-context`
- Language-specific: `golang`, `python`, `typescript`

### Description

Write a clear description that helps users find your bundles:

> "SCM bundles for Go development: testing patterns, error handling, and best practices"

## Versioning

### Semantic Versioning

Use semantic versioning for bundles:

- **Major** (1.0 → 2.0): Breaking changes
- **Minor** (1.0 → 1.1): New fragments/features
- **Patch** (1.0.0 → 1.0.1): Bug fixes, typo corrections

### Git Tags

Tag releases for version pinning:

```bash
git tag v1.0.0
git push origin v1.0.0
```

Users can then pin to specific versions:

```bash
ctxloom fragment install mybundles/go-development@v1.0.0
```

## Best Practices

### Content Quality

1. **Be concise** - AI context has size limits
2. **Be specific** - Vague guidance isn't helpful
3. **Be actionable** - Include examples and patterns
4. **Test your content** - Use your bundles before publishing

### Organization

1. **One topic per bundle** - Don't mix unrelated content
2. **Use tags consistently** - Enable profile-based selection
3. **Document variables** - If using templates, document required variables
4. **Include examples** - Show how to use your bundles

### Maintenance

1. **Keep bundles updated** - Review and update regularly
2. **Accept contributions** - Enable issues and PRs
3. **Changelog** - Document changes between versions
4. **Deprecation** - Clearly mark deprecated content

## Team Repositories

For team/organization use:

### Private Repositories

SCM works with private repos when authenticated:

```bash
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
ctxloom remote add team https://github.com/myorg/scm-internal
```

### Monorepo Structure

For larger organizations:

```
org-scm/
├── scm/
│   └── v1/
│       ├── bundles/
│       │   ├── frontend/
│       │   │   ├── react.yaml
│       │   │   └── typescript.yaml
│       │   ├── backend/
│       │   │   ├── go.yaml
│       │   │   └── python.yaml
│       │   └── shared/
│       │       ├── security.yaml
│       │       └── testing.yaml
│       └── profiles/
│           ├── frontend-dev.yaml
│           ├── backend-dev.yaml
│           └── fullstack-dev.yaml
└── README.md
```

### Access Control

- Use GitHub/GitLab teams for access control
- Consider separate repos for different access levels
- Public bundles in public repo, sensitive standards in private

## Validation

Before publishing, validate your bundles:

```bash
# Check YAML syntax
ctxloom validate scm/v1/bundles/my-bundle.yaml

# Test loading
ctxloom fragment show my-bundle#fragments/testing

# Test in a profile
ctxloom run --dry-run -f my-bundle#fragments/testing
```

## Example Repositories

Look at these repositories for inspiration:

- Community bundles follow the patterns described here
- Check the `scm-main` default remote for examples
- Search GitHub for `scm-` repositories
