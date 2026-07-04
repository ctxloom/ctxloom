---
title: "Templating"
---

Fragments and skills support [Mustache](https://mustache.github.io/) templating for dynamic content.

## Basic Syntax

Use double braces for variable substitution:

```yaml
fragments:
  project-info:
    content: |
      # {{PROJECT_NAME}} Guidelines
      This project uses {{LANGUAGE}}.
      Team: {{TEAM}}
```

## Defining Variables

Template data comes entirely from the resolved profile's `variables:` map — there are no built-in variables. Undefined variables render as empty with a warning. (The `CTXLOOM_ROOT` shell environment variable overrides project-root detection; it is not available inside templates.)

### In Profiles

```yaml
# .ctxloom/profiles/developer.yaml
variables:
  PROJECT_NAME: "my-app"
  LANGUAGE: "Go"
  LOG_LEVEL: "debug"
  TEAM: "backend"
```

### In Config

```yaml
# .ctxloom/config.yaml
profiles:
  definitions:
    quick:
      variables:
        MODE: "fast"
```

## Variable Inheritance

When using parent profiles, variables inherit and can be overridden:

```yaml
# base.yaml
variables:
  LANGUAGE: "Python"
  FRAMEWORK: "FastAPI"
  LOG_LEVEL: "info"

# child.yaml
parents:
  - base
variables:
  PROJECT_NAME: "my-app"    # New variable
  FRAMEWORK: "Django"       # Override parent
  # LANGUAGE and LOG_LEVEL inherited from base
```

## Mustache Features

### Simple Variables

```mustache
Hello, {{name}}!
```

### Sections (Conditionals)

Profile variables are strings, so a section acts as a presence toggle: any non-empty value enables it, and an unset or empty variable skips it. Lists are not supported, so section iteration is not available.

```yaml
variables:
  DEBUG: "true"
```

```mustache
{{#DEBUG}}
Debug mode is enabled.
{{/DEBUG}}
```

### Inverted Sections (Falsy Check)

```mustache
{{^PRODUCTION}}
This is not production - be careful!
{{/PRODUCTION}}
```

### Raw Output (Unescaped)

```mustache
{{{HTML_CONTENT}}}
```

### Comments

```mustache
{{! This comment won't appear in output }}
```

## Error Handling

- **Undefined variables**: Logged as warnings, rendered as empty
- **Render failures**: Original content returned unchanged
- **All variables are strings**: Converted to `map[string]interface{}`

## Examples

### Project Context

```yaml
# Profile
variables:
  PROJECT: "api-server"
  LANGUAGE: "Go"
  VERSION: "1.0"

# Fragment
content: |
  # {{PROJECT}} ({{VERSION}})

  This {{LANGUAGE}} project follows these standards:
  - Use gofmt for formatting
  - Write tests for all public functions
```

### Conditional Content

```yaml
# Profile
variables:
  USE_DOCKER: "true"
  CI_PLATFORM: "github"

# Fragment
content: |
  ## Deployment

  {{#USE_DOCKER}}
  Build with: docker build -t app .
  {{/USE_DOCKER}}

  {{#CI_PLATFORM}}
  CI runs on: {{CI_PLATFORM}}
  {{/CI_PLATFORM}}
```

## Best Practices

1. **Use descriptive names** - `PROJECT_NAME` not `pn`
2. **Document required variables** - Note them in the fragment's `notes:` field
3. **Provide defaults in profiles** - Avoid undefined variable warnings
4. **Keep templates simple** - Complex logic belongs in code
5. **Use sections for optionals** - `{{#VAR}}...{{/VAR}}` handles missing gracefully
