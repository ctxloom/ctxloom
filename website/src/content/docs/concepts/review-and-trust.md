---
title: "Review and trust"
---

Remote dependencies change over time. ctxloom never silently adopts an upstream
change from a remote you haven't trusted — it **stages** the change for you to
review first. Trusting a remote opts it out of review.

Trust is enforced in two independent layers:

- **Lockfile review** controls *which version of a bundle is resolvable at all*
  (this page's staging and approval flow).
- **Per-item exposure** controls *whether an individual fragment, skill, MCP
  server, or hook reaches the agent*, keyed by content hash.

:::note[Approving is not trusting]
`bundle approve` moves the lockfile — it grants nothing at the item layer. An
approved bundle from an untrusted remote still withholds its items until they
are covered by a grant, a bundle posture, or remote trust. If you approved a
bundle and its content isn't appearing, this is why.
:::

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

## Trusting items and bundles

Remote trust gates how lockfile changes arrive; a second, finer surface gates
individual items. Trust-gating applies to fragments, skills, MCP servers,
hooks, and tooling declarations: a gated item from an unreviewed source is
withheld from the agent until granted.

```bash
ctxloom trust <ref>            # Grant one item (e.g. core#fragments/tdd)
ctxloom blacklist <ref>        # Withhold one item everywhere
ctxloom bundle trust <name>    # Trust a whole bundle as a source
ctxloom bundle untrust <name>  # Withhold the bundle's grant-less items
```

`ctxloom trust` grants a single item, bound to its current content hash: a
later content change drops the grant and forces re-review. Refs use the
selector syntax — `<bundle>#fragments/<name>`, `<bundle>#skills/<name>`, or
`<bundle>#mcp/<name>`.

`ctxloom blacklist` withholds an item from every exposure surface. It writes
both a sticky ref-level block (which survives content changes) and the item's
current content hash onto a denylist (so an identical copy under another name
stays blocked).

`ctxloom bundle trust` sets a SHA-agnostic posture toward the bundle as a
source. It cascades to every item in the bundle that has no explicit per-item
grant or blacklist; `ctxloom bundle untrust` flips the posture back so
grant-less items are withheld.

The same gate covers tooling: `ctxloom tooling` collects container-tool
declarations only from trusted bundles.

## How a trust decision is made

One resolver decides every item's exposure. First match wins, and the default
is deny:

1. Content hash on the **denylist** → deny
2. Sticky **blacklist** entry for the ref → deny
3. Explicit **grant** matching the current content hash → allow
4. **Bundle posture** (trusted/untrusted) → its decision
5. **Project-local** item (authored in this project, all kinds) → allow
6. **Trusted remote** → allow
7. Default → **deny**

Deny always beats allow: a blacklisted item stays withheld even inside a
trusted bundle or remote. Items you author in this project are trusted
automatically; a *copy* of remote content is not — items are keyed by their
true source, so cloning a bundle into the cache doesn't manufacture local
trust. A withheld item is silently absent from the agent's view; you get one
aggregate stderr notice pointing at the review commands.

Builtin bundles (shipped inside the binary) and profile *definitions* are not
gated; a profile's constituent items still gate at their own chokes.

The full model — storage formats, enforcement points, lifecycle, and known
edge cases — is documented in
[docs/trust-model.md](https://github.com/ctxloom/ctxloom/blob/main/docs/trust-model.md).
