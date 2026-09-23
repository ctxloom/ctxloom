---
title: "Authoring Bundles"
---

Community bundles cover the common cases — a language's idioms, a framework's patterns. They don't know your team's own rules: the error-handling convention you settled on after last quarter's incident, or the review checklist specific to your codebase. A bundle is how you write those down once and get them injected into every session instead of repeating them in chat.

This guide creates one from scratch, edits it, and publishes it so your team (or the wider community) can pull it too.

## Create Your First Bundle

```bash
# Create a new bundle
ctxloom bundle create my-standards

# With a description
ctxloom bundle create my-standards -d "My coding standards"
```

This creates `.ctxloom/content/bundles/v2/my-standards.yaml` with an example
fragment and command. That directory is committed — it's your project's own
authored content, the tree `ctxloom bundle sign --all` signs, and what a publishing
repo ships:

```yaml
version: 1.0.0
description: My coding standards
fragments:
    example:
        tags:
            - example
        content: |-
            # Example Fragment

            Add your content here.
        no_distill: true
commands:
    example:
        tags:
            - example
        content: Example prompt content. Describe what this prompt does.
        no_distill: true
        description: Example prompt
```

Pass `--tree` to author it as a directory instead (`bundle.yaml` plus one file per
item). Only a tree can be published, so use `--tree` for a bundle you mean to share.

## Edit Your Bundle

`ctxloom bundle edit` doesn't open an editor — it's a flag-driven command for
adding or removing items (`--add-fragment`, `--remove-prompt`, `--add-tag`,
`--add-mcp`, and so on). Run it with no flags and it just prints "No changes
made" and the help text.

To fill in real content, open `.ctxloom/content/bundles/v2/my-standards.yaml`
directly in your own editor. (For touching a single existing item in place,
`ctxloom fragment edit my-standards#fragments/<name>` and
`ctxloom command edit my-standards#commands/<name>` do use `$EDITOR`.)

Add content to make it useful:

```yaml
version: "1.0.0"
description: "My coding standards"
tags:
  - development
  - standards

fragments:
  coding-style:
    tags: [style]
    content: |
      # Coding Standards
      - Use meaningful variable names
      - Keep functions under 50 lines
      - Write tests for all new code

  error-handling:
    tags: [errors]
    content: |
      # Error Handling
      - Always check errors immediately
      - Wrap errors with context
      - Use sentinel errors sparingly

commands:
  code-review:
    description: "Review code for issues"
    tags: [review]
    content: |
      Review this code for:
      - Adherence to coding standards
      - Error handling completeness
      - Test coverage
```

## Bundle Structure

A bundle is a YAML file containing fragments (context), commands (exported as slash commands), MCP servers, profiles, and hooks.

```yaml
version: "1.0.0"                    # Required: semantic version
description: "Bundle description"   # Optional: what this bundle provides
author: "your-name"                 # Optional: author name
tags: [tag1, tag2]                  # Optional: tags for all items

# Human-readable notes (NOT sent to AI)
notes: |
  Internal documentation...

# Setup instructions (NOT sent to AI)
installation: |
  Prerequisites: ...

fragments:
  fragment-name:
    tags: [extra-tags]              # Merged with bundle tags
    content: |
      # Context Content
      Your markdown content here...

commands:
  command-name:
    description: "For /help output"
    tags: [tags]
    content: |
      # Command Template
      Your prompt here...

mcp:
  server-name:
    command: "npx my-mcp-server"
    args: ["--flag", "value"]
    env:
      API_KEY: "${API_KEY}"

profiles:                           # Profiles shipped with the bundle,
  profile-name:                     # addressed <bundle>#profiles/<name>
    bundles: [my-bundle#fragments/fragment-name]

hooks:                              # Agent lifecycle hooks
  pre_tool:
    - matcher: "Bash"
      command: "./scripts/check.sh"
```

### Fragment Fields

| Field | Description |
|-------|-------------|
| `content` | **Required.** The actual content sent to AI |
| `tags` | Additional tags (merged with bundle tags) |
| `notes` | Human notes (NOT sent to AI) |
| `installation` | Setup instructions (NOT sent to AI) |
| `no_distill` | If true, skip compression |

### Command Fields

