---
sidebar_position: 4
---

# Profiles

A **profile** is a named configuration that references bundles, tags, and variables. Profiles enable quick context switching.

## Profile Format

Profiles are stored in `.scm/profiles/` as YAML files:

```yaml
description: "Standard development context"
parents:
  - scm-main/python-developer  # Inherit from remote profile
bundles:
  - my-custom-bundle           # Local bundle reference
tags:
  - production
variables:
  project_name: "my-project"
```

## Using Profiles

```bash
# Run with a profile
scm run -p developer "implement error handling"

# Preview profile context
scm run -p developer -n

# Use remote profile directly
scm run -p scm-main/python-developer "help with Python code"
```

## Managing Profiles

```bash
scm profile list                    # List all profiles
scm profile show <name>             # Show profile details
scm profile add <name>              # Create a new profile
scm profile update <name>           # Update profile
scm profile remove <name>           # Delete a profile
scm profile export <name> <dir>     # Export profile
scm profile import <path>           # Import profile
```

### Create a Profile

```bash
scm profile add developer -b python-tools -d "Standard dev context"
```

### Update a Profile

```bash
# Add bundles or parents
scm profile update developer --add-bundle security-tools
scm profile update developer --add-parent scm-main/base

# Remove bundles or parents
scm profile update developer --remove-bundle old-bundle
```

## Profile Inheritance

Profiles can inherit from other profiles using `parents`:

```yaml
description: "Senior developer context"
parents:
  - developer              # Inherit from local profile
  - scm-main/security     # Inherit from remote profile
bundles:
  - advanced-patterns     # Add more bundles
```

Inheritance is resolved in order, with later profiles overriding earlier ones.

## Variables

Profiles can define variables used in fragment templates:

```yaml
variables:
  project_name: "my-app"
  language: "Python"
  team: "backend"
```

See the [Templating Guide](/guides/templating) for using variables in fragments.
