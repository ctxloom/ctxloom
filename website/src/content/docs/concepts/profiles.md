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
  - https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer

tags:                              # Include fragments with these tags
  - golang
  - testing

bundles:                           # Bundle references
  - go-development                 # Local bundle
  - ctxloom-default/security             # Remote bundle
  - my-bundle#fragments/specific  # Specific fragment
  - my-bundle#skills/review       # Specific skill

variables:                         # Template variables (Mustache)
  DATABASE_URL: "postgresql://..."
  PROJECT_NAME: "my-app"
  DEBUG: "true"
```

To make a profile the default context for a bare `ctxloom run`, bind it to the
**default agent** — set `default_agent` in `.ctxloom/config.yaml` to an agent
whose `profiles:` list includes it (see [The Default Agent](#the-default-agent)
below). The legacy `profiles.defaults` array and the per-file `default: true`
flag are no longer supported.

When `ctxloom run` is invoked with no `--agent` and no `-p`/`-f`/`-t`, it binds
the default agent. If no `default_agent` is configured (or it names an undefined
agent), ctxloom warns and continues with empty context — never blocking startup.

### Preferred LLM (`llm:`)

A profile can name the LLM it should launch with via `llm:` — a config label
(e.g. `claude-fast`, `agy-code`) or a backend type. `ctxloom run` uses it
unless `--llm`/`-l` overrides; a misconfigured value warns and falls back to the
primary role rather than blocking startup. Set it with `--llm` on
`profile create`/`profile modify`.

This field is what makes a profile a self-contained agent for `ctxloom map` /
`weave`: each parallel member runs on its own `llm:`, and the synthesizer on a
high-power one.

Profiles are also what [agents](/concepts/agents/) bind engines to: an agent is
a named, local-only engine↔profile binding (`ctxloom agent set`), consumed by
`run --agent` and `map`/`weave --agents`.

## Content Reference Syntax

| Format | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all content) |
| `bundle#fragments/name` | Specific fragment |
| `bundle#skills/name` | Specific skill |
| `bundle#profiles/name` | Profile shipped by the bundle |
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

# Use a bundle-shipped profile directly (canonical URL ref)
ctxloom run -p 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer' "help with Go"

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
ctxloom agent default dev               # Set/show the default agent (the default context)
ctxloom profile materialize developer --target ./out  # Write the assembled agent surface
```

`profile materialize` writes a profile's assembled surface (context, MCP
config, hooks, commands) into a target directory as a backend's native on-disk
layout, so an externally-launched agent inherits the profile without ctxloom in
the loop.

Remote profiles ship inside bundles. To consume one, author a local profile
that inherits from it, then pull:

```bash
ctxloom profile create my-dev --parent 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer'
ctxloom remote pull
```

### Create with Options

```bash
ctxloom profile create backend \
  --parent base \
  --parent 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer' \
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
  - https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer
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

Skills are deliberately not excludable. Exclusion exists for content that is
*pushed* on the session — fragments are ingested into the context window and
MCP servers run and consume resources, so an unwanted one has a real cost. A
skill is only a slash command: it does nothing until you invoke it, so an
unwanted skill just sits unused in the menu. Bundle authors can still scope
where a skill surfaces per backend with the skill's `llm.<backend>.enabled`
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

## The Default Agent

A bare `ctxloom run` (no `--agent`, no `-p`/`-f`/`-t`) binds the **default
agent**: the agent named by top-level `default_agent`, whose composed `profiles:`
become the context and whose engine + runtime + permissions drive the session.
This replaces the retired `profiles.defaults` — "the default profile set" is now
simply whatever the default agent composes.

`ctxloom init` scaffolds this for you: it writes a local `default` profile, binds
it to a `default` agent (carrying the engine you selected), and points
`default_agent` at it:

```yaml
default_agent: default
agents:
  default:
    engine: claude-code
    runtime: host
    profiles:
      - default
```

An agent's `profiles:` is a **list**, and each entry may be a local profile name
or a bundle-qualified profile ref. To repoint the default later, use
`ctxloom agent default` (or edit `default_agent`/`agents` directly):

```bash
ctxloom agent default            # show the current default agent
ctxloom agent default dev        # make the 'dev' agent the default
ctxloom agent set dev --profiles developer,base --engine claude-code
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

Profiles can be defined directly in config.yaml under `profiles.definitions`:

```yaml
# .ctxloom/config.yaml
profiles:
  definitions:
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
