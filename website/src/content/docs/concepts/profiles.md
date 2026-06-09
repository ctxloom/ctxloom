---
title: "Profiles"
---

A **profile** is a named configuration that references bundles, tags, and variables. Profiles enable quick context switching.

## Profile Structure

Profiles are stored in `.ctxloom/profiles/` as YAML files:

```yaml
description: "Profile description"

llm: claude-fast                    # Preferred LLM (config label or backend)

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

To mark a profile as the default for `ctxloom run`, list it under
`defaults.profiles` in `.ctxloom/config.yaml` (see [Default Profiles](#default-profiles) below). The legacy per-file `default: true` flag is no longer supported.

When `ctxloom run` is invoked with no profile and no configured default, it shows
an interactive picker of installed profiles (skipped when not on a terminal).

### Preferred LLM (`llm:`)

A profile can name the LLM it should launch with via `llm:` — a config label
(e.g. `claude-fast`, `gemini-code`) or a backend type. `ctxloom run` uses it
unless `--llm`/`-l` overrides; a misconfigured value warns and falls back to the
primary role rather than blocking startup. Set it with `--llm` on
`profile create`/`profile modify`.

This field is what makes a profile a self-contained agent for `ctxloom map` /
`weave`: each parallel member runs on its own `llm:`, and the synthesizer on a
high-power one.

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
| `https://github.com/user/repo@bundles/name@v1.2.3` | Full URL with pinned content version |
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
ctxloom profile default developer       # Set/show the default profile(s)
```

To consume a remote profile, author a local profile that inherits from it, then
pull:

```bash
ctxloom profile create my-dev --parent ctxloom-default/dev
ctxloom remote pull
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

Refs passed to `--parent`/`-b` may be bare convenience refs (e.g.
`--parent developer` or `-b code-review-base#fragments/conduct`), which expand
against the configured default remote into canonical URLs. Full URLs and
`ctxloom:local@...` refs pass through unchanged.

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

Profiles can exclude fragments or MCP servers inherited from parents:

```yaml
# developer.yaml
description: "Lightweight developer profile"
parents:
  - full-context            # Inherit everything
exclude_fragments:
  - verbose-logging         # But skip these fragments
  - deprecated-style
exclude_mcp:
  - slow-server             # Don't include this MCP server
```

Prompts are deliberately not excludable. Exclusion exists for content that is
*pushed* on the session — fragments are ingested into the context window and
MCP servers run and consume resources, so an unwanted one has a real cost. A
prompt is only a slash command: it does nothing until you invoke it, so an
unwanted prompt just sits unused in the menu. Bundle authors can still scope
where a prompt surfaces per backend with the prompt's `llm.<backend>.enabled`
flag.

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

List profiles to load automatically in `.ctxloom/config.yaml`:

```yaml
defaults:
  profiles:
    - developer
    - ctxloom-default/base
```

The default is a **list**, and each entry may be a local profile name or a
remote ref. ctxloom auto-promotes a profile here when:

- you create the first profile (via `create_profile` or interactive setup), or
- you reference a remote profile and no default is configured yet, or
- exactly one profile is installed locally (single-profile fallback at run time).

If you want a different default later, edit `defaults.profiles` directly or use
the `profile default` command:

```bash
ctxloom profile default developer         # set the default profile
ctxloom profile default                   # show the current default(s)
ctxloom profile default --unset developer # clear an entry
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
