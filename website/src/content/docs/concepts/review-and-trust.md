---
title: "Review and trust"
---

ctxloom holds one line: **a human sees third-party content before the agent
does.** Content from a remote is withheld from the agent until you review it, and
every later change to it is withheld again. Reviewing is one command —
`ctxloom review`.

## What is exempt

First-party content reaches the agent without review:

- **Local** — fragments, commands, MCP servers, hooks, and skills you authored in this
  project. A *copy* of a remote item is not local: items are keyed by their true
  source, so cloning a bundle into the cache doesn't manufacture local trust.
- **Builtin** — bundles shipped inside the binary. Trusting ctxloom trusts them.
- **Trusted publisher** — a bundle whose bytes were signed by a key you trust for
  the `publish` namespace (see [Trusting a publisher](#trusting-a-publisher)).
  Trust is keyed to the **signing key**, not to the repository the bytes came
  from: a fork, a typosquatted host, or a tampered clone cannot produce content
  that verifies under the key you actually trusted.

Nothing is trusted for being *added*. Adding a remote registers an address, and
that is all it does — `ctxloom-default` and the personal repos `init` adds take
the review path like anything else until their bundles are signed by a key you
trust.

Everything else — any unsigned remote item, and any item signed by a key you
don't trust — is **pending** until you review it.

## The three states

Every remote item — fragment, command, MCP server, hook, or skill — is in exactly one
state:

- **pending** — never reviewed, or its content changed since you approved it.
  Withheld from the agent.
- **approved** — you **countersigned this exact content with your own SSH key**.
  The signature *is* the approval record: at every exposure ctxloom rebuilds the
  bytes it is about to hand the agent and checks that a countersignature verifies
  over exactly those bytes. Change a byte and no signature covers it any more, so
  the item drops back to pending. The raw and distilled forms of an item are
  signed independently — approving one does not approve the other.
- **rejected** — you declined it, also by countersigning: once against the ref
  (sticky — it survives the content changing underneath), and once against the
  content with the ref deliberately omitted, so a renamed or moved identical copy
  stays rejected wherever it turns up.

Rejection wins over everything, including the first-party exemption — you can
reject an item even from a trusted publisher or a builtin.

A signature says *who*, never *whether it is good for you*. A validly signed
malicious fragment is still malicious, which is why rejection outranks every
signature, including ctxloom's own.

## Reviewing

```bash
ctxloom review             # Walk pending items and decide each
ctxloom review --list      # Print the pending table without reviewing
ctxloom review --project   # Record decisions in the committable project store
```

`ctxloom review` walks every pending item, grouped by bundle:

- **New** items show their full content.
- **Updated** items — content you approved before that has since changed — show a
  diff against the version you approved.
- MCP servers and hooks display as **what they run**: command, args, env,
  matcher — the exact executable surface your countersignature covers.

Per item, choose **[t]rust**, **[r]eject**, or **[s]kip**; **[T]** and **[R]**
apply that answer to every remaining item in the bundle, **[q]** quits. The
letters are the CLI's own verbs, so what you learn here works on the command
line. Just looking never changes anything.

Your signing key is resolved once, from `ssh-agent`, before the first item is
shown — a review session that can't record its result shouldn't spend your
attention first. If that key is a plain software key rather than a hardware one,
the session warns once: any process holding `SSH_AUTH_SOCK` — including an agent
ctxloom just launched — can ask the agent to sign approvals as you, unless the
key is confirm-guarded (`ssh-add -c`) or hardware-backed. It is a warning, not a
block.

Decisions land in your personal store, `~/.ctxloom/approvals`, one signature file
per decision. With **no key available at all**, review offers an explicit,
confirmed **unsigned** path: decisions are recorded as bare markers, exactly as
forgeable as any file on disk. Those go to the personal store only and are never
written to the committable one. `--project` writes the committable project store
(`.ctxloom/approvals`), so a team lead or CI can countersign once and every
developer who trusts that key inherits the decision — it **requires** a real
signing key, with no unsigned fallback.

Off a terminal (piped, or with `--list`), review prints the pending table and
exits, so scripts and agents can see what a human still owes a look. `ctxloom
init` ends with a review session when anything is pending.

:::note[The lockfile grants nothing]
The lockfile only pins **which commit** of a bundle is installed — it never
exposes an item. Even a freshly pinned bundle withholds its items until you
review them. If a bundle's content isn't appearing, run `ctxloom review`.
:::

## How changes arrive

`ctxloom deps upgrade` re-resolves your dependencies within their version
constraints (see [Versioning, locking, and holds](/concepts/remotes/#versioning-locking-and-holds))
and moves the lockfile to the newest commit each constraint allows. It does not
gate at the lockfile: changed content no longer verifies against the approval you
gave the old content, so it re-gates to **pending** and is withheld until you
review it.

Passive `ctxloom deps pull` fetches exactly what the lock already pins and
never advances a SHA.

To freeze a dependency so `upgrade` never advances it, [hold](/concepts/remotes/#holds)
it — this is dependency management, not trust:

```bash
ctxloom deps hold <name>     # freeze at the locked SHA (alias: pin)
ctxloom deps unhold <name>   # release the hold (alias: unpin)
```

## Trusting a publisher

Trust a **key**, and everything that key signs — text, executables, and all
future updates — skips review:

```bash
ctxloom signer trust context@acme.com --key ~/.ssh/acme-publish.pub
ctxloom trust signer list
ctxloom signer untrust context@acme.com
```

The principal (`context@acme.com`) is just a label; the key is the trust. Keys
land in your `allowed_signers` store — `~/.ctxloom/allowed_signers` for you,
`.ctxloom/allowed_signers` (with `--project`) for everyone who clones the repo,
plus the defaults embedded in the binary. All three are unioned. The
`--namespace` flag is the role system: `publish` (the default) lets a key exempt
the content it signs from review, while `approve` lets a key's countersignatures
approve items for you — so a lead can review on the team's behalf.

Publish your own bundles the same way — sign them, trust your key:

```bash
ctxloom bundle sign my-tools    # writes a detached my-tools.yaml.sig sibling
ctxloom bundle sign --all
```

A signature that verifies under a key you trust is checked over the bundle's raw
file bytes, before the YAML is even parsed. A signature from a key you *don't*
trust, or one scoped to the wrong namespace, is simply unsigned content to you:
no error, it takes the review path. A signature that is present but does not
verify over the bytes it sits beside is treated as tampering, and the bundle is
withheld entirely rather than degraded to unsigned.

Trust a publisher only when you would run anything it publishes: the exemption
covers every future update from that key, unreviewed. It does not un-reject
anything you've rejected.

## Deciding about one item

`ctxloom review` is the interactive porcelain; the same decisions are scriptable
per item, one verb per state:

```bash
ctxloom bundle trust <ref>    # approve one item (e.g. code-quality#fragments/solid)
ctxloom bundle reject <ref>   # reject one item everywhere, always
ctxloom bundle forget <ref>   # clear either decision — back to pending
```

Reach for `forget`, not `reject`, to undo a decision. A rejection is not the
inverse of an approval: it is sticky, it survives the content changing, and it
overrides both a trusted publisher and your project's own content. `forget`
removes whichever decision is on file — approval **or** rejection — and leaves
the item exactly as it was before anyone reviewed it.

All three go through the same path `ctxloom review` uses, so porcelain and
plumbing produce identical results on disk. Refs use the
selector syntax — `<bundle>#fragments/<name>`, `<bundle>#commands/<name>`,
`<bundle>#mcp/<name>`, `<bundle>#hooks/<event>/<index>`, or `<bundle>#skills/<name>`.

## How a trust decision is made

One resolver decides every item's exposure. It is fed the exact **bytes** about
to be exposed — never a precomputed hash, because a hash can only be compared
against a file that anything can write, while bytes can be *verified*. First
match wins, and the default is withhold:

0. **the approvals stores must be readable.** A store that has never been created
   is fine — that's a fresh project. A store that exists but can't be read is a
   fault, not an empty set: it might be hiding a *rejection*. Every item is
   denied, including local and builtin ones, and a fatal trust-store finding is
   raised. Fix or remove the store, then re-review.
1. **rejected** — a rejection covers this ref, or covers exactly these bytes →
   withhold
2. **retracted** — the *publisher* withdrew this bundle via their remote manifest
   (recorded locally the last time you synced) → withhold, even if you trust the
   key that signed it
3. **local** — authored in this project, every kind → allow
4. **builtin** — shipped inside the binary → allow
5. **trusted signer** — a key you trust to publish signed exactly these bytes →
   allow
6. **approved** — a countersignature from a key you trust to approve verifies
   over exactly these bytes, at this ref, in this form → allow
7. otherwise → **pending**, withhold

Builtins get their own step *below* rejection precisely so you can reject one;
they are routed through the same resolver as everything else rather than skipping
it. A content hash still exists, but only as an index — the filename under which a
candidate countersignature is looked up. Finding a candidate proves nothing:
only a successful cryptographic verify allows, so a hand-crafted file at the
right index resolves pending.

A withheld item is silently absent from the agent's view; you get one aggregate
stderr notice — `N item(s) awaiting review — run 'ctxloom review'`. The signed
payload is the exact bytes *before* profile-variable substitution, so templating
can't smuggle content past the gate. Profile *definitions* are not gated — a
profile is orchestration — but every item a profile pulls in gates at its own
choke.

The full model — storage formats, enforcement points, lifecycle, and known edge
cases — is documented in
[docs/trust-model.md](https://github.com/ctxloom/ctxloom/blob/main/docs/trust-model.md).

## Why any of this exists

If it is not obvious why a *prompt library* needs signatures at all, start with
[A prompt is executable code](/security/prompts-are-code/): a bundle can put a shell
command in your harness's settings file, and the ecosystem ships these unsigned, over
git, from strangers. The rest of the case is laid out in
[What a bundle can do to you](/security/bundle-anatomy/),
the [threat model](/security/threat-model/) — including, explicitly, what ctxloom does
**not** defend — and [key management](/security/key-management/).

