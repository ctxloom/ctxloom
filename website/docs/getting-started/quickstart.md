---
sidebar_position: 2
---

# Quick Start

Get up and running with SCM in minutes.

## Initialize Your Project

```bash
# Create .scm directory in your project
scm init

# Or create a global config at ~/.scm
scm init --home
```

## Create Your First Bundle

```bash
# Create a new bundle
scm bundle create my-standards
```

This creates `.scm/bundles/my-standards.yaml` with a basic structure.

## Add Content to Your Bundle

Edit the bundle to add your context:

```yaml
version: "1.0.0"
description: "My coding standards"
tags:
  - development

fragments:
  coding-standards:
    tags: [style]
    content: |
      # My Coding Standards
      - Use meaningful variable names
      - Write tests for all new code
      - Document public APIs
```

## Run with Your Context

```bash
# Include your bundle when running AI
scm run -f my-standards "Help me with this code"

# Preview what context would be sent
scm run -n

# Use a profile for common combinations
scm run -p developer "implement error handling"
```

## Common Workflows

```bash
# Run with specific fragment
scm run -f python-tools#fragments/typing "add type hints"

# Combine fragments ad-hoc
scm run -f security#fragments/owasp -f python#fragments/errors "audit this code"

# Include all fragments with a tag
scm run -t security "check for vulnerabilities"

# Switch AI backend
scm run -l gemini "use Gemini instead of Claude"
```

## Next Steps

- Learn about [Bundles](/concepts/bundles) and [Fragments](/concepts/fragments)
- Set up [Profiles](/concepts/profiles) for quick context switching
- Share content via [Remotes](/concepts/remotes)
