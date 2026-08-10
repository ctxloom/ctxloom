---
title: "Key management"
---

Trusting a publisher is the single highest-leverage act in ctxloom. It is also the only one
that is hard to take back. Trust a key, and **everything that key ever signs** — text,
executables, every future update — reaches your agent without review. That is the payoff, and
it is the risk, and they are the same sentence.

So this page is mostly about limits. What a key buys you, what it does not, and — most
importantly — **what revocation does not reach**.

## You already have the key

ctxloom never reads, generates, or stores private key material. Every signature is produced by
your existing `ssh-agent`. If you already sign git commits with SSH, there is nothing to set
up. If you don't have a key or an agent yet, see [Installation → Signing and
publishing](/getting-started/installation/#signing-and-publishing-needs-ssh) for what to
install:

```bash
ctxloom bundle sign my-tools     # writes a detached my-tools.yaml.sig sibling
ctxloom bundle sign --all        # every local bundle this project publishes
```

Key discovery is zero-config, in this order:

1. `--key` (or the `sign.key` config default) — a `SHA256:...` fingerprint, a path to a
   public key, or an ssh-agent key's comment, matched case-insensitively
2. `git config user.signingkey`
3. The sole identity in `ssh-agent`, when there is exactly one

A publisher signature covers the **raw bundle file bytes** and is carried as a detached
`<bundle>.yaml.sig` sibling in the same git tree at the same pinned commit. It is verified
*before* the YAML is parsed.

:::caution[`ctxloom review` does not accept `--key`]
Review resolves its countersigning key through the same discovery chain, but it does not
expose `--key` or honour the `sign.key` config default. On `review`, only `git config
user.signingkey` and `ssh-agent` are consulted.
:::

## The trust root

Trust is a property of a **signing key**, not of a repository. The trust root is the union of
three `allowed_signers` files, in OpenSSH format, read verbatim:

| Location | Scope |
|---|---|
| Embedded in the binary | ctxloom's own publish key |
| `~/.ctxloom/allowed_signers` | You |
| `.ctxloom/allowed_signers` | The project — committable, so a team inherits it |

All three are **unioned**. There is no precedence between them; precedence lives entirely in
the [decision function](/security/trust-states/#the-decision-function), never in the
filesystem. Hand-editing an `allowed_signers` file is fully equivalent to using the CLI —
it is read verbatim either way.

```bash
ctxloom signer trust context@acme.com --key ~/.ssh/acme-publish.pub
ctxloom trust signer list
ctxloom trust signer show context@acme.com
ctxloom signer untrust context@acme.com
```

The principal (`context@acme.com`) is just a label. **The key is the trust.**

## Namespaces are the role system

The `namespaces=` option on an `allowed_signers` entry is not decoration. It is what keeps one
assertion from being replayed as another:

| Namespace | Assertion |
|---|---|
| `publish.v1.ctxloom.dev` | "I, key K, published these bytes" — made by an author, over a bundle file |
| `approve.v1.ctxloom.dev` | "I reviewed these exact bytes and allow them to reach my agent" — made by you |
| `reject.v1.ctxloom.dev` | "I refuse these exact bytes / this ref" |

A key trusted only to `publish` **cannot approve content on your behalf**, and vice versa.
This is why a stolen publish key cannot forge approvals. ctxloom's own embedded key is scoped
to `publish` only, for exactly this reason.

A signature by a key that is not in your trust root, or that is scoped to the wrong namespace,
is simply **unsigned content to you**: quiet, no error, it takes the review path.

A signature that is present but does **not** verify over the bytes it sits beside — a trusted
key over different bytes, or a corrupted blob — is **tamper**. The bundle is withheld
entirely, never degraded to unsigned. Otherwise corrupting a `.sig` file would be enough to
downgrade a signed bundle into an unsigned one.

## Hardware keys vs software keys

This distinction is not academic, and it has a specific consequence.

If your countersigning key is a plain software key held in `ssh-agent`, then **any process
that can reach `SSH_AUTH_SOCK` can ask the agent to sign as you** — including an agent that
ctxloom itself just launched. That agent could countersign its own approvals. ctxloom detects
this and warns **once per review session**:

- **Hardware-backed** keys (`sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`)
  require a physical touch. The agent cannot sign without you.
- **Confirm-guarded** keys (`ssh-add -c`) prompt on every use.
- **Containerized runs** simply do not carry the socket.

It is a warning, not a block. ctxloom will not stop you from reviewing with a bare software
key on a host where the agent you are reviewing *for* can reach your `ssh-agent`. If the
approvals in your store are load-bearing for a team, use a hardware key.

With **no key at all**, review offers an explicit, confirmed **unsigned** path: decisions are
recorded as bare markers, exactly as forgeable as any file on disk. Those are written to the
personal store only. `ctxloom review --project` — which writes the committable store an entire
team then inherits — **requires** a real key and refuses to run without one. An unsigned
decision in a shared store would be a forgery primitive with a friendly name.

## What revocation does and does not reach

Read this section before you rely on `signer remove`.

**What it reaches.** Removing a key from `~/.ctxloom/allowed_signers` or
`.ctxloom/allowed_signers` stops that key's signatures counting as trusted on the next load.
Content that key signed falls back to the review path — it re-gates to pending and is withheld
until a human reviews it. Content you had *already approved* stays approved: your approval is
your own countersignature over the bytes, and it does not depend on the publisher's key.
Rejections likewise survive; nothing un-rejects.

**What it does not reach.**

:::danger[The embedded ctxloom key cannot be untrusted]
ctxloom's compiled-in trust root is **unconditionally unioned into every lookup**, and
`ctxloom signer untrust` only rewrites the user or project *file*. There is no negative-entry
mechanism. Running `ctxloom signer untrust ben+ctxloom@abbitt.me` does **not** stop
ctxloom-published bundles from being auto-trusted. If you want to review ctxloom's own content
by hand, there is currently no supported way to ask for that.
:::

**Revocation is local, and it is pull-based.** There is no revocation list, no OCSP, no
expiry, and no phone-home. Removing a key is an edit to *your* files. It reaches other
developers only if they pull a project `allowed_signers` you changed — and only when they next
sync. A key compromise is not announced to anyone by this system; you have to tell them.

**One key currently signs every ctxloom surface.** The same embedded publish key signs the
default bundles and the companion loadouts, so its compromise radius is every signed surface
at once. And the released binaries are not signed at all — see [Trusting the
Binaries](/getting-started/binary-trust/).

## Signing is never exposed to the agent

Signing and verification are **CLI-only** and are never exposed over MCP. Handing an agent a
`signer add` capability would defeat the entire property this design exists to provide.

## Practical guidance

- **Trust a publisher only when you would run anything it publishes.** The exemption covers
  every future update from that key, unreviewed.
- **Scope keys with `namespaces=`.** A publisher does not need approve rights.
- **Keep reviewer keys hardware-backed**, especially if you use `review --project` and a team
  inherits your decisions.
- **Remember rejection is supreme.** You can always reject unilaterally, no matter who signed
  it.

Back to: [Threat model](/security/threat-model/) · [Trust states and the
gate](/security/trust-states/)
