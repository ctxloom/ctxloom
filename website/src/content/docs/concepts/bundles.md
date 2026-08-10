---
title: "Bundles"
---

Your coding standards live in one project's `CLAUDE.md` and your MCP server config lives in another repo's `.claude/settings.json`. Each project reinvents its own copy, and they drift the moment one gets updated and the others don't.

A **bundle** collects that content (fragments, commands, MCP server configs, profiles, hooks) into one versioned YAML file you can commit, share through a [remote](/concepts/remotes/), and pull into any project. Update the bundle once and every project that references it can pick up the change with `ctxloom deps pull`.

## Bundle Structure

Local bundles are stored in `.ctxloom/content/bundles/` as YAML files. That
directory is committed — it's the content your project publishes, and it's
what `ctxloom bundle sign --all` signs — unlike `.ctxloom/cache/`, which is
gitignored and holds only regenerable, remote-pulled artifacts.

```yaml
version: "1.0.0"                    # Bundle version (not enforced by the parser; `ctxloom bundle create` defaults it to 1.0.0)
tags: [golang, development]         # Bundle-level tags
author: "ctxloom"                       # Author name
description: "Bundle description"   # Description

# Human-readable notes (NOT sent to AI)
notes: |
  Internal notes about this bundle...

# Setup instructions (returned to AI on install)
installation: |
  Run: npm install ...

fragments:
  fragment-name:
    tags: [language, patterns]      # Additional tags (merged with bundle)
    notes: "Human notes"            # NOT sent to AI
    installation: "Setup guide"     # Shown in `bundle show`/`bundle list`, not surfaced on install
    content: |
      # Fragment Content
      Your markdown content here...

    # Distillation fields (auto-generated)
    content_hash: "sha256:..."      # Hash of original content
    distilled: |                    # Token-efficient version
      # Compressed content
    distilled_by: "claude-code"     # Model that created it
    no_distill: false               # Disable distillation

commands:
  command-name:
    description: "Command description"
    tags: [tool, generation]
    notes: "Human notes"            # NOT sent to AI
    installation: "Setup"           # Shown in `bundle show`/`bundle list`, not surfaced on install
    content: |
      # Command Content
      Your command template here...

    # Per-LLM export settings
    llm:
      claude-code:
        enabled: true             # null = enabled (opt-out)
        description: "For /help"
        argument_hint: "usage"
        allowed_tools: [Read, Write]
        model: "claude-opus-4-5"

mcp:
  server-name:
    command: "npx my-mcp-server"    # Command to execute
    args: ["--flag", "value"]       # Arguments
    env:                            # Environment variables
      API_KEY: "${API_KEY}"
    notes: "Human notes"            # NOT sent to AI
    installation: "Install guide"   # Shown in `bundle show`/`bundle list`, not surfaced on install

profiles:
  profile-name:                     # Shipped profile, addressed as
    description: "..."              # <bundle>#profiles/profile-name
    bundles: [...]
    tags: [...]

hooks:
  session_start:                    # Keyed by lifecycle event
    - command: "..."
```

## Key Fields

### Fragment Fields

| Field | Type | Description |
|-------|------|-------------|
| `content` | string | Required. The actual content sent to AI |
| `tags` | array | Additional tags (merged with bundle tags) |
| `notes` | string | Human-readable notes (NOT sent to AI) |
| `installation` | string | Setup instructions, shown in `bundle show`/`bundle list` (not surfaced on install — only the bundle-level `installation` is) |
| `no_distill` | bool | If true, skip distillation |
| `content_hash` | string | SHA256 hash of content |
| `distilled` | string | Token-efficient version |
| `distilled_by` | string | Model that created distillation |

### Command Fields

| Field | Type | Description |
|-------|------|-------------|
| `content` | string | Required. The command content |
| `description` | string | Human-readable description |
| `tags` | array | Tags for filtering |
| `llm` | object | Per-LLM export settings (slash-command surface per backend) |

### MCP Server Fields

| Field | Type | Description |
|-------|------|-------------|
| `command` | string | Required. Command to execute |
| `args` | array | Command arguments |
| `env` | map | Environment variables |
| `notes` | string | Human-readable notes (NOT sent to AI) |
| `installation` | string | Setup instructions, shown in `bundle show`/`bundle list` (not surfaced on install — only the bundle-level `installation` is) |

## Managing Bundles

```bash
ctxloom fragment list                   # List all fragments
ctxloom fragment list --bundle my-bundle
ctxloom fragment show my-bundle#fragments/name
ctxloom fragment create my-bundle name  # Create fragment
ctxloom fragment edit my-bundle#fragments/name
ctxloom fragment remove my-bundle#fragments/name --yes
```

## Distillation

Distillation creates compressed versions optimized for token usage:

```bash
ctxloom fragment distill my-bundle#fragments/name
ctxloom fragment distill my-bundle#fragments/name --force  # Re-distill
```

Distilled fields are added automatically:

```yaml
fragments:
  my-fragment:
    content: "Original detailed content..."
    content_hash: "sha256:abc123..."
    distilled: "Compressed version..."
    distilled_by: "claude-opus-4-5-20251101"
```

### Skip Distillation

For fragments that must be preserved exactly:

```yaml
fragments:
  critical-rules:
    no_distill: true
    content: "Must be sent verbatim..."
```

## Content References

Reference bundle content using hash syntax. These forms work on the command
line — `ctxloom fragment show`, `ctxloom command show`, `ctxloom trust`, and
similar item-addressing commands:

| Syntax | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all fragments, commands, MCP) |
| `bundle#fragments/name` | Specific fragment |
| `bundle#commands/name` | Specific command |
| `bundle#profiles/name` | Profile shipped by the bundle |
| `bundle#mcp` | All MCP servers |
| `bundle#mcp/name` | Specific MCP server |
| `remote/bundle` | Bundle from remote |
| `remote/bundle#fragments/x` | Fragment from remote bundle |

They are **not** all valid inside a profile's `bundles:` list. A profile only
recognizes a whole-bundle ref or a `#fragments/name` cherry-pick there — see
[Content Reference Syntax](/concepts/profiles/#content-reference-syntax) in
the profiles doc for why `#commands/name` and `#mcp/name` silently do the wrong
thing in that position, and use a profile's `commands:` list to curate commands
instead.

## Notes vs Installation

These fields serve different purposes:

- `notes` - Internal documentation for humans only. Never sent to the AI.
- `installation` - Setup instructions, surfaced when the bundle is installed via `ctxloom deps pull` so they can be followed as setup steps.

This allows bundle authors to include setup commands (npm install, environment variables, etc.) that the user or their agent can run after installation.
