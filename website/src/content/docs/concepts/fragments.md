---
title: "Fragments"
---

You've typed "use table-driven tests, wrap errors with context, log at the right level" into enough prompts that you could recite it. Every new session, you type it again, or the AI guesses and gets it wrong.

A **fragment** is where that content lives once: a reusable snippet of context (coding standards, error-handling rules, a review checklist) stored in a bundle. Reference it by name with `-f` or a tag with `-t`, or pull it in through a [profile](/concepts/profiles/), and it reaches the AI without you retyping a word.

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
ctxloom run -f python-tools#fragments/coding-standards "review this code"

# Combine multiple fragments
ctxloom run -f security#fragments/owasp -f python#fragments/errors "audit this code"
```

### Include by Tag

```bash
# Include all fragments with a tag
ctxloom run -t security "check for vulnerabilities"

# Combine tags
ctxloom run -t python -t security "review Python security"
```

## Editing Fragments

```bash
# Edit fragment content in your editor
ctxloom fragment edit my-bundle#fragments/coding-standards
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

Fragments support [Mustache](https://mustache.github.io/) templating for dynamic content.

### Basic Variables

```yaml
fragments:
  project-context:
    content: |
      # {{ PROJECT_NAME }} Development

      This {{ LANGUAGE }} project follows these standards:
      - Use standard formatting tools
      - Write tests for all new code
```

Variables are defined in profiles:

```yaml
# .ctxloom/profiles/my-project.yaml
variables:
  PROJECT_NAME: "my-api"
  LANGUAGE: "Go"
bundles:
  - my-standards
```

### Conditional Sections

Mustache supports conditional sections:

- `#VAR` - renders section if VAR is truthy
- `^VAR` - renders section if VAR is falsy
- `/VAR` - closes a section

```text
## Deployment

{{#USE_DOCKER}}
### Docker Build
docker build -t app .
docker push registry/app
{{/USE_DOCKER}}

{{#CI_PLATFORM}}
CI/CD runs on {{CI_PLATFORM}}.
{{/CI_PLATFORM}}

{{^USE_DOCKER}}
Deploy directly without containerization.
{{/USE_DOCKER}}
```

There are no built-in variables: the template data is exactly the profile's
`variables:` map. An undefined variable renders empty and produces a warning.

See the [Templating Guide](/guides/templating) for complete syntax and advanced features.

## Distillation

Fragments can be distilled (AI-compressed) to reduce token usage. See the [Distillation Guide](/guides/distillation) for details.

```yaml
fragments:
  verbose-guide:
    content: |
      [Long, detailed content...]
    distilled: |
      [AI-compressed version...]
    content_hash: "sha256:abc123..."
    no_distill: false  # Set true to prevent distillation
```
