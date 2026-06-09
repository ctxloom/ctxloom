---
title: "Review and trust"
---

Remote dependencies change over time. ctxloom never silently adopts an upstream
change from a remote you haven't trusted — it **stages** the change for you to
review first. Trusting a remote opts it out of review.

## How changes arrive

`ctxloom remote upgrade` re-resolves your dependencies within their version
constraints (see [Versioning, locking, and holds](/concepts/remotes/#versioning-locking-and-holds))
and moves the lockfile:

- From a **trusted** remote, the change is applied to the active lock immediately.
- From an **untrusted** remote, the new commit is **staged** in a pending lockfile
  for review — nothing is installed until you approve.

Passive `ctxloom remote pull` fetches exactly what the lock already pins and
stages nothing — review is only ever triggered by an `upgrade`.

## Reviewing staged changes

```bash
ctxloom bundle review              # List bundles with staged changes
ctxloom bundle show-pending <name> # Print a pending bundle's YAML + diff vs active
ctxloom bundle approve             # Adopt the staged changes into the active lock
ctxloom bundle decline [name]      # Discard the staged change (all, or one by name)
```

`approve` merges the pending lockfile into the active one; your profile YAML is
never rewritten — only the lock moves. `decline` drops the pending change and
leaves the active lock untouched.

## Holding an item

To stop a specific item from being proposed for upgrade at all, [hold](/concepts/remotes/#holds)
it:

```bash
ctxloom bundle hold <name>     # freeze at the locked SHA (alias: pin)
ctxloom bundle unhold <name>   # release the hold (alias: unpin)
```

A held item is skipped by `upgrade` and never surfaces in review.

## Trusting a remote

Trust a remote to apply its upgrades without review:

```bash
ctxloom remote trust <name>
```

This sets `trust_bundles: true` for the remote in `.ctxloom/remotes.yaml`. Trust is
per-remote — your own `ctxloom-default` or team remote can be trusted while a
third-party remote stays gated behind review.
