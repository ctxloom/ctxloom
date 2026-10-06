---
title: "Key management"
---

The trust decision in ctxloom is adding a repository (see [Trust](/concepts/review-and-trust/)).
A signing key says *who* published bytes: a signed bundle tree is verified over its files when
it is installed, and a signature that does not cover the bytes beside it is refused as
tampering.

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

## The trust root

Trust is a property of a **signing key**, not of a repository. The trust root is the union of
three `allowed_signers` files, in OpenSSH format, read verbatim:

| Location | Scope |
|---|---|
| Embedded in the binary | ctxloom's own publish key |
| `~/.ctxloom/allowed_signers` | You |
| `.ctxloom/allowed_signers` | The project — committable, so a team inherits it |

All three are **unioned**. There is no precedence between them. Hand-editing an `allowed_signers` file is fully equivalent to using the CLI —
it is read verbatim either way.

```bash
ctxloom signer trust context@acme.com --key ~/.ssh/acme-publish.pub
ctxloom signer list
ctxloom signer show context@acme.com
ctxloom signer untrust context@acme.com
```

`signer trust` and `signer untrust` write the project store by default; `--user` writes yours
instead.

The principal (`context@acme.com`) is just a label. **The key is the trust.**

## Namespaces are the role system

The `namespaces=` option on an `allowed_signers` entry is not decoration. It is what keeps one
assertion from being replayed as another:

| Namespace | Assertion |
|---|---|
| `publish.v1.ctxloom.dev` | "I, key K, published these bytes" — made by an author, over a bundle file |

ctxloom's own embedded key is scoped to `publish` only.

A signature by a key that is not in your trust root, or that is scoped to the wrong namespace,
is simply **unsigned content to you**: quiet, no error.

A signature that is present but does **not** verify over the bytes it sits beside — a trusted
key over different bytes, or a corrupted blob — is **tamper**. The bundle is withheld
entirely, never degraded to unsigned. Otherwise corrupting a `.sig` file would be enough to
downgrade a signed bundle into an unsigned one.

## Hardware keys vs software keys

If your signing key is a plain software key held in `ssh-agent`, then **any process that can
reach `SSH_AUTH_SOCK` can ask the agent to sign as you** — including an agent that ctxloom
itself just launched. Prefer:

- **Hardware-backed** keys (`sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`),
  which require a physical touch.
- **Confirm-guarded** keys (`ssh-add -c`), which prompt on every use.
- **Containerized runs**, which simply do not carry the socket.

## What revocation does and does not reach

Read this section before you rely on `signer untrust`.

**What it reaches.** Removing a key from `~/.ctxloom/allowed_signers` or
`.ctxloom/allowed_signers` stops that key's signatures counting as trusted on the next load.

**What it does not reach.**

:::caution[Untrusting the embedded ctxloom key suppresses it; it cannot delete it]
The compiled-in key ships in the binary, and no command removes it. Running
`ctxloom signer untrust ben+ctxloom@abbitt.me` records the principal in a `distrusted_signers`
file instead (`.ctxloom/distrusted_signers` by default, `~/.ctxloom/distrusted_signers` with
`--user`), and the trust root is rebuilt without that key on every verification after it. The
suppression does not reach ctxloom's companion loadout, which is admitted at exec (companion
allow) and never on its signature. A `distrusted_signers` file that exists but cannot be read suppresses every
embedded key, so an I/O error cannot quietly re-trust a key you removed. To trust the key
again, delete its line from the file.
:::

**Revocation is local, and it is pull-based.** There is no revocation list, no OCSP, no
expiry, and no phone-home. Removing a key is an edit to *your* files. It reaches other
developers only if they pull a project `allowed_signers` you changed — and only when they next
sync. A key compromise is not announced to anyone by this system; you have to tell them.

**One key currently signs every ctxloom surface.** The same release key signs the default
bundles, the companion loadouts and the released binaries (a detached `<binary>.sig` in the
`companion.v1.ctxloom.dev` namespace), so its compromise radius is every signed surface at
once. The embedded trust root grants that key `publish` only. The `companion` grant that lets
a signed binary execute is written into the project's `.ctxloom/allowed_signers` by
`ctxloom init`, where deleting the line withdraws it. The binaries carry no Apple or Windows
code-signing signature; see [Trusting the Binaries](/getting-started/binary-trust/).

## Signing is never exposed to the agent

Signing and verification are **CLI-only** and are never exposed over MCP. Handing an agent a
`signer trust` capability would defeat the entire property this design exists to provide.

## Practical guidance

- **Scope keys with `namespaces=`.**
- **Keep signing keys hardware-backed.**

Back to: [Threat model](/security/threat-model/)
