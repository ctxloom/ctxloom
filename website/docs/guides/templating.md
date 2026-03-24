---
sidebar_position: 3
---

# Templating

Fragments support [Mustache](https://mustache.github.io/) templating for dynamic content.

## Basic Usage

Use double braces for variables:

```yaml
# In bundle fragment:
fragments:
  project-info:
    content: |
      # {{project_name}} Guidelines
      This project uses {{language}}.
      Team: {{team}}
```

```yaml
# In profile:
variables:
  project_name: "my-app"
  language: "Python"
  team: "backend"
```

## Built-in Variables

These variables are available in all templates:

| Variable | Description |
|----------|-------------|
| `SCM_ROOT` | Project root directory (parent of .scm) |
| `SCM_DIR` | Full path to .scm directory |

```yaml
fragments:
  paths:
    content: |
      Project root: {{SCM_ROOT}}
      Config location: {{SCM_DIR}}
```

## Variable Sources

Variables can come from:

1. **Profile variables** - Defined in profile YAML
2. **Parent profiles** - Inherited from parent profiles
3. **Built-in variables** - SCM_ROOT, SCM_DIR

### Profile Variables

```yaml
# .scm/profiles/developer.yaml
description: "Development profile"
variables:
  project_name: "my-project"
  language: "Go"
  log_level: "debug"
```

### Variable Inheritance

When using parent profiles, variables are inherited:

```yaml
# Parent profile
variables:
  language: "Python"
  framework: "FastAPI"

# Child profile
parents:
  - base-python
variables:
  project_name: "my-app"  # Adds to parent variables
  framework: "Django"     # Overrides parent value
```

## Mustache Features

### Conditionals

```yaml
content: |
  {{#use_typescript}}
  - Use TypeScript for all new code
  {{/use_typescript}}
  {{^use_typescript}}
  - JavaScript is acceptable
  {{/use_typescript}}
```

### Lists

```yaml
variables:
  reviewers:
    - Alice
    - Bob
    - Charlie
```

```yaml
content: |
  Code reviewers:
  {{#reviewers}}
  - {{.}}
  {{/reviewers}}
```

## Best Practices

1. **Use descriptive variable names** - `project_name` not `pn`
2. **Document required variables** - In fragment descriptions
3. **Provide defaults in profiles** - Avoid undefined variable errors
4. **Keep templates simple** - Complex logic belongs in code
