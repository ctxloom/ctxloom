---
title: "Bundles"
---

A **bundle** is a versioned YAML file containing related fragments, prompts, and MCP server configurations.

## Bundle Structure

Bundles are stored in `.ctxloom/bundles/` as YAML files:

```yaml
version: "1.0.0"                    # Bundle version (required)
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
    installation: "Setup guide"     # Returned to AI on install
    content: |
      # Fragment Content
      Your markdown content here...

    # Distillation fields (auto-generated)
    content_hash: "sha256:..."      # Hash of original content
    distilled: |                    # Token-efficient version
      # Compressed content
    distilled_by: "claude-code"     # Model that created it
    no_distill: false               # Disable distillation

prompts:
  prompt-name:
    description: "Prompt description"
    tags: [tool, generation]
    notes: "Human notes"            # NOT sent to AI
    installation: "Setup"           # Returned to AI on install
    content: |
      # Prompt Content
      Your prompt template here...

    # Plugin-specific settings
    plugins:
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
    installation: "Install guide"   # Returned to AI on install
```

## Key Fields

### Fragment Fields

| Field | Type | Description |
|-------|------|-------------|
| `content` | string | Required. The actual content sent to AI |
| `tags` | array | Additional tags (merged with bundle tags) |
| `notes` | string | Human-readable notes (NOT sent to AI) |
| `installation` | string | Setup instructions (returned to AI on install) |
| `no_distill` | bool | If true, skip distillation |
| `content_hash` | string | SHA256 hash of content |
| `distilled` | string | Token-efficient version |
| `distilled_by` | string | Model that created distillation |

### Prompt Fields

| Field | Type | Description |
|-------|------|-------------|
| `content` | string | Required. The prompt template |
| `description` | string | Human-readable description |
| `tags` | array | Tags for filtering |
| `plugins` | object | Plugin-specific settings |

### MCP Server Fields

| Field | Type | Description |
|-------|------|-------------|
| `command` | string | Required. Command to execute |
| `args` | array | Command arguments |
| `env` | map | Environment variables |
| `notes` | string | Human-readable notes (NOT sent to AI) |
| `installation` | string | Setup instructions (returned to AI on install) |

## Managing Bundles

```bash
ctxloom fragment list                   # List all fragments
ctxloom fragment list --bundle my-bundle
ctxloom fragment show my-bundle#fragments/name
ctxloom fragment create my-bundle name  # Create fragment
ctxloom fragment edit my-bundle#fragments/name
ctxloom fragment delete my-bundle#fragments/name
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

Reference bundle content using hash syntax:

| Syntax | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all fragments, prompts, MCP) |
| `bundle#fragments/name` | Specific fragment |
| `bundle#prompts/name` | Specific prompt |
| `bundle#mcp` | All MCP servers |
| `bundle#mcp/name` | Specific MCP server |
| `remote/bundle` | Bundle from remote |
| `remote/bundle#fragments/x` | Fragment from remote bundle |

## Notes vs Installation

These fields serve different purposes:

- `notes` - Internal documentation for humans only. Never sent to the AI.
- `installation` - Setup instructions. **Returned to the AI when a bundle is installed** via the MCP `pull_remote` tool, so the AI can follow setup steps automatically.

This allows bundle authors to include setup commands (npm install, environment variables, etc.) that the AI will execute after installation.