| Field | Description |
|-------|-------------|
| `content` | **Required.** The prompt template |
| `description` | Human-readable description (shown in /help) |
| `tags` | Tags for filtering |
| `no_distill` | If true, skip compression |
| `llm` | Per-backend export settings for the exported slash command, keyed by backend (e.g. `claude-code`): `enabled`, `description`, and per-backend extras like `argument_hint`, `allowed_tools`, `model` |

### MCP Server Fields

| Field | Description |
|-------|-------------|
| `command` | Command to execute (a local server). Set this or `url` |
| `args` | Command arguments |
| `env` | Environment variables |
| `url` | Endpoint of a network-hosted server; its scheme is the transport |
| `headers` | HTTP headers sent when dialing `url` |

## Managing Bundle Content

### Add a Fragment

```bash
ctxloom fragment create my-standards testing
```

Then edit its content (opens `$EDITOR`):
```bash
ctxloom fragment edit my-standards#fragments/testing
```

### Add a Command

```bash
ctxloom command create my-standards code-review
```

### Remove Content

```bash
ctxloom fragment remove my-standards#fragments/old-fragment --yes
ctxloom command remove my-standards#commands/old-prompt --yes
```

## Test Your Bundle

```bash
# List fragments in your bundle
ctxloom fragment list --bundle my-standards

# View a specific fragment
ctxloom fragment show my-standards#fragments/coding-style

# Preview how it would be assembled
ctxloom run -f coding-style --dry-run

# Run with a fragment from your bundle
ctxloom run -f coding-style "Help me with this code"
```

`-f/--fragment` takes a fragment name (or `bundle#fragments/name` if the name
isn't unique across your installed bundles), not a bundle name — a bundle
name won't match anything and `run` will error. To pull in every fragment a
bundle provides at once, reference the bundle from a profile's `bundles:`
list and run with `-p <profile>` instead (see [Profiles](/concepts/profiles)).

## Sharing a Bundle

A publishing repository lays its bundles out the same way a project does, under
`.ctxloom/content/bundles/v2/`, so you don't arrange files by hand. Create the
repository, register it as a remote, and push the bundle to it:

```bash
ctxloom remote create standards you/ctxloom-standards
ctxloom bundle push my-standards standards
```

`bundle push` refuses a single-file bundle, because bundles are distributed as
trees; create a shareable one with `ctxloom bundle create my-standards --tree`.
It commits straight to the default branch; `--pr` opens a pull
request instead. `--sign` signs the bundle first, so consumers who trust your
key skip review. See [Sharing](/guides/sharing) for the full publishing guide.

### Naming for Discovery

Name your repository `ctxloom` or `ctxloom-*` so `ctxloom remote discover` can find it:

- `ctxloom` - General content
- `ctxloom-golang` - Go-specific bundles
- `ctxloom-security` - Security-focused content

### Using a Shared Bundle

Registering a remote installs nothing. A consumer references the bundle from a
profile, pulls it, and accepts it:

```bash
ctxloom remote create standards you/ctxloom-standards
ctxloom profile create standards -b standards/my-standards
ctxloom deps pull
ctxloom review
ctxloom run -p standards "help me"
```

## Distillation

Compress verbose content for better token efficiency:

```bash
# Distill a specific fragment
ctxloom fragment distill my-standards#fragments/coding-style

# Re-distill (force)
ctxloom fragment distill my-standards#fragments/coding-style --force
```

Distilled content is stored alongside the original:

```yaml
fragments:
  coding-style:
    content: "Original detailed content..."
    content_hash: "sha256:abc123..."
    distilled: "Compressed version..."
    distilled_by: "claude-opus-4-5-20251101"
```

### Skip Distillation

For content that must be preserved exactly:

```yaml
fragments:
  critical-rules:
    no_distill: true
    content: "Must be sent verbatim..."
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
3. **Keep fragments focused** - One concept per fragment

### What Not to Include

- `notes` and `installation` fields are for humans, not sent to AI
- Use these for prerequisites, setup instructions, and internal documentation

## Next Steps

- [Session Memory](/getting-started/memory) - Preserve context across sessions
- [Profiles](/concepts/profiles) - Combine bundles into profiles
- [Sharing](/guides/sharing) - Full guide to publishing bundles
- [Distillation](/guides/distillation) - Token optimization details
