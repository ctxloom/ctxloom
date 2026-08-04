# Content signing: design and rationale

> ## SUPERSEDED — historical design record (2026-07-13)
>
> **Superseded by [signature-envelope.spec.md](signature-envelope.spec.md)** (the
> normative wire contracts and payload framing) and
> **[trust-model.md](trust-model.md)** (the normative account of shipped behavior).
> This document is kept only as a record of *why* the design took the shape it did.
>
> **Do not cite this document as a description of current behavior.** Specific
> things in it that are now FALSE:
>
> - It describes `trust_bundles` / `remoteTrusted` / `Remote.TrustBundles` as live.
>   **They are deleted.** Source trust — "everything this repo publishes, forever,
>   hash-blind" — no longer exists; trust is keyed to a **signing identity** in
>   `allowed_signers` (spec §11).
> - It describes `ctxloom sign` and `ctxloom signer` as unimplemented. **Both ship.**
> - Its CLI surface and file layouts were superseded during implementation
>   (authored content now lives under `.ctxloom/content/`, not `.ctxloom/bundles/`).
>
> Where this document and the spec disagree, **the spec wins**; where the spec and
> trust-model.md disagree, **trust-model.md wins**.

## The problem

Today's trust model is four mechanisms: content hashes, an acceptance ledger in
`.ctxloom/trust.yaml`, a hash denylist for rejections, and a hash-blind bypass
for trusted sources. It works, and it is honest about what it does. But look at
its shape: a record that binds an approval to exact bytes, a ledger of who
approved what, and a revocation list. That is a hand-rolled signature scheme with
the cryptography removed. It gives integrity against accidental change and gives
nothing against a hostile publisher.

Two holes follow directly, and both are worth naming plainly.

**Source trust is hash-blind.** `EffectiveTrust` step 3
(`internal/operations/trust.go:122-131`, resolving through `remoteTrusted` at
`trust.go:170-180`) allows an item because its *repo URL* carries
`trust_bundles: true`. It never looks at the content. A remote you trusted once
can serve changed bytes forever, silently, and the gate will pass them. The
current doc already lists this as Known Gap 3, "Trusted sources are broad".
Trust is keyed to a location, and a location can be compromised, forked, or
typosquatted.

**The approval ledger is a plain file.** `.ctxloom/trust.yaml` is YAML on disk.
Anything that can write your home directory can insert `state: accepted` and
approve content on your behalf. That includes the coding agent ctxloom just
launched, which has file-write tools. An agent can today fetch a fragment, write
an acceptance for it, and read it back into its own context on the next assembly.
Nothing prevents this. The gate that exists to keep unreviewed text away from the
LLM can be opened by the LLM.

## What signing buys

Trust keyed to a signing *identity* rather than to a content hash or a source
name.

An identity survives things a URL does not. Substitute a fork, typosquat the
host, compromise the forge, tamper with an object in the clone: none of it
produces content that verifies under the key you actually trusted. The bytes
either carry that key's signature or they do not.

Identity is also portable across transports in a way a repo URL can never be. A
URL cannot travel with a blob on a companion binary's stdout; a signature can.
The signed artifact is the pair `(content bytes, detached signature)`, and that
pair is inert with respect to how it moved. Sign a bundle once and it verifies
identically whether it arrives through a git remote, a tarball, an object store,
an MDM push, or a companion's stdout. That property is what lets an organization
publish signed context once and ship it through whatever channel it already runs.

## Countersigning

Approving content means the human signs the exact bytes with their own key, and
that signature *is* the record. There is no separate ledger. One primitive
instead of two.

Two properties fall out for free, and they are the two the current model builds
by hand.

Content changes upstream, so the countersignature no longer verifies over the new
bytes, so the item returns to pending. That is exactly today's "acceptance binds
to the `(raw, distilled)` hash pair; any change re-gates to pending" — obtained
without maintaining a hash ledger at all.

