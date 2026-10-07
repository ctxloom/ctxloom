---
title: "Trust"
---

ctxloom draws one line: **adding a git repository is the trust act.** Content
resolves only through a remote you registered (`ctxloom remote create`), and
what that remote serves reaches your agent. There is no separate review step,
and no per-item approval to record.

## Where trust is decided

- **Remotes** — registering a repository is the decision. A reference to a
  repository you have not registered is refused, naming the `ctxloom remote
  create` that would add it. A remote profile may refer only to bundles in its
  own repository; a profile in your project may combine bundles from any
  remotes you registered.
- **Your project** — fragments, commands, MCP servers, hooks, and skills you
  authored in this project are yours.
- **Companions** — a companion binary (ltk, taskloom, ...) runs only once you
  register it by name (`ctxloom companion add <name>`). Nothing else on your
  `PATH` is run for being there.

## What changes reach you

The lockfile pins which commit of each bundle is installed. `ctxloom deps
pull`, `ctxloom init` and startup create first pins and never move an existing
one. `ctxloom deps upgrade` previews what moving each pin brings in — every
item added, removed or changed, the command and arguments of every hook and
MCP server, and a diff of every changed script — and `ctxloom deps upgrade
--yes` applies it.

To freeze a dependency so `upgrade` never advances it, [hold](/concepts/remotes/#holds)
it:

```bash
ctxloom deps hold <name>     # freeze at the locked SHA
ctxloom deps unhold <name>   # release the hold
```

## Signatures

A signature says *who* published bytes, never *whether they are good for
you*. A bundle tree signed by its publisher is verified over its files when it
is installed: a signature that is present but does not cover the bytes beside
it is treated as tampering, and the tree is refused rather than installed.

Keys are kept in `allowed_signers` stores (`ctxloom signer`):
`.ctxloom/allowed_signers` for everyone who clones the repo,
`~/.ctxloom/allowed_signers` for you alone, plus the defaults embedded in the
binary.

## Why any of this exists

If it is not obvious why a *prompt library* needs a trust decision at all, start
with [A prompt is executable code](/security/prompts-are-code/): a bundle can put
a shell command in your harness's settings file. The rest of the case is laid
out in [What a bundle can do to you](/security/bundle-anatomy/) and the
[threat model](/security/threat-model/) — including, explicitly, what ctxloom
does **not** defend.
