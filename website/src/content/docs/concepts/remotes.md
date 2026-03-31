---
title: "Remotes"
---

# Remotes

A **remote** is a Git repository for sharing bundles and profiles across teams and projects.

## Pre-configured Remote

After `ctxloom init`, the `ctxloom-default` remote is pre-configured, providing community bundles and profiles.

```bash
# Use remote profiles directly
ctxloom run -p ctxloom-default/python-developer "help with Python code"
```

## Managing Remotes

```bash
ctxloom remote list                     # List configured remotes
ctxloom remote add <name> <url>         # Register a remote source
ctxloom remote remove <name>            # Remove a remote
ctxloom remote browse <name>            # Browse remote contents
ctxloom remote discover                 # Find public ctxloom repositories
```

### Add a Remote

```bash
# GitHub shorthand
ctxloom remote add myteam myorg/ctxloom-team

# Full URL
ctxloom remote add corp https://gitlab.com/corp/ctxloom
```

## Pulling Content

```bash
# Pull a bundle
ctxloom remote pull ctxloom-default/testing --type bundle

# Pull a profile
ctxloom remote pull ctxloom-default/python-developer --type profile
```

Pulled content is saved locally in your `.ctxloom/` directory.

## Using Remote Content

### Direct Reference

Reference remote content directly without pulling:

```bash
# Use remote profile
ctxloom run -p ctxloom-default/python-developer "help me"

# Use remote fragment
ctxloom run -f ctxloom-default/security#fragments/owasp "audit this"
```

### In Profiles

Reference remote profiles as parents:

```yaml
description: "My custom profile"
parents:
  - ctxloom-default/python-developer
bundles:
  - my-local-additions
```

## Discovering Remotes

Find public ctxloom repositories:

```bash
ctxloom remote discover
```

This searches GitHub and GitLab for repositories with ctxloom content.

## Creating Your Own Remote

Any Git repository with `.ctxloom/` structure can be a remote:

```
my-ctxloom-repo/
├── .ctxloom/
│   ├── bundles/
│   │   └── my-bundle.yaml
│   └── profiles/
│       └── my-profile.yaml
└── README.md
```

Push to GitHub/GitLab and share the repository URL.
