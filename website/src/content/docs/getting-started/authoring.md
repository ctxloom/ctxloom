---
title: "Authoring Bundles"
---

# Authoring Bundles

Create and manage your own context bundles.

## Create Your First Bundle

```bash
# Create a new bundle
ctxloom bundle create my-standards

# With a description
ctxloom bundle create my-standards -d "My coding standards"
```

This creates `.ctxloom/bundles/my-standards.yaml`:

```yaml
version: "1.0.0"
description: "My coding standards"
tags: []

fragments: {}

prompts: {}
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

prompts:
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

A bundle is a YAML file containing fragments (context), prompts (slash commands), and MCP servers.

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

prompts:
  prompt-name:
    description: "For /help output"
    tags: [tags]
    content: |
      # Prompt Template
      Your prompt here...

mcp:
  server-name:
    command: "npx my-mcp-server"
    args: ["--flag", "value"]
    env:
      API_KEY: "${API_KEY}"
```

### Fragment Fields

| Field | Description |
|-------|-------------|
| `content` | **Required.** The actual content sent to AI |
| `tags` | Additional tags (merged with bundle tags) |
| `variables` | Mustache variables used in content |
| `notes` | Human notes (NOT sent to AI) |
| `installation` | Setup instructions (NOT sent to AI) |
| `no_distill` | If true, skip compression |

### Prompt Fields

| Field | Description |
|-------|-------------|
| `content` | **Required.** The prompt template |
| `description` | Human-readable description (shown in /help) |
| `tags` | Tags for filtering |
| `variables` | Mustache variables used |

### MCP Server Fields

| Field | Description |
|-------|-------------|
| `command` | **Required.** Command to execute |
| `args` | Command arguments |
| `env` | Environment variables |

## Managing Bundle Content

### Add a Fragment

```bash
ctxloom bundle fragment add my-standards testing
```

Then edit to add content:
```bash
ctxloom bundle edit my-standards
```

### Add a Prompt

```bash
ctxloom bundle prompt add my-standards code-review
```

### Delete Content

```bash
ctxloom bundle fragment delete my-standards#fragments/old-fragment
ctxloom bundle prompt delete my-standards#prompts/old-prompt
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
│   └── v1/
│       ├── bundles/
│       │   ├── go-development.yaml
│       │   └── testing-patterns.yaml
│       └── profiles/
│           └── go-developer.yaml
└── README.md
```

The `ctxloom/v1/` directory is **required** for ctxloom to recognize the repository.

### Naming for Discovery

Name your repository `ctxloom` or `ctxloom-*` to be discoverable:

- `ctxloom` - General content
- `ctxloom-golang` - Go-specific bundles
- `ctxloom-security` - Security-focused content

### Publish to GitHub

```bash
# Create repo structure
mkdir -p ctxloom/v1/bundles ctxloom/v1/profiles

# Copy your bundles
cp .ctxloom/bundles/my-standards.yaml ctxloom/v1/bundles/

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
