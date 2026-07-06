---
title: "Review and trust"
---

ctxloom holds one line: **a human sees third-party content before the agent
does.** Content from a remote you haven't trusted is withheld from the agent
until you review it, and every later change to it is withheld again. Reviewing is
one command — `ctxloom review`.

## What is exempt

First-party content is trusted without review:

- **Local** — fragments, skills, MCP servers, and hooks you authored in this
  project. A *copy* of a remote item is not local: items are keyed by their true
  source, so cloning a bundle into the cache doesn't manufacture local trust.
- **Builtin** — bundles shipped inside the binary. Trusting ctxloom trusts them.
- **Trusted sources** — remotes you've marked trusted (see
  [Trusting a source](#trusting-a-source)). `ctxloom-default` and the personal
  remotes `init` adds are trusted by default.

Everything else — any item from an untrusted remote — is **pending** until you
review it.

## The three states

Every remote item — fragment, skill, MCP server, or hook — is in exactly one
state:

- **pending** — never reviewed, or its content changed since you accepted it.
  Withheld from the agent.
- **accepted** — you reviewed this exact content. Bound to the content's hash: a
  later change returns the item to pending and asks for re-review.
- **rejected** — you declined it. Withheld permanently. Recorded by content hash
  too, so a renamed identical copy stays rejected.

Rejection wins over everything, including the first-party exemption — you can
reject an item even from a trusted source or a builtin.

## Reviewing

```bash
ctxloom review          # Walk pending items and decide each
ctxloom review --list   # Print the pending table without reviewing
```

`ctxloom review` walks every pending item, grouped by bundle:

- **New** items show their full content.
- **Updated** items — content you accepted before that has since changed — show a
  diff against the version you accepted.
- MCP servers and hooks display as **what they run**: command, args, env,
  matcher.

Per item, choose **[a]ccept**, **[r]eject**, or **[s]kip**; **[A]** accepts every
remaining item in the bundle. Accepting binds the item to its current content;
rejecting withholds it for good. Just looking never changes anything.

Off a terminal (piped, or with `--list`), review prints the pending table and
exits, so scripts and agents can see what a human still owes a look. `ctxloom
init` ends with a review session when anything is pending.

:::note[The lockfile grants nothing]
The lockfile only pins **which commit** of a bundle is installed — it never
exposes an item. Even a freshly pinned bundle withholds its items until you
review them. If a bundle's content isn't appearing, run `ctxloom review`.
:::

## How changes arrive

`ctxloom remote upgrade` re-resolves your dependencies within their version
constraints (see [Versioning, locking, and holds](/concepts/remotes/#versioning-locking-and-holds))
and moves the lockfile to the newest commit each constraint allows. It does not
gate at the lockfile: any changed content simply re-hashes to **pending** and is
withheld until you review it.

Passive `ctxloom remote pull` fetches exactly what the lock already pins and
never advances a SHA.

To freeze a dependency so `upgrade` never advances it, [hold](/concepts/remotes/#holds)
it — this is dependency management, not trust:

```bash
ctxloom bundle hold <name>     # freeze at the locked SHA (alias: pin)
ctxloom bundle unhold <name>   # release the hold (alias: unpin)
```

## Trusting a source

Trust a remote to exempt everything it publishes — text, executables, and all
future updates — from review:

```bash
ctxloom remote trust <name>     # exempt this remote's content from review
ctxloom remote untrust <name>   # gate its content behind review again
```

This sets `trust_bundles: true` for the remote in `.ctxloom/remotes.yaml`. Trust
is per-remote: your own `ctxloom-default` or team remote can be trusted while a
third-party remote stays gated. Trust a source only when you would run anything
it publishes. Trusting a source does not un-reject anything you've rejected.

## Accepting or rejecting one item

`ctxloom review` is the interactive porcelain; the same decisions are scriptable
per item:

```bash
ctxloom trust <ref>       # accept one item (e.g. code-quality#fragments/solid)
ctxloom blacklist <ref>   # reject one item everywhere
```

Both write the same states `ctxloom review` writes. Refs use the selector
syntax — `<bundle>#fragments/<name>`, `<bundle>#skills/<name>`,
`<bundle>#mcp/<name>`, or `<bundle>#hooks/<event>/<index>`.

## How a trust decision is made

One resolver decides every item's exposure. First match wins, and the default is
withhold:

1. **rejected** — ref rejected, or its content hash on the denylist → withhold
2. **local** — authored in this project (all kinds) → allow
3. **trusted source** — the item's remote is trusted → allow
4. **accepted** — accepted, and the content hash still matches → allow
5. otherwise → **pending**, withhold

A withheld item is silently absent from the agent's view; you get one aggregate
stderr notice — `N item(s) awaiting review — run 'ctxloom review'`. The gate
hashes the exact bytes before profile-variable substitution, so templating can't
smuggle content past it. Builtin bundles and profile *definitions* are not gated;
a profile's constituent items still gate at their own chokes.

The full model — storage formats, enforcement points, lifecycle, and known edge
cases — is documented in
[docs/trust-model.md](https://github.com/ctxloom/ctxloom/blob/main/docs/trust-model.md).
</content>