A rejection signs bytes, not a ref. It therefore verifies against those bytes
*wherever they appear*, so a rejected fragment that is renamed, moved, or
republished under someone else's key still fails. That is the content-hash
denylist, obtained for nothing.

The asymmetry there is deliberate and load-bearing. An approval binds the ref
(narrow: this item, here, in this form). A content rejection omits the ref
(broad: these bytes, anywhere). Approval should be narrow and rejection should be
broad, and collapsing the two payloads into one shape would silently break one
property or the other.

## Signatures authenticate; review authorizes

This distinction carries the whole design.

A signature says *who*. It never says *whether this is good for you*. A validly
signed malicious fragment is still a malicious fragment, and the ctxloom release
key is perfectly capable of signing a fragment that talks your agent into
exfiltrating secrets.

So there is no "signed" trust state. The three states are unchanged: pending,
accepted, rejected. A signed item whose key you do not trust is not a fourth
thing; it is pending. Signer identity is an *input* to the decision, never a
state of the item.

And rejection stays supreme. It is evaluated first, ahead of every allow, exactly
as it is today. You can reject content signed by the ctxloom release key itself.
You can reject a builtin. Nothing outranks a rejection, and no signature can
un-reject anything.

The proposed decision function is six steps, first-match-wins, fail-closed:
rejected denies; local allows; builtin allows; a trusted publisher signer allows;
a valid approval countersignature allows; everything else is pending and
withheld. Step 4 is what replaces the hash-blind `trust_bundles` bypass — trust
moves from the repo the bytes came from to the key that signed them.

## Scope: what is gated, and what is not

This boundary is easy to leave implicit, and an invisible security boundary is one
people trip over. So, precisely:

**Signing and review exist for content arriving from elsewhere.** Remote bundle
repos, corporate and personal and third-party. Any publisher who is not you.

**Companion loadouts are the exception, and the exception is instructive.** They
used to be listed above. They are not any more: a loadout is read by **executing**
the companion binary, so reviewing its content puts the prompt strictly *after*
the arbitrary code execution it would be protecting you from. Content review in
that position buys ~nothing and costs friction on a tool the user deliberately
installed — and routine prompts are how you teach people to click through the
ones that matter. The decision moved to the point that *does* have purchase:
**whether ctxloom may run the binary at all**, recorded trust-on-first-use
against the binary's absolute path and SHA-256, and refused outright in any
non-interactive session. Signing still matters for a loadout — a signature that
*fails* to verify is tamper and withholds the bundle entirely — it simply is not
what admits it. See `docs/trust-model.md`, "Companion loadouts".

**They do not gate your own project's repo.** A bundle committed at
`.ctxloom/bundles/*.yaml` is a `ctxloom:local` ref (`internal/remote/reference.go:17`)
for every teammate who clones the project, so it is allowed at step 2
(`internal/operations/trust.go:119-121`) without review and without a signature
check. This is deliberate and it stays. Builtin bundles compiled into the binary
are likewise allowed without review.

The reasoning is worth saying out loud. You already trust that repo enough to run
its build scripts, its devcontainer, and its test suite as yourself. Its fragments
sit inside a boundary you crossed the moment you cloned it. Requiring a signature
to read prose from a repo whose code you already execute would be ceremony, not
security. This is the same reasoning that makes a committed `allowed_signers` file
a sound bootstrap for the team root, below.

The residual risk is real and is named in the team journey: a fragment merged into
the project repo reaches every teammate's agent unreviewed. The control for that
is code review, not signing.

## The UX surface

Signing has to be the easy path. If signing is even slightly annoying,
publishers ship unsigned, every user of that bundle eats a review prompt, review
fatigue sets in, people click through, and the trust model becomes theater. An
unsigned bundle is not a neutral outcome. It is a tax levied on every one of that
bundle's users, and ease of signing is therefore a security property.

