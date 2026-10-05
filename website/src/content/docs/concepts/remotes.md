---
title: "Remotes"
---

A teammate pastes you their review checklist in Slack. You paste it into your bundle, tweak two lines for your project, and now there are two versions that will never agree again. Multiply that by every project on the team and "our standards" stops meaning anything specific.

A **remote** is a Git repository ctxloom pulls [bundles](/concepts/bundles/) and profiles from, so a fragment or profile lives in exactly one place and every project references that source instead of a pasted copy. Update the bundle once, and `ctxloom deps pull` brings every project back in sync.

## Pre-configured Remote

After `ctxloom init`, the `ctxloom-default` remote is pre-configured, providing community bundles (which can also ship profiles).

```bash
# Run a bundle-shipped profile directly
ctxloom run -p 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer' "help with Go"
```

## Managing Remotes

```bash
ctxloom remote list                     # List configured remotes
ctxloom remote create <name> <url>         # Register a remote source
ctxloom remote remove <name> --yes       # Remove a remote
ctxloom remote show <name>            # Browse remote contents
ctxloom remote discover                 # Find public ctxloom repositories
ctxloom remote default <name>           # Set the default remote
```

A remote is an **address**, and nothing more: registering one grants its content
no access to the agent. Content published under an SSH signing key you trust reaches
the agent automatically; everything else from a remote lands as **pending** and is
withheld per item until you approve it with `ctxloom review`. Trust follows the
key, not the repository — see [Review and trust](/concepts/review-and-trust/).

### Add a Remote

```bash
# GitHub shorthand
ctxloom remote create myteam myorg/ctxloom-team

# Full URL
ctxloom remote create corp https://gitlab.com/corp/ctxloom
```

The forge resolves from the URL: a `forges:` entry in `remotes.yaml` whose
`base_url` names the same server (host and port; a scheme's default port may be
omitted) is used first — a different port is a different server and gets
neither that forge's endpoint nor its `token_env`. Otherwise github.com (and
the `owner/repo` shorthand) uses the GitHub API adapter; every other host —
GitLab, Gitea, self-hosted — uses the generic `git` adapter (clone + local read, with your
ambient git authentication). Pass `--forge` to override: `github`, `git`, or
the label of a `forges:` entry in `remotes.yaml` (for example a GitHub
Enterprise instance).

```bash
ctxloom remote create corp https://git.example.com/corp/ctxloom --forge git
```

## Consuming Remote Content

You don't "install" remote items into your project. Instead you author a local
profile that **references** remote content, then pull. `ctxloom deps pull`
fetches everything your local profiles reference, updates the lockfile, and
applies hooks — it takes no item argument.

```bash
# Consume a remote bundle (or one fragment of it)
ctxloom profile create testing --include ctxloom-default/testing
ctxloom profile create tdd --include ctxloom-default/testing#fragments/tdd

# Inherit a bundle-shipped profile (canonical URL ref)
ctxloom profile create my-dev --parent 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer'

# Fetch everything your profiles reference
ctxloom deps pull
```

Pulled content is saved locally in your `.ctxloom/` directory.

## Using Remote Content

### Direct Reference

Reference remote content directly without authoring a profile:

```bash
# Use a bundle-shipped profile
ctxloom run -p 'https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer' "help me"

# Use a remote fragment
ctxloom run -f 'https://github.com/ctxloom/ctxloom-default@bundles/testing#fragments/tdd' "add tests for this"
```

### In Profiles

Reference bundle-shipped profiles as parents:

```yaml
description: "My custom profile"
parents:
  - https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer
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

### Pull, update, upgrade

```bash
ctxloom deps pull      # resolve every constraint → lock.yaml and fetch exactly what
                         # the lock pins (stable: keeps the current commit while a
                         # constraint is unchanged)
ctxloom deps check    # report the newest commit available within each constraint
ctxloom deps upgrade   # re-resolve within constraints and move the LOCK (never the
                         # manifest); whether changed content reaches the agent is
                         # decided per item at exposure (ctxloom review)
```

Locking happens automatically as part of `pull` — there is no separate lock step.

`upgrade` resolves a range (`@^1.2`) to the newest matching tag, a branch to its
new tip, and leaves exact pins and [held](#holds) items untouched. It writes only
the lockfile — your profile YAML is never rewritten, so a version bump is a clean
`lock.yaml` diff. Whether any changed content reaches the agent is decided per
item at exposure and reviewed with `ctxloom review`; see
[Review and trust](/concepts/review-and-trust/).

### Holds

A **hold** freezes an item at its currently-locked commit so `upgrade` won't
advance it — even when its constraint would otherwise allow a newer one. It is
policy, not a manifest edit: the held commit still satisfies the constraint, so
nothing diverges.

```bash
ctxloom deps hold <name>     # freeze at the locked SHA
ctxloom deps unhold <name>   # release the hold
```

Holding a *profile* freezes its whole subtree: the held profile is read at its old
commit, so the bundles it pulls in stay frozen too. The hold lives on the lockfile
entry, and `lock.yaml` is committed, so a hold travels with the repository.

### When a pinned bundle cannot be read

A bundle the lockfile pins but whose content is not on disk fails to load with a
fix line that depends on why it is missing:

- **Never pulled here.** The pinned commit has no checkout on this machine. Run
  `ctxloom deps pull` (or remove the bundle from the profiles that name it).
- **Absent at its pin.** The commit is checked out but has no bundle where
  ctxloom looks, for example a pin that predates a layout change in the remote.
  `deps pull` keeps an existing pin at its commit, so it cannot fix this; run
  `ctxloom deps upgrade` to advance the pin. A [held](#holds) pin is never
  advanced, so the fix line says to `ctxloom deps unhold <name>` first.

### Pinned bundles are read-only

Content pinned from a remote is not yours to edit in place. A command that would
write into a pinned remote bundle is refused, and the message names the remote
and the pin along with both ways forward:

- to change it only here, fork it with `ctxloom bundle import <name>` and edit
  the local copy;
- to change it for everyone, edit it upstream, then `ctxloom deps upgrade`.

## Discovering Remotes

Find public ctxloom repositories:

```bash
ctxloom remote discover
```

This searches GitHub for repositories with ctxloom content.

## Creating Your Own Remote

Any Git repository with a `.ctxloom/content/` root can be a remote. Each
published bundle is a directory under `bundles/v2/`:

```
my-ctxloom-repo/
├── .ctxloom/
│   └── content/
│       └── bundles/
│           └── v2/
│               └── my-bundle/
│                   ├── bundle.yaml
│                   ├── SHA256SUMS
│                   └── .sigs/
└── README.md
```

Remotes distribute bundles; a profile you want to share ships inside a bundle
under its `profiles:` key. Push to any git host and share the repository URL.
