---
title: "Profiles"
---

Switching from backend work to a security audit shouldn't mean re-typing `-f security#fragments/owasp -f security#fragments/threat-model -t compliance` and hoping you remembered every fragment. Do that by hand a few times and you'll start skipping fragments, or reusing "close enough" context that doesn't match the task.

A **profile** bundles that whole selection (bundles, tags, variables) under one name, so `ctxloom run -p security-audit` swaps your entire context in one flag instead of reassembling it by hand every time.

## Profile Structure

Profiles are stored in `.ctxloom/profiles/` as YAML files:

```yaml
description: "Profile description"

llm: claude-fast                    # Preferred LLM (config label or backend)

parents:                            # Inherit from other profiles
  - base-profile
  - https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer

tags:                              # Descriptive only (listing/discovery) — does NOT select content
  - golang
  - testing

select_tags:                       # Include fragments with these tags
  - security

bundles:                           # Bundle references
  - go-development                 # Local bundle
  - ctxloom-default/security             # Remote bundle
  - my-bundle#fragments/specific  # Specific fragment

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
agent), ctxloom aborts before launch with a fatal finding telling you to run
`ctxloom agent default <name>`. Pass `--degraded` to get the old
warn-and-continue behavior: it launches anyway with empty context instead of
blocking.

### Preferred LLM (`llm:`)

A profile can name the LLM it should launch with via `llm:` — a config label
(e.g. `claude-fast`, `agy-code`) or a backend type. `ctxloom run` uses it
unless `--llm`/`-l` overrides; a misconfigured value warns and falls back to the
primary role rather than blocking startup. Set it with `--llm` on
`profile create`/`profile modify`.

This field is what makes a profile a self-contained agent for a delegated
fan-out: each child spawned via `agent_run` runs on its own `llm:`.

Profiles are also what [agents](/concepts/agents/) bind engines to: an agent is
a named, local-only engine↔profile binding (`ctxloom agent set`), consumed by
`run --agent` and `agent_run`.

## Content Reference Syntax

These `bundle#kind/name` forms address one item and work everywhere ctxloom
takes an item reference on the command line — `ctxloom command show`,
`ctxloom fragment edit`, `ctxloom trust`, and so on:

| Format | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all content) |
| `bundle#fragments/name` | Specific fragment |
| `bundle#commands/name` | Specific command |
| `bundle#profiles/name` | Profile shipped by the bundle |
| `bundle#mcp` | All MCP servers from bundle |
| `bundle#mcp/name` | Specific MCP server |
| `remote/bundle` | Bundle from remote |
| `remote/bundle#fragments/x` | Fragment from remote |

### Inside a profile's `bundles:` list

A profile's `bundles:` list is narrower: only a whole-bundle ref or a
`#fragments/name` cherry-pick belong there.

| Format | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all content) |
| `remote/bundle` | Entire bundle from a remote |
| `bundle#fragments/name` | One fragment, cherry-picked |
| `remote/bundle#fragments/name` | One fragment from a remote bundle |

`bundle#commands/name` and `bundle#mcp/name` are valid for CLI addressing but
**not** here: a profile's bundle-expansion logic only recognizes the
`fragments/` selector, so anything else either silently pulls in the entire
bundle (every command and every MCP server, not just the one named) or silently
loads nothing, depending on whether the ref is local or a canonical URL — with
no error either way. To curate which commands a profile exports, use the
profile's `commands:` list instead (see the [Commands](/concepts/commands/) doc), not a `#commands/`
ref inside `bundles:`.

### Extended Formats

| Format | Description |
|--------|-------------|
| `https://github.com/user/repo@bundles/name@v1.2.3` | Full URL with pinned content version |
| `git@github.com:user/repo@bundles/name#fragments/name` | Git SSH format |

The SSH form always needs the `@bundles/name` item path — `#fragments/name` is
an addition to it, not a replacement. `git@github.com:user/repo#fragments/name`
(with no `@bundles/...`) fails to parse.

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
ctxloom profile remove old-profile --yes # Remove profile
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

Refs passed to `--parent`/`-b` follow one rule: a bare, unprefixed name (e.g.
`--parent developer` or `-b code-review-base#fragments/conduct`) is always a
**local** profile/bundle name — it is never expanded against a remote. Only an
alias-prefixed ref (`<remote-alias>/<bundle>[#selector]`, e.g.
`-b ctxloom-default/security`) expands into its canonical URL, using the
configured remote's alias. Full URLs and `ctxloom:local@...` refs pass through
unchanged.

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

Commands are deliberately not excludable. Exclusion exists for content that is
*pushed* on the session — fragments are ingested into the context window and
MCP servers run and consume resources, so an unwanted one has a real cost. A
command is only a slash command: it does nothing until you invoke it, so an
unwanted command just sits unused in the menu. Bundle authors can still scope
where a command surfaces per backend with the command's `llm.<backend>.enabled`
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
ctxloom agent create dev --profiles developer,base --engine claude-code
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