`ctxloom sign <ref>` is a top-level verb over any ref. This mirrors git, where
signing is a flag on the producing action (`git commit -S`, `git tag -s`) rather
than a separate ceremony. Publishing commands take `--sign` (`ctxloom fragment
push <name> --sign`, `ctxloom command push <name> --sign`), and
`ctxloom config set sign.default true` makes it the default for anyone who
publishes. The best signing ceremony is the one that already happened.

A publisher signature covers a whole bundle **file**, so an item ref resolves to
its containing bundle and says so:

```
$ ctxloom sign my-tools#fragments/go-testing
Signing bundle my-tools (contains fragments/go-testing) — signatures cover whole bundles.
  .ctxloom/bundles/my-tools.yaml  →  .ctxloom/bundles/my-tools.yaml.sig
```

This is not a limitation to apologize for; it is what keeps the publisher
signature parser-independent. Verification is a byte comparison that happens
before any YAML is parsed.

The refs follow the existing grammar in
[reference-grammar.md](reference-grammar.md), reusing the same item-ref parser
that `trust` and `blacklist` already use. No second grammar. Note that
`ctxloom-default` is a remote *alias*, not a bundle, so it does not appear alone
on the left of a `#`:

```
ctxloom sign my-tools                                       # bare = local bundle (the common case)
ctxloom sign ctxloom-default/go-tools#fragments/go-testing  # → signs bundle go-tools
ctxloom sign https://github.com/ctxloom/ctxloom-default@bundles/go-tools
```

Key discovery is zero-config, in order: `git config user.signingkey` when it
names an SSH key, then ssh-agent when it holds exactly one identity, then an
explicit `--key` or `sign.key` which overrides both. Anyone already signing git
commits needs no ctxloom setup at all. When the agent holds several keys and git
names none, ctxloom refuses to guess and prints the fingerprints with the command
to make a choice stick, because signing under the wrong identity produces a
signature nobody trusts and the publisher will not find out until a user
complains. When no key exists anywhere, `--sign` is a hard error and never
degrades to a silent unsigned publish.

Countersigning happens inside `ctxloom review`, which is unchanged as the single
review porcelain. Approving an item signs its bytes instead of writing a hash row.

Signer management is CLI-only: `ctxloom signer add|list|show|remove`, over a
store in the `ssh-keygen` `allowed_signers` format, verbatim. It is **never
exposed over MCP**. Handing an agent a `signer add` tool would give it precisely
the capability this design exists to deny it.

Adding a signer is the most dangerous command in the feature and it says so,
naming the real consequence and showing the fingerprint to check out of band:

```
Trust context@acme.com as a PUBLISHER?

  SHA256:8Ja1xkV9…q0Zc   (ssh-ed25519)

  Everything this signer ever publishes — text AND executables (MCP servers,
  hooks), now and in every future update — will reach your agent WITHOUT REVIEW.
  Verify this fingerprint out of band before you continue.

  [y/N]
```

For an approve-namespace key the consequence is different and is worded
differently: everything this signer ever *approves* reaches your agent
unreviewed, which is delegating your review decisions to them, permanently.

## Trust roots and how they compose

A real session routinely composes content from three trust roots at once, and
this is the normal configuration rather than a corner case.

The **team** keeps bundles in the project repo, alongside the code. The
**organization** runs a corporate bundle repo, a remote, signed with an org key.
The **developer** may keep a personal bundle repo, also a remote, signed with
their own key, while consuming the corporate one.

The thing to understand is that **trusted-signer is per key, not per source**.
Trusting the org key does not trust the team lead's key, and trusting the lead
does not trust the org. Every item is judged by whoever signed *it*. There is no
"this repo is trusted" object anywhere in the design; that object is precisely
what is being deleted.

The `allowed_signers` store has three locations and they are unioned, with no
precedence between them: keys compiled into the binary (ctxloom's own release and
bundle keys), the user store at `~/.ctxloom/allowed_signers`, and a committable
project store at `.ctxloom/allowed_signers`. Precedence lives in the decision
function, not in the filesystem. The embedded defaults are removable, so a user
who does not want to auto-trust ctxloom-default's bundles can say so, and that
content then takes the review path like anything else.

