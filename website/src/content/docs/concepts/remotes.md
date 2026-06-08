---
title: "Remotes"
---

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

## Versioning, locking, and holds

ctxloom versions remote dependencies the way `apt`, `npm`, and `uv` do — three
layers, with one rule: **the lock always satisfies the manifest.**

- **Constraint** — what a profile asks for, written in the reference's `@version`
  slot. Human-authored intent.
- **Resolution** — the exact commit that satisfies it, recorded in
  `.ctxloom/lock.yaml`. The lockfile is the *only* place the resolved SHA lives.
- **Hold** — a "don't auto-upgrade this" flag, applied without editing the manifest.

### Reference constraints

A reference's `@version` is a constraint resolved against the source repo's git
tags (ordered by semver) and branches:

| Reference | Meaning | `upgrade` behavior |
|---|---|---|
| `…@bundles/x` | track the default branch | advances to the new tip |
| `…@bundles/x@main` | track a branch (a channel) | advances to the branch tip |
| `…@bundles/x@^1.2` | newest semver tag in range | advances within the range |
| `…@bundles/x@v1.2.3` | an exact tag | never moves (exact pin) |
| `…@bundles/x@<sha>` | an exact commit | never moves (exact pin) |

To pin to a release, write the exact tag (`@v1.2.3`); to loosen, use a range or a
branch. Your existing `@<sha>` references are already valid — they're just the
tightest constraint.

### Lock, sync, update, upgrade

```bash
ctxloom remote lock      # resolve every constraint → lock.yaml (stable: keeps the
                         # current commit while a constraint is unchanged)
ctxloom remote sync      # install exactly what the lock pins (resolves nothing)
ctxloom remote update    # report the newest commit available within each constraint
ctxloom remote upgrade   # re-resolve within constraints and move the LOCK (never the
                         # manifest); trusted remotes apply, others stage for review
```

`upgrade` resolves a range (`@^1.2`) to the newest matching tag, a branch to its
new tip, and leaves exact pins and [held](#holds) items untouched. It writes only
the lockfile — your profile YAML is never rewritten, so a version bump is a clean
`lock.yaml` diff. Changes from an untrusted remote are staged for `ctxloom bundle
review` / `approve`; see [Review and trust](/concepts/review-and-trust/).

### Holds

A **hold** freezes an item at its currently-locked commit so `upgrade` won't
advance it — even when its constraint would otherwise allow a newer one. It is
policy, not a manifest edit: the held commit still satisfies the constraint, so
nothing diverges.

```bash
ctxloom bundle hold <name>     # freeze at the locked SHA (alias: pin)
ctxloom bundle unhold <name>   # release the hold (alias: unpin)
```

Holding a *profile* freezes its whole subtree: the held profile is read at its old
commit, so the bundles it pulls in stay frozen too. The hold lives on the lockfile
entry, which is typically gitignored, so it does not travel across `git clone`.

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
