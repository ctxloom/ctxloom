---
sidebar_position: 5
---

# Remotes

A **remote** is a Git repository for sharing bundles and profiles across teams and projects.

## Pre-configured Remote

After `scm init`, the `scm-main` remote is pre-configured, providing community bundles and profiles.

```bash
# Use remote profiles directly
scm run -p scm-main/python-developer "help with Python code"
```

## Managing Remotes

```bash
scm remote list                     # List configured remotes
scm remote add <name> <url>         # Register a remote source
scm remote remove <name>            # Remove a remote
scm remote browse <name>            # Browse remote contents
scm remote discover                 # Find public SCM repositories
```

### Add a Remote

```bash
# GitHub shorthand
scm remote add myteam myorg/scm-team

# Full URL
scm remote add corp https://gitlab.com/corp/scm
```

## Pulling Content

```bash
# Pull a bundle
scm remote pull scm-main/testing --type bundle

# Pull a profile
scm remote pull scm-main/python-developer --type profile
```

Pulled content is saved locally in your `.scm/` directory.

## Using Remote Content

### Direct Reference

Reference remote content directly without pulling:

```bash
# Use remote profile
scm run -p scm-main/python-developer "help me"

# Use remote fragment
scm run -f scm-main/security#fragments/owasp "audit this"
```

### In Profiles

Reference remote profiles as parents:

```yaml
description: "My custom profile"
parents:
  - scm-main/python-developer
bundles:
  - my-local-additions
```

## Discovering Remotes

Find public SCM repositories:

```bash
scm remote discover
```

This searches GitHub and GitLab for repositories with SCM content.

## Creating Your Own Remote

Any Git repository with `.scm/` structure can be a remote:

```
my-scm-repo/
├── .scm/
│   ├── bundles/
│   │   └── my-bundle.yaml
│   └── profiles/
│       └── my-profile.yaml
└── README.md
```

Push to GitHub/GitLab and share the repository URL.