Approvals live in two stores that map onto the topology directly. Personal
decisions go to `~/.ctxloom/approvals/` and follow the developer across every
project. Shared decisions go to `.ctxloom/approvals/` in the project repo, are
committed, and are inherited by teammates and by CI. Both are read at gate time
and unioned into one candidate set.

Conflicts resolve by the step order, and the consequence worth stating plainly is
that **rejection is supreme across all roots**:

| Case | Outcome |
|---|---|
| Project store approves X; you have personally rejected X | **DENY** (rejected) |
| You approve X; the project store rejects X | **DENY** (rejected) |
| Project store approves X; you have not reviewed X | ALLOW, *iff* you trust the approving key |
| Project store approves X, signed by a key you do not trust | pending (inert, not an error) |

A personal rejection beats an inherited approval from the team lead or from the
org. That is the property that keeps a developer in control of their own agent
even inside an organization that centrally approves content for them.

It cuts the other way too, deliberately: a user cannot locally override an
org-wide rejection by approving the content themselves. Their only recourse is to
drop the org's reviewer key entirely, which drops *all* of that key's decisions
at once. That is coarse, and it is the correct trade. The alternative is a
per-item "I override my security team" escape hatch, which is exactly what an
attacker would aim the agent at.

The committable project store is not a new attack surface. An approval signed by
a key that is not in your `allowed_signers` is inert. Committing a malicious
approval requires the victim to already trust the attacker's key, at which point
the attacker did not need the approval — they could have signed the content.
Authority comes from the key, never from the file's location on disk.

Companion loadouts ride this same model for *authentication*: a signed loadout is
judged by its signer exactly like any other bundle content, and one whose
signature does not verify is withheld as tamper. What they do **not** ride is
review — see "Scope" above and `docs/trust-model.md`'s "Companion loadouts". The
gate for a companion is exec, not content, because exec happens first.

## Bootstrapping trust, per root

A signature proves who signed. It cannot tell you whose key to trust in the first
place. Every signing scheme bottoms out here, and an enterprise story that
hand-waves it is not a story. The three roots reach the laptop by different
paths, and the differences are the point.

**The team root largely solves itself.** The team commits
`.ctxloom/allowed_signers` into the project repo, next to the bundles and
approvals it authorizes. This is trust-on-first-clone, and it is exactly as
strong as your trust in that repo — which is already strong enough that you run
its build scripts, its CI, its Makefile, and its devcontainer as yourself. A repo
that can execute code on your machine can certainly name a key. Naming its
signers is strictly *inside* a boundary you already crossed, so it adds no new
exposure. What it does not do is protect you from cloning the wrong repo, and no
key-distribution scheme can, because at that point the attacker is your trust
root.

**The org root arrives by a different channel**, and this is where enterprise
provisioning earns its keep. The org places `allowed_signers` at
`~/.ctxloom/allowed_signers` through the same fleet-management channel that
provisions the laptop. A key delivered that way is as trustworthy as the machine
image itself, which is the strongest practical option available. It requires an
org that has such a channel, so it is useless for individuals and for open source.
The fallback is an explicit `ctxloom signer add` with an out-of-band check against
a fingerprint the org publishes.

**The personal root has no bootstrap problem.** It is the developer's own key,
already in their own agent. Self-trust.

The four paths, with what each actually guarantees:

