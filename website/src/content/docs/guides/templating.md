---
title: "Templating"
---

Without templating, a fragment that mentions your project name or language is stuck to one project — reuse it elsewhere and you're hand-editing a copy per repo, and the copies drift. Write the fragment once with placeholders, and each project's profile fills them in with its own values: one fragment, as many projects as you have profiles for.

Fragments support [Mustache](https://mustache.github.io/) templating to make this work.

:::note[Skills are different]
This page describes templating in **fragments**. A skill exported as a slash command uses a narrower, unrelated mechanism: `{{VAR}}` placeholders are rewritten to positional shell arguments (`$1`, `$2`, … in first-occurrence order) at export time — the profile's `variables:` map is never consulted. Only the plain `{{word}}` pattern is recognized, so sections, inverted sections, raw output, and comments below do not work in skills.
:::

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

Template data comes entirely from the resolved profile's `variables:` map — there are no built-in variables. Undefined variables render as empty and log a warning (`ctxloom: warning: undefined variable: {{FOO}}`). (The `CTXLOOM_ROOT` shell environment variable overrides project-root detection; it is not available inside templates.)

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

- **Undefined variables**: Logged as a warning (`ctxloom: warning: undefined variable: {{NAME}}`), rendered as empty
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
