# 0037 — Adding a git repository is the trust act

## Status

Accepted

## Context

ctxloom delivers content — fragments, commands, skills, profiles, hooks and MCP
servers — from git repositories registered as remotes. A layer of trust
machinery sat on top of that: publisher signatures over bundle trees, a
per-user and per-project set of trusted signer keys, per-item review that
withheld executables until a countersigned approval existed, rejection and
retraction records, version floors that refused a pin moving backwards, and a
gate every exposure decided through.

Measured against the threat model that machinery defended little. The threat
model is OTHER users, accounts and networks. The local user is not in it: they
chose which repositories to add, and they run the binary that reads them. Every
layer of the trust machinery asked that same user, again, to vouch for content
they had already chosen by adding its repository — and the attacks it did stop
(a tampered fetch, a substituted repository) are already stopped by git over an
authenticated transport and by pinning commits.

The cost was real. The machinery spanned several packages, a signature
envelope specification, a review ceremony with its own stores, a lifecycle of
states, and a prompt or a withheld item on every path where content moved. It
was the largest single source of user-facing friction and of defects found in
review.

## Decision

**Registering a remote is the trust act.** `ctxloom remote create <name> <url>`
admits that repository's content; nothing else is asked. ctxloom keeps no
second trust decision over a registered repository's content.

What survives, because each defends something in scope or keeps a build
reproducible:

- **No resolution by reference.** Content resolves only through a registered
  remote; nothing registers one automatically. Every fetch and every read of
  installed content refuses an unregistered repository with one error
  (`remote.NotRegisteredError`, wrapping `remote.ErrRemoteNotRegistered`) that
  names the `ctxloom remote create` that would admit it. A profile shipped in a
  remote repository may name only bundles from that repository
  (`profiles.Profile.CheckOwnRepo`, `profiles.ErrCrossRepoReference`), so adding
  a repository never admits the repositories its profiles mention. A local
  profile may compose several registered repositories.
- **The lockfile pins every dependency to a commit**, for reproducibility, and
  only `ctxloom deps upgrade --yes` moves an existing pin. `deps pull`,
  `ctxloom init` and startup sync create first pins and never move one.
- **The upgrade diff.** `deps upgrade` without `--yes` is a preview that writes
  nothing. Every pin change, and every first pin, is disclosed item by item
  (`operations.PinChange`): what is added, removed or modified, the command,
  args, env, URL and headers of each hook and MCP server before and after, and a
  full diff of each changed script. Env and header values are shown only as
  fingerprints (`valueFingerprint`), because this output reaches terminals, CI
  logs and JSON consumers and those values carry tokens.
- **Engine repository trust.** Whether the human trusts the WORKING repository
  to run its own committed code is the engine's own answer, read and obeyed on
  every launch (`engine.Engine.Trust`; for claude, `claude.Claude.Trust` and
  `repoSourceArgs`). It is a different question from which repositories'
  content ctxloom delivers, and it stays.

What was removed: bundle and loadout signing and their verification, signer
keys and the `signer` commands, `bundle sign` and `--sign`, the embedded
ctxloom signing key, `--disable-sig-check`, release-artefact signatures,
`ctxloom review` and `bundle trust`/`reject`/`forget`, the approval and
rejection stores, the exposure gate and its trust Reasons, retraction, version
floors and `--allow-downgrade`, and the `sign:` config block.

**Why:** a check that asks the person who already made a choice to make it again
is ceremony, not defence. Registering a repository is already deliberate,
visible and reversible (`ctxloom remote remove`). Making it the only trust
decision puts the control where the decision is actually made, and the
upgrade diff keeps the one thing worth seeing — what changed before it runs.

Rejected alternatives:

- **Keep signatures, drop review.** A signature says who published content,
  never whether it is safe to run; against this threat model, git over an
  authenticated transport already answers "who", and pinning answers "which
  bytes".
- **A provenance flag on each remote** (human-added vs auto-registered). It
  keeps auto-registration alive and adds state. Remotes registered automatically
  by earlier versions are left as they are, and users are told to review
  `ctxloom remote list`.

## Consequences

- A registered repository's content reaches the agent at its pinned commit with
  no prompt and no withheld items. Adding a repository is the moment to judge
  it; `deps pull` shows a first pin's full contents, executables included.
- Content from a repository that is not registered — including one a profile
  names, and content already cached from a remote since removed — fails to
  resolve with the `ctxloom remote create` fix.
- Pin movement is explicit: a changed manifest constraint takes effect only on
  `deps upgrade --yes`.
- Supersedes [0036](0036-exec-gate-withholds-by-default-locality-is-the-trust-boundary.md)
  and [0004](0004-skip-review-each-ux.md). The normative description of what is
  trusted, and the accepted risks that remain, is
  [trust-model.md](../trust-model.md).