| Path | Guarantees | Fails when |
|---|---|---|
| `allowed_signers` committed in the project repo *(default for teams and OSS)* | Trust-on-first-clone; inherits the repo's existing trust boundary and widens it by nothing | You cloned a malicious fork or a typosquat — a prior question signatures were never going to answer |
| Org-managed config / MDM push *(recommended for enterprises)* | As trustworthy as the machine image | The org has no such channel |
| Explicit CLI add with an out-of-band fingerprint check | The only path with an independent verification step; resists a compromised repo and a compromised distribution channel at once | The human does not actually check the fingerprint, and most will not |
| Embedded in the binary *(ctxloom's own keys only)* | Trust in the binary — circular, and unavoidably fine: if you do not trust the binary, nothing it says can help you | Not usable by third parties |

**TOFU / first-sight prompting is rejected.** "This bundle is signed by unknown
key `SHA256:abc…` — trust it? [y/N]" fires at the moment the user is least
equipped to answer, is triggerable by the attacker at will, and trains
click-through. Unknown-key content is simply *unsigned content to you*: it takes
the review path, where a human reads the actual bytes. That is the correct
default and it needs no prompt.

## Three journeys

### The individual developer

She pulls a remote bundle. Its content is unsigned, or signed by a key she has
never heard of — which amounts to the same thing, quietly and without a scary
error, because a warning for every unknown publisher just teaches people to
ignore warnings. The items are pending, so they are withheld from her agent.

She runs `ctxloom review`, reads the fragments, and accepts the ones she wants.
Accepting countersigns the bytes with her SSH key. That is the whole change from
today: same porcelain, same three keys to press, and the record it writes is a
signature instead of a hash row.

A week later the bundle's author changes a fragment upstream. She pulls. The new
bytes are not the bytes she countersigned, so her signature no longer verifies
over them, so the item is pending again and withheld. `ctxloom review` shows her
a diff against what she approved. She reads the diff and accepts, or she doesn't.

Nothing else about her workflow changes. Fragments she authors in her own project
are local, first-party, and never gated, so **local authoring never requires a
key**. This is the overwhelmingly common case. Signing her own content is optional
and matters only when she publishes it for others. If she has no SSH key at all
and does not want one, `ctxloom review` detects that up front, before wasting her
attention on twenty fragments, and offers unsigned approval records behind an
explicit opt-in. That path is exactly as safe as ctxloom is today, which is to say
her agent can forge those records, and the confirmation says so in those words.

### The team lead

His team keeps its bundles in the project repo, and this is the low-friction
journey: **those bundles need no signing at all.** They are local content, allowed
at step 2, never gated. A teammate clones the repo and the fragments simply work.
He signs them only if the team also publishes them as a remote for *other*
projects to consume.

The residual risk in that arrangement is real, and he should know it. A fragment
merged into the project repo reaches every teammate's agent unreviewed and
unsigned. A pull request that adds code is read as code. A pull request that adds
a fragment is prose, and prose slides through review in a way a diff to a build
script does not — while functionally being instructions to an agent that then acts
with the developer's credentials.

**The control is code review.** In-repo bundle content is protected by exactly the
same mechanism as in-repo code, which means his PR review *is* the trust gate for
it. A change to a fragment deserves the same seriousness as a change to a CI
script, because that is the closest analogue to what a fragment actually is.
Signing does not help here and is not meant to.

Where signing earns its keep for him is the content arriving from *elsewhere*:
the corporate bundle repo, a personal one, third-party bundles. (Not companion
loadouts — those are admitted at exec, not reviewed as content; see "Scope"
above.) He reviews that content once, with `ctxloom review --project`,
countersigning with his key. The signatures land in `.ctxloom/approvals/`. He
commits them, alongside a `.ctxloom/allowed_signers` naming his approve key.

Every teammate who clones inherits those approvals and never re-reviews that
outside content. They need no key of their own, because verifying a signature
requires no key — only *making* one does. Any of them can still reject something
unilaterally, and that rejection beats his approval on their machine.

This is also the CI story, and CI is just a teammate who never approves anything.
The runner clones, and every item is either publisher-trusted or covered by a
committed approval signed by a key in the committed `allowed_signers`. Nothing is
pending. No prompt, no TTY, **no secret and no key on the runner**. If a bundle is
upgraded and its bytes change, the committed approval stops verifying and the item
goes pending, so CI should run in strict mode where pending is fatal. A red build
that says "a human needs to review this" is the intended behavior.

### The organization

Acme publishes context centrally from a corporate bundle repo. In CI, it signs its
bundles with the org key. A security reviewer reviews the content once, centrally,
countersigning approvals with a reviewer key. Acme ships three things: the signed
bundles, an `allowed_signers` naming the org publish key and the reviewer's
approve key, and the reviewer's countersignatures. Those are the same three files
a team lead ships. A team lead and an organization are the same actor at different
scale, which is the sign the design is factored correctly.

The org key and the reviewer key are separate keys, scoped by the `namespaces=`
option that `allowed_signers` already has. That option *is* the role system, for
free: a publish-only key cannot approve content even if it is stolen, and an
approve-only key cannot publish under the org's name. Signed companion loadouts go
out the same way and are authenticated by their signer like anything else — with
the difference that a companion's admission is decided at exec rather than by
review (see "Scope").

Every Acme developer who trusts those keys gets verified, pre-approved content
with zero review prompts, zero secrets on their machine, and no key of their own.
The key bootstrap is the one genuinely hard step, and it is the table above: MDM
for the fleet, or `signer add` with an out-of-band fingerprint check.

## What it costs

Four things get worse. None of them are footnotes.

### The anti-self-approval property is off by default

This is the one to read twice, and it is the spec's own strongest argument against
building the feature at all.

`SSH_AUTH_SOCK` is deliberately **not** stripped from host-run agents. Stripping
it would break legitimate agent workflows (`git push` over SSH, an agent signing
its own commits), and those are real and common, so we do not break them. A
host-run agent inherits the full parent environment
(`internal/lm/isolation/runner.go:65`).

ssh-agent is a signing oracle. Any process holding that socket can ask it to sign
arbitrary bytes. It cannot *read* the key, and it does not need to: it needs a
signature, and the agent will hand it one, silently. So a coding agent can ask
your ssh-agent to sign an approval for the fragment it just wrote, and get one.

> **In the posture most developers are actually in, this design does not remove
> the forgery. It changes its shape, and adds a credential whose compromise is
> global rather than per-project.**

The hash ledger's weakness was local: write `trust.yaml`, forge approvals in
*this* project. A stolen signing key forges approvals in *every* project,
retroactively and prospectively. That is a bad trade, and it becomes a good one
only under one of these:

| Posture | Can a host-run agent forge your approval? |
|---|---|
| No key, unsigned approval records | **Yes.** Writes the record directly. Identical to today. |
| Bare ssh-agent (`ssh-add`) — the common default | **Yes, silently.** It asks the agent to sign, and gets a signature. **No protection.** |
| Confirm-before-use (`ssh-add -c`) | **No, not silently.** Every signature raises a confirmation naming the request. An agent trying to self-approve produces a dialog you never asked for, which is both the block and the alarm. |
| Hardware key (`ssh-keygen -t ed25519-sk`) | **No.** Signing needs a physical touch. No software process can produce it. |
| Containerized agent | **No, structurally.** No `SSH_AUTH_SOCK` in the container at all. |

Confirm-before-use is the minimum bar for the property to be real. A hardware key
is the gold standard and costs us no code, since FIDO2 signing is native to
`ssh-keygen -Y sign`. Containers get it structurally and for free, which is one of
the better arguments for running agents in containers at all. Anyone who plumbs an
agent socket into a container in future is making a security-relevant change to
this design, not a convenience.

So ctxloom must **warn you when your key is a plain software key**: once, at the
start of a review session that is about to countersign, and never again after you
answer. Hardware keys are detectable because the key type is self-identifying
(`sk-ssh-ed25519@openssh.com`). Confirm-before-use is **not** detectable, because
the ssh-agent protocol does not expose per-identity constraints, so the user
declares it and we believe them. That warning is not polish. Shipping without it,
and without a real push toward confirm-before-use, hardware keys, or containers,
would make things worse while claiming to make them better.

What a bare-agent user still gets, so the picture is complete: publisher
authentication, portable and inheritable approvals, tamper-evidence in transit,
and rejection that survives rename. The one thing they do not get is protection
from their own agent.

### Revocation is coarse

SSH signatures carry no trusted timestamp. There is no field for it in
PROTOCOL.sshsig, so we cannot ask whether a signature was made *before* a key was
compromised. Removing a key from `allowed_signers` therefore invalidates **every
approval that key ever made**, and all of that content returns to pending for
re-review. There is no "revoke as of date T".

This is defensible. A compromised approval key means every approval it made is
genuinely suspect, and mass re-review is the correct response rather than a
regression. But it is a real operational cost. A transparency log (Sigstore /
Rekor) is precisely a system that adds trusted time, which is the strongest
argument for that migration later.

### The trust store stops being human-readable

`trust.yaml` can be read, diffed, and audited in a text editor. A directory of
armored signature blobs cannot. This is a genuine usability regression and it is
not free to fix, because any human-readable index we add is untrusted display
metadata that must never feed a decision, so we would be maintaining an artifact
that can drift from the truth. The mitigation is a real `ctxloom approvals list`
porcelain that renders from *verified* signatures, plus the discipline never to
let a cached index answer a security question.

### Inherited approvals are a broad grant

Trusting a signer for the approve namespace means everything they ever approve
reaches your agent unreviewed, forever. Sharing is a first-class goal and it is
genuinely valuable, but this has not made the grant *narrower* than
`trust_bundles` was. It has replaced trusting a location with trusting an
identity, which is better, and a careless or compromised team-lead key still
auto-approves content into every developer's agent. Keep reviewer keys
hardware-backed, scope them to the approve namespace so they cannot also publish,
and remember that any developer can still reject unilaterally. That is the release
valve.

## What it does not defend against

**Signed does not mean safe.** A signature says who, never whether the content is
good for you. This is why review is a separate and non-optional axis, and why
rejection outranks every signature including ctxloom's own.

**A malicious binary you have agreed to run.** If a hostile `taskloom` is on
your PATH *and you allowed it*, its hooks and MCP servers execute. Signing its
loadout changes nothing about that. Executing a binary is a strictly larger
grant than reading its context, and the loadout signature is about provenance,
not about containing a hostile companion.

What *is* defended is the step before: a binary merely being on `$PATH` under a
companion name no longer earns an exec. ctxloom asks once, records the answer
against the file's absolute path and SHA-256, and skips an unconfirmed companion
outright in any non-interactive session — so a name-squatting npm dependency in
`./node_modules/.bin` cannot get itself run by a session start. See
`docs/trust-model.md`, "Companion loadouts".

**An attacker with arbitrary write access to your home directory.** Such an
attacker can add their own key to `allowed_signers`, or replace the `ctxloom`
binary outright. Signatures do not fix a writable trust root. Signing raises the
bar from "write one YAML row" to "tamper with the trust root", and that is worth
something, but it is not a boundary against full host compromise. The boundary is
real for a *containerized* agent, which is the case ctxloom actually cares about.

**Content in your own project repo.** By design, as above. The control there is
code review.

**A publisher who signs and then goes bad.** Key trust is trust, and revocation is
coarse.

**Timing.** We cannot prove *when* an approval was made. Any `reviewed_at` we
store is untrusted metadata for humans and is never an input to a decision.

## Open questions

Genuinely undecided, listed rather than papered over:

- Key rotation. The spec covers revocation, which is coarse, but does not describe
  a rotation flow that preserves prior approvals. Given the missing timestamp,
  there may not be one short of re-signing everything.
- `ctxloom approvals list` is named as the mitigation for the readability
  regression, but its surface is not specified alongside the `signer` commands.
- `--no-sign` appears in the spec's "no key found" error text but is not in its
  CLI listing.
- Team CI that signs bundles. The signing pipeline is specified for
  ctxloom-default and for the companions, but not for an ordinary team publishing
  its own bundles as a remote.
