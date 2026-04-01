---
title: "Profiles"
---

A **profile** is a named configuration that references bundles, tags, and variables. Profiles enable quick context switching.

## Profile Structure

Profiles are stored in `.ctxloom/profiles/` as YAML files:

```yaml
description: "Profile description"
default: false                      # Mark as default profile

parents:                            # Inherit from other profiles
  - base-profile
  - ctxloom-default/python-developer

tags:                              # Include fragments with these tags
  - golang
  - testing

bundles:                           # Bundle references
  - go-development                 # Local bundle
  - ctxloom-default/security             # Remote bundle
  - my-bundle#fragments/specific  # Specific fragment
  - my-bundle#prompts/review      # Specific prompt

variables:                         # Template variables (Mustache)
  DATABASE_URL: "postgresql://..."
  PROJECT_NAME: "my-app"
  DEBUG: "true"
```

## Content Reference Syntax

| Format | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all content) |
| `bundle#fragments/name` | Specific fragment |
| `bundle#prompts/name` | Specific prompt |
| `bundle#mcp` | All MCP servers from bundle |
| `bundle#mcp/name` | Specific MCP server |
| `remote/bundle` | Bundle from remote |
| `remote/bundle#fragments/x` | Fragment from remote |

### Extended Formats

| Format | Description |
|--------|-------------|
| `https://github.com/user/repo@v1/bundles/name` | Full URL with version |
| `git@github.com:user/repo#fragments/name` | Git SSH format |

## Using Profiles

```bash
# Run with a profile
ctxloom run -p developer "implement error handling"

# Preview profile context
ctxloom run -p developer --dry-run

# Use remote profile directly
ctxloom run -p ctxloom-default/python-developer "help with Python"

# Combine profile with extra fragments
ctxloom run -p developer -f security#fragments/owasp "audit code"
```

## Managing Profiles

```bash
ctxloom profile list                    # List all profiles
ctxloom profile show developer          # Show profile details
ctxloom profile create my-profile       # Create new profile
ctxloom profile edit developer          # Edit in configured editor
ctxloom profile delete old-profile      # Remove profile
ctxloom profile install ctxloom-default/dev    # Install from remote
```

### Create with Options

```bash
ctxloom profile create backend \
  --parent base \
  --parent ctxloom-default/security \
  -b go-development \
  -b testing \
  -d "Backend developer profile"
```

## Profile Inheritance

Profiles can inherit from other profiles using `parents`:

```yaml
# base.yaml
description: "Base configuration"
bundles:
  - core-standards
variables:
  LOG_LEVEL: "info"

# developer.yaml
description: "Developer profile"
parents:
  - base                    # Inherit from local
  - ctxloom-default/security      # Inherit from remote
bundles:
  - dev-tools              # Add more bundles
variables:
  LOG_LEVEL: "debug"       # Override parent value
  DEV_MODE: "true"         # Add new variable
```

### Inheritance Rules

- **Order matters**: Later parents override earlier ones
- **Child overrides all**: Child values override all parent values
- **Bundles merge**: No duplicates
- **Tags merge**: Combined from all parents
- **Variables merge**: Child overrides parent values
- **Exclusions accumulate**: Cannot un-exclude what a parent excluded
- **Circular detection**: ctxloom errors on circular references

## Excluding Content

Profiles can exclude fragments, prompts, or MCP servers inherited from parents:

```yaml
# developer.yaml
description: "Lightweight developer profile"
parents:
  - full-context            # Inherit everything
exclude_fragments:
  - verbose-logging         # But skip these fragments
  - deprecated-style
exclude_prompts:
  - review-nitpick          # Skip this prompt
exclude_mcp:
  - slow-server             # Don't include this MCP server
```

### Managing Exclusions

```bash
# Add an exclusion
ctxloom profile modify developer --exclude-fragment verbose-logging

# Remove an exclusion (stop excluding)
ctxloom profile modify developer --include-fragment verbose-logging

# View exclusions
ctxloom profile show developer
```

### Via MCP Tools

```json
{
  "tool": "update_profile",
  "arguments": {
    "name": "developer",
    "add_exclude_fragments": ["verbose-logging"],
    "remove_exclude_mcp": ["slow-server"]
  }
}
```

### Exclusion Inheritance

Exclusions accumulate through the inheritance chain - a child profile cannot "un-exclude" something excluded by a parent. This keeps the mental model simple: exclusions always win.

## Fragment Priority

Fragments can have priorities that control their position in assembled context. This addresses the "Lost in the Middle" problem where LLMs attend poorly to middle content.

```yaml
# In profile
fragments:
  - name: critical-rules
    priority: 10            # Highest priority -> placed at start
  - name: best-practices
    priority: 5             # Second highest -> placed at end
  - coding-standards        # No priority (defaults to 0) -> middle
```

### Bookend Strategy

ctxloom uses a "bookend" placement strategy based on LLM attention research:

| Position | Content | Why |
|----------|---------|-----|
| **Start** | Highest priority | Primacy effect - best attention |
| **End** | Second highest priority | Recency effect - good attention |
| **Middle** | Lower priorities | Weaker attention, less critical content |

### Setting Priorities

```bash
# Priorities are set in profile YAML
# Edit directly:
ctxloom profile edit developer
```

Or via the MCP tool when the profile uses inline fragment definitions.

## Default Profiles

Mark a profile as default to load automatically:

```yaml
# .ctxloom/profiles/developer.yaml
description: "Default dev profile"
default: true
bundles:
  - standards
```

Or in config.yaml:

```yaml
defaults:
  profiles:
    - developer
    - ctxloom-default/base
```

## Variables

Profile variables are used in Mustache templates:

```yaml
# Profile
variables:
  PROJECT_NAME: "my-app"
  LANGUAGE: "Go"
  TEAM: "backend"
```

```yaml
# Fragment content using variables
content: |
  # {{PROJECT_NAME}} Development

  This {{LANGUAGE}} project is maintained by {{TEAM}}.
```

See [Templating](/guides/templating) for full variable documentation.

## Inline Profiles

Profiles can be defined directly in config.yaml:

```yaml
# .ctxloom/config.yaml
profiles:
  quick-review:
    description: "Quick code review"
    bundles:
      - code-review
    variables:
      REVIEW_DEPTH: "surface"
```

Use like any other profile:

```bash
ctxloom run -p quick-review "review this PR"
```
