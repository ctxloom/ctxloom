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
unconditionally unioned into every lookup, and `ctxloom trust signer delete` only rewrites the
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

**A repository you clone can choose which of your keys signs.** ctxloom's zero-config
signing chain reads `git config user.signingkey`, and it runs `git config` inside the
working repository — so git answers out of *that repository's* `.git/config`, a file that
arrives with the clone. Cloning is therefore enough to redirect signing to a different key.
What it cannot do is produce an attestation from a key you do not hold: the signer is always
a live `ssh-agent` identity, and Mallory's key is not in your agent. A ctxloom signature is
an attestation from a controlled key or identity — and so is a git signature; `git commit
-S` resolves `user.signingkey` from the same file and claims the same kind of thing. We
accept the boundary git accepts, and we inherit git's residual with it: the signature still
comes from an identity you control, but possibly a different one than you intended, a
personal key where a work key was meant. That is an attribution problem, not a broken
attestation. Restricting the lookup to `--global` would break per-repository identities,
which are an ordinary setup; prompting would put a consent step into a flow that most often
runs unattended in CI.

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

**An agent can rewrite the repository you point it at — including the parts of `.git` that
execute code on your host.** ctxloom gives an agent its own git worktree, and the
repository's git *common* directory is exposed to that agent read-write: in a container it
is bind-mounted at its identical host path (`gitCommonDirMount` in
`internal/lm/isolation/container.go`, which mounts it with `ReadOnly` false), and on the
host runtime there is no boundary in the way at all. That directory is not only objects and
refs. It holds `hooks/`, and it holds the repo-local `config`, whose `core.hooksPath`,
`core.fsmonitor`, `core.sshCommand`, `core.pager` and `[alias]` keys all name commands git
will run. An agent that writes `hooks/pre-commit` there has planted a program that runs the
next time **a human commits in the primary checkout** — on the host, outside any container,
as that user. This is not a theoretical file: ctxloom's own repository has live `pre-commit`
and `prepare-commit-msg` hooks in that directory today, alongside the `.sample` files git
ships. So state the blast radius accurately. Under accident it is a spoiled branch. Under
malice it is **host code execution**, not repository corruption.

**Four things isolate four different risks, and only three of them are controls.** It is
worth being explicit about which one owns which question, because they are routinely
credited with each other's work.

- **The review and trust pipeline, at ingest.** Fragment review, skill review, publisher
  signatures, countersignatures. This is the control against a malicious *instruction*
  reaching an agent at all. Its reach is bounded — see the next entry.
- **The container, at runtime.** Isolates the agent *process* and the host filesystem: what
  the agent may execute, and what it can see and touch outside the mounts it was handed.
- **The worktree and branch, as blast radius.** Isolation against *accident* and ordinary
  agent error. A confused agent's edits land on a throwaway branch instead of your working
  tree, and are cheap to throw away. This is **not** isolation against malice; nothing about
  a worktree stops a deliberate write from reaching the shared `.git` both of you use.
- **The read-write git mount.** Not a control. It is the residual, accepted above.

An agent that has been given a working repository can write to that repository. That is what
makes it useful, and ctxloom cannot fundamentally prevent it while remaining useful — it is
inherent, not a defect we intend to fix. We do not defend against it; you carry it. The
decision is upstream of ctxloom: point an agent at a repository you would be willing to
restore from its remote, keep the review pipeline (not the container, and not the worktree)
as the thing standing between you and an instruction that wants your host, and read
`.git/hooks` and `.git/config` before your next commit if you have reason to doubt a run.

**Review covers the content ctxloom delivers, not everything an agent reads.** The gate is
strong on one ingress and silent on the rest. It covers fragments, skills, bundles, hooks
and MCP declarations — content ctxloom itself resolves and hands to the agent. It does not
cover a poisoned file already committed in the repository you point the agent at, a web page
the agent fetches, the contents of an upstream dependency it installs or reads, or an
injection carried in data the agent merely processes. None of those pass through the trust
gate, because ctxloom never resolved them. "Reviewed context" does not mean "this agent
cannot be given a malicious instruction".

**On Docker Desktop the host-to-container LLM transport has no cryptographic authentication, and is reachable across the container network.**
On non-Linux hosts (macOS and Windows, under Docker Desktop) ctxloom reaches the isolated LLM
plugin over plain gRPC — there is no per-run bearer token, no mTLS (go-plugin's AutoMTLS is
off), and the only handshake value is a static, compiled-in magic cookie that guards against
mis-execution rather than a credential. The host-side port is published to `127.0.0.1` only, so
it is not reachable from off the host. But the in-container listener binds all interfaces
(`0.0.0.0`) and ctxloom does not place agent containers on an isolated network, so any other
container on the same Docker bridge can reach the plugin directly at the container's IP — the
`127.0.0.1` host publish constrains only host-side access, never container-to-container traffic.
On a shared or multi-tenant container host, treat this as a trust boundary and isolate untrusted
workloads on separate Docker networks. On Linux the same transport is a bind-mounted unix socket
rather than TCP, so this caveat is specific to the Docker Desktop path.

**We own the MCP servers we seed. We do not own the ones we did not write.** An MCP
declaration is not text — a server entry names an executable, and the engine spawns it. So a
server that arrives through ctxloom's own supply chain is squarely ours: config, profiles, and
**bundles**, including remote ones, since a profile may carry an `mcp` block and profiles are
bundle-deliverable. Those servers are content we deliver, they go through the same gate as
every other bundle item, and if one is malicious that is our failure to have shown it to you.

Entries we did not write are a different matter, and the separation is structural rather than a
promise:

- For **Claude Code**, ctxloom never writes your project `.mcp.json` at all. It passes its own
  servers via `--mcp-config`, pointing at an out-of-cwd file, and deliberately omits
  `--strict-mcp-config` so the engine *layers* ctxloom's set on top of yours instead of
  replacing it. Your file is untouched because ctxloom never opens it.
- For **Antigravity** and **Kiro**, ctxloom does write the engine's native registry in place —
  and records the names it wrote in a sidecar **ledger**. Removal keys off that ledger, not off
  the file, so user-authored entries (including remote `url` servers) survive byte-for-byte,
  along with top-level fields ctxloom does not model.

Two limits follow, and neither is hypothetical. **The approval gate belongs to the engine, not
to us** — and an agent you configured with `permissions: bypass` is launched with that engine's
skip-permissions flag, which disables it. A bundle-delivered server then starts without a
prompt. That takes both a publisher you trusted and an agent you deliberately marked bypass, but
it means `bypass` is broader than "stop asking me about file edits": it also means "run what my
trusted bundles declare." **And a name ctxloom already claimed stays claimed.** In the ledger
path, a bundle declaring a name you have *not* used is written and recorded; a bundle declaring a
name you hand-authored is now skipped with a warning, leaving your entry alone. But that guard is
forward-looking only. If a name entered the ledger before it existed, ctxloom still treats that
name as its own on every reconcile — your original definition was overwritten at the time, and a
ledger records a name, never the content it replaced, so there is nothing left to give back.
Check the ledger, not the registry, when you want to know what ctxloom believes it owns.

## The one line we hold

Everything above reduces to a single invariant: **a human sees third-party content —
including every update to it — before the LLM does.** First-party content is exempt: what you
authored in this project, what shipped inside the binary, and what a publisher you trust
signed. Everything else is born pending and withheld.

We do not claim to know whether a prompt is safe. We claim to know **who wrote it** and
**that it has not changed** — and to put it in front of you before it reaches the machine
holding your credentials.

Next: [Trust states and the gate](/security/trust-states/).
