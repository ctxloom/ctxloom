---
title: "Threat model"
---

A security claim you cannot state precisely is a security claim you cannot keep. This page
names the adversary, states what ctxloom defends, and — at equal length and in equal detail —
states what it does not.

If you only read one section, read [What we do not
defend](#what-we-do-not-defend). A product whose limits are hidden is worse than one with
fewer features.

## The cast

- **Alice** — a developer. She runs the agent. Her machine, her shell, her credentials. Every
  defense here exists for her.
- **Bob** — her teammate. Clones the same repo, inherits the same project config. He is not
  an attacker; he is the reason a decision has to be shareable.
- **Carol** — the team lead. She reviews content once, with `ctxloom review --project`, and
  commits the result so Alice and Bob inherit it. Her approvals are only worth as much as the
  key that made them.
- **Trent** — the platform or security team: the **trusted publisher**. Alice trusts his key
  once, and thereafter everything he signs flows to her agent without review. Trent's key is
  the crown jewel of this system.
- **Mallory** — the active attacker. She tampers with content after it was signed; publishes
  a look-alike library under her own key; typosquats Trent's repo; smuggles a hook past a
  hurried review; writes `signer: trent` into her own bundle YAML and hopes.

**There is no Eve.** Eve is the passive eavesdropper of the classic cast, and she is absent
on purpose. ctxloom makes **no confidentiality claim** about your context. Inventing an Eve
scenario would imply a defense we do not have. See below.

## What we defend

Each of these is enforced at the exposure choke, on the exact bytes about to reach the agent.

**Tampering after signing.** Mallory edits a bundle that Trent signed. The publisher
signature no longer covers the bytes it sits beside. This is treated as *tamper*, not as
"unsigned": the bundle is withheld entirely rather than degraded to the review path — so
corrupting a signature cannot downgrade a signed bundle into an unsigned one.

**Content changing under an approval.** Alice approved v1. Upstream ships v2. Her
countersignature covered v1's bytes and does not verify over v2's, so the item drops back to
pending and is withheld until she reviews it — as a diff against what she approved.

**A stranger's signature.** Mallory signs her bundle with her own key. Alice does not trust
that key. To Alice, the content is simply **unsigned**: quiet, no error, it takes the review
path. A signature by an untrusted key is not a credential and does not become a fourth state.

**A bundle naming its own publisher.** Mallory writes `signer: trent` into her bundle's YAML.
It does nothing. The field is not deserialized from content; it can only be set by a load
path that already verified a signature against Alice's trust root.

**Smuggling an executable past review.** A pending or rejected hook or MCP server is **never
written into the generated backend settings**. It is not registered-but-disabled; it is
absent. The harness cannot run what was never written. A pending or rejected skill gates at
the same choke — before it is resolved into the list a harness's own skill directory gets
written from, and before `ctxloom skill show`/`ctxloom://skills/{name}` will return it — so a
withheld skill's `scripts/` files, executable bit and all, are never delivered to your machine
until it is reviewed.

**A trusted publisher shipping something bad.** Rejection is evaluated *first*, ahead of
every allow — including the trusted-publisher exemption and including bundles that shipped
inside the ctxloom binary itself. Alice can always reject unilaterally, and no signature
un-rejects anything.

**Typosquats and URL variants.** Trust is keyed to the signing **identity**, not to the
location the bytes arrived from. A fork, a look-alike host, a compromised forge, or a
tampered clone cannot produce content that verifies under the key Alice actually trusted.
Repo URLs are canonicalized on both sides of every comparison, and a rejection of *content*
is deliberately signed with the ref omitted — so a renamed or moved identical copy stays
rejected wherever it reappears.

**A corrupted approvals store.** If a store exists but cannot be read, ctxloom does not read
it as "nothing rejected". It **denies every item** — including local and builtin content —
and raises a fatal trust-store finding. An unreadable store might be hiding a rejection, and
silently reopening a gate a human closed is the one failure mode that is not allowed to be
quiet. (A store that has never been created is fine; that is just a fresh project.)

**A machine rewriting approved content.** Distillation is an LLM rewriting a fragment. Those
are different bytes than the ones Alice read. So each exposed form is countersigned
independently, and switching the effective form re-gates the item. Approved content cannot be
silently replaced by machine-written content.

**A writable `.ctxloom/`.** An agent that can write files cannot manufacture an approval by
editing a ledger row, because there is no ledger row — an approval *is* a signature. A file
it cannot sign is inert noise. (This holds only under the preconditions in the next section.)

## What we do not defend

**We do not encrypt your context. There is no confidentiality claim.** Bundles travel over
git in the clear. Anyone who can read the repo can read every fragment, command, hook and MCP
declaration in it. ctxloom proves **provenance and integrity** — where content came from, and
that it was not changed. It does not, anywhere, keep it secret.

**Signed does not mean safe.** Trent's key can sign a malicious fragment, and the signature
will verify perfectly. A signature says *who*, never *whether this is good for you*. This is
the reason rejection outranks every signature, including ctxloom's own. If you trust a
publisher, you are trusting every future thing that key signs — text and executables, all
updates, unreviewed. Trust a publisher only when you would run anything it publishes.

**A stolen publisher key is a full compromise until you remove it.** Trust is inherited
broadly by design. Scope keys with `namespaces=`, keep reviewer keys hardware-backed, and
remember that a developer can always reject unilaterally.

**ctxloom's own embedded key cannot be untrusted.** The compiled-in trust root is
unconditionally unioned into every lookup, and `ctxloom signer remove` only rewrites the
user or project *file*. There is no negative-entry mechanism. Removing
`ben+ctxloom@abbitt.me` does **not** stop ctxloom-published bundles from being auto-trusted.
If you want to review ctxloom's own content by hand, there is currently no supported way to
ask for that. This is a known gap, not a subtlety.

**A writable trust root is game over.** An attacker who can append to your `allowed_signers`
file names their own key as trusted. Nothing downstream can help you.

**Your own ssh-agent can sign as you.** If your countersigning key is a plain software key
loaded into `ssh-agent`, then any process holding `SSH_AUTH_SOCK` — *including an agent
ctxloom itself just launched* — can ask that agent to sign approvals as you. ctxloom warns
once per review session when it detects this. It is a warning, never a block. The defenses
are `ssh-add -c` (confirm on every use), a hardware-backed key, or running the agent in a
container without the socket.

**The unsigned review path is forgeable, by construction.** With no key available at all,
`ctxloom review` offers an explicit, confirmed **unsigned** path: decisions are recorded as
bare markers, as forgeable as any file on disk. It is a labelled opt-in, never the default,
and it is never permitted in the committable project store. `ctxloom review --project`
requires a real key and refuses to run without one.

**Hook identity is positional.** `{event}/{index}` keying means inserting or reordering a
bundle's hooks shifts later hooks' identities. Approvals re-gate (safe), but a sticky
ref-level rejection can land on a different hook than the one you rejected. The content-level
rejection still catches identical content.

**A rejection of content is form-specific.** Rejecting an item content-rejects the raw and
distilled forms present *at rejection time*. A moved copy later exposed in a different form,
under a different ref, can escape the content component in that form.

**Signed bundles only verify over git.** Publisher verification is wired into the remote-git
seed and the companion loadout, and nowhere else. A signed bundle dropped into a directory
ctxloom reads is **not verified** — it is either first-party local content or carries no
signer. An organization cannot yet ship signed context through an MDM-style drop-in. This
fails safe (unverified content is reviewed), so it is a missing feature rather than a hole,
but it does not work today.

**An editor's own MCP servers bypass the trust gate entirely.** When an ACP-speaking editor
(Zed, or any other client) opens a session, it can hand ctxloom MCP servers directly in that
request. Those servers are forwarded to the engine as given — never checked against a
publisher signature, never routed through review or rejection, on any transport (stdio, http,
or sse). This is not an oversight: the gate authenticates content *ctxloom itself* resolves
from a bundle or a remote, and an editor's own session configuration has no publisher and no
bundle to check — it is Alice's own direct configuration of her own already-trusted editor.
Only the MCP servers ctxloom resolves for you (from bundles and remotes) are gated; anything
your editor hands ctxloom directly is outside this system's remit and rides along unreviewed.

**One key signs every ctxloom surface, and the release binaries are not signed at all.** A
single embedded publish key signs the default bundles and the companion loadouts, so its
compromise radius is every signed surface at once. The released *binaries* carry no signature
whatsoever — see [Trusting the Binaries](/getting-started/binary-trust/), which is honest
about what that costs you.

**`$PAGER` runs during review.** Review shells out to your pager, which is user-controlled
code execution at review time. Acknowledged and accepted, as it is in every tool that pages.

## The one line we hold

Everything above reduces to a single invariant: **a human sees third-party content —
including every update to it — before the LLM does.** First-party content is exempt: what you
authored in this project, what shipped inside the binary, and what a publisher you trust
signed. Everything else is born pending and withheld.

We do not claim to know whether a prompt is safe. We claim to know **who wrote it** and
**that it has not changed** — and to put it in front of you before it reaches the machine
holding your credentials.

Next: [Trust states and the gate](/security/trust-states/).
