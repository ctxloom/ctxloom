---
sidebar_position: 1
---

# Bundles

A **bundle** is a YAML file containing related fragments, prompts, and MCP server configurations.

## Bundle Format

Bundles are stored in `.scm/bundles/` as YAML files:

```yaml
version: "1.0.0"
description: "Python development standards"
tags:
  - python
  - development

fragments:
  coding-standards:
    tags: [python, style]
    content: |
      # Python Coding Standards
      - Use ruff for formatting and linting
      - Follow PEP 8 guidelines

  error-handling:
    tags: [python, errors]
    content: |
      # Error Handling
      - Use specific exception types
      - Add context when re-raising

prompts:
  code-review:
    description: "Review Python code for best practices"
    content: |
      Review this Python code for adherence to best practices...

mcp:
  tree-sitter:
    command: "tree-sitter-mcp"
    args: ["--stdio"]
```

## Managing Bundles

```bash
scm bundle list                     # List all bundles
scm bundle show <name>              # Show bundle contents
scm bundle view <name[#path]>       # View bundle or item content
scm bundle create <name>            # Create a new bundle
scm bundle edit <name>              # Edit bundle metadata
scm bundle export <name> <dir>      # Export bundle to directory
scm bundle import <path>            # Import bundle from file
```

## Editing Bundle Content

```bash
# Edit fragment content
scm bundle fragment edit my-bundle coding-standards

# Edit prompt content
scm bundle prompt edit my-bundle review

# Edit MCP config
scm bundle mcp edit my-bundle tree-sitter

# Add/remove tags
scm bundle edit my-bundle --add-tag python
```

## Distillation

Bundles can be **distilled** to create compressed versions optimized for token usage:

```bash
scm bundle distill .scm/bundles/*.yaml
```

Distilled fields are added automatically:

```yaml
fragments:
  my-fragment:
    content: "Original detailed content..."
    content_hash: "sha256:..."
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

Reference bundle content using this syntax:

| Syntax | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all fragments) |
| `bundle#fragments/name` | Specific fragment from bundle |
| `bundle#prompts/name` | Specific prompt from bundle |
| `remote/bundle#fragments/name` | Fragment from remote bundle |
