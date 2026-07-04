---
title: "Authoring Bundles"
---

Create and manage your own context bundles.

## Create Your First Bundle

```bash
# Create a new bundle
ctxloom bundle create my-standards

# With a description
ctxloom bundle create my-standards -d "My coding standards"
```

This creates `.ctxloom/cache/bundles/my-standards.yaml` with an example
fragment and skill:

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
skills:
    example:
        description: Example prompt
        tags:
            - example
        content: Example prompt content. Describe what this prompt does.
        no_distill: true
```

## Edit Your Bundle

```bash
# Open in your editor
ctxloom bundle edit my-standards
```

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

skills:
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

A bundle is a YAML file containing fragments (context), skills (exported as slash commands), MCP servers, profiles, and hooks.

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

skills:
  skill-name:
    description: "For /help output"
    tags: [tags]
    content: |
      # Skill Template
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

### Skill Fields

| Field | Description |
|-------|-------------|
| `content` | **Required.** The prompt template |
| `description` | Human-readable description (shown in /help) |
| `tags` | Tags for filtering |
| `no_distill` | If true, skip compression |
| `llm` | Per-backend export settings for the exported slash command, keyed by backend (`claude-code`, `antigravity`, `codex`, `kiro`): `enabled`, `description`, and per-backend extras like `argument_hint`, `allowed_tools`, `model` |

### MCP Server Fields

| Field | Description |
|-------|-------------|
| `command` | **Required.** Command to execute |
| `args` | Command arguments |
| `env` | Environment variables |

## Managing Bundle Content

### Add a Fragment

```bash
ctxloom fragment create my-standards testing
```

Then edit to add content:
```bash
ctxloom bundle edit my-standards
```

### Add a Skill

```bash
ctxloom skill create my-standards code-review
```

### Delete Content

```bash
ctxloom fragment delete my-standards#fragments/old-fragment
ctxloom skill delete my-standards#skills/old-prompt
```

## Test Your Bundle

```bash
# List fragments in your bundle
ctxloom fragment list --bundle my-standards

# View a specific fragment
ctxloom fragment show my-standards#fragments/coding-style

# Preview how it would be assembled
ctxloom run -f my-standards --dry-run --print

# Run with your bundle
ctxloom run -f my-standards "Help me with this code"
```

## Repository Structure for Sharing

To share bundles via GitHub/GitLab, create a repository with this structure:

```
my-ctxloom-repo/
├── ctxloom/
│   └── bundles/
│       ├── go-development.yaml
│       └── testing-patterns.yaml
└── README.md
```

The `ctxloom/` directory is **required** for ctxloom to recognize the repository. Profiles ship inside bundles (a bundle's `profiles:` key) and are addressed `<bundle>#profiles/<name>` — remote repos have no top-level profiles directory.

### Naming for Discovery

Name your repository `ctxloom` or `ctxloom-*` to be discoverable:

- `ctxloom` - General content
- `ctxloom-golang` - Go-specific bundles
- `ctxloom-security` - Security-focused content

### Publish to GitHub

```bash
# Create repo structure
mkdir -p ctxloom/bundles

# Copy your bundles
cp .ctxloom/cache/bundles/my-standards.yaml ctxloom/bundles/

# Push to GitHub
git init
git add .
git commit -m "Initial ctxloom bundles"
git remote add origin https://github.com/you/ctxloom-standards.git
git push -u origin main
```

Others can then use your bundles:

```bash
ctxloom remote add standards you/ctxloom-standards
ctxloom run -f standards/my-standards "help me"
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
