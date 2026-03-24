---
sidebar_position: 2
---

# Fragments

A **fragment** is a reusable context snippet within a bundle. Fragments contain the actual content that gets sent to AI assistants.

## Fragment Structure

Fragments are defined within bundles:

```yaml
fragments:
  coding-standards:
    tags: [python, style]
    content: |
      # Python Coding Standards
      - Use ruff for formatting and linting
      - Follow PEP 8 guidelines
      - Use type hints for function signatures

  error-handling:
    tags: [python, errors]
    content: |
      # Error Handling Patterns
      - Use specific exception types
      - Add context when re-raising exceptions
      - Log errors with appropriate levels
```

## Using Fragments

### Include Specific Fragments

```bash
# Include a specific fragment
scm run -f python-tools#fragments/coding-standards "review this code"

# Combine multiple fragments
scm run -f security#fragments/owasp -f python#fragments/errors "audit this code"
```

### Include by Tag

```bash
# Include all fragments with a tag
scm run -t security "check for vulnerabilities"

# Combine tags
scm run -t python -t security "review Python security"
```

## Editing Fragments

```bash
# Edit fragment content in your editor
scm bundle fragment edit my-bundle coding-standards
```

This opens the fragment content in your `$VISUAL` or `$EDITOR`.

## Tags

Tags help organize and filter fragments:

- Bundle-level tags apply to all fragments in the bundle
- Fragment-level tags are specific to that fragment
- Use `-t <tag>` to include all fragments with a matching tag

```yaml
# Bundle-level tags
tags:
  - python

fragments:
  # Fragment-level tags
  typing:
    tags: [typing, mypy]
    content: ...
```

## Templating

Fragments support [Mustache](https://mustache.github.io/) templating. See the [Templating Guide](/guides/templating) for details.
