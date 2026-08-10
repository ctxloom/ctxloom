# ctxloom Trust Model

The canonical reference for how ctxloom decides what content reaches an agent.
Everything here is derived from the enforcement code; where behavior and older
doc-comments disagree, this document describes the behavior (open discrepancies
are listed under Known gaps).

> **Wire contracts and payload framing:**
> [signature-envelope.spec.md](signature-envelope.spec.md) — what bytes are signed,
> how the countersignature payload is framed, and the exact strings third parties
> bind to. That document explains *why these bytes*; this one is the normative
> account of what the system *does*. **Where the two disagree, this document wins.**

## The invariant

**A human sees third-party content — including every update to it — before the
LLM does.** First-party content is exempt: material you authored in this project
(`ctxloom:local`), builtin bundles shipped inside the binary, and content from a
**trusted publisher** — a bundle signed by a key you trust for the publish
namespace (`allowed_signers`). Every other remote item is born **pending** and is
withheld from the agent until a human reviews it.

Trust is keyed to the signing **identity**, not to the location the bytes arrived
from. A fork, a typosquatted host, a compromised forge, or a tampered clone
object cannot produce content that verifies under the key you actually trusted.
This replaces the old `trust_bundles` source-trust flag, which trusted a URL
hash-blind — a location can be substituted; a signature over the bytes cannot.

There is one trust layer, not two. The lockfile is pure dependency pinning —
which commit of a bundle is installed — and grants no exposure (ADR 0033: it is
not a security surface, and nothing here reads or writes it). Whether an
individual item ever reaches the agent is decided per item, at the **exposure
choke** — never at fetch or lock time. A pull of an unsigned, badly-signed, or
rejected bundle succeeds; its content is withheld when it would be exposed.

## Item states

Every remote item — fragment, command, MCP server, hook — is in exactly one of
three states:

- **pending** — never reviewed, or its content changed since a human approved
  it. Withheld from the agent. Pending is the implicit state of any item with
  no countersignature covering its current bytes.
- **approved** — a human **countersigned this exact content with their own SSH
  key**. The signature IS the approval record — there is no separate ledger row
  to forge. It binds to the item's raw and (when one exists) distilled bytes as
  independent signatures, each over the exact materialization; a change to
  either exposed form means that signature no longer verifies, and the item
  returns to pending.
- **rejected** — a human declined it, also by countersigning: a **ref-level**
  signature (sticky — survives the content changing under the ref) and a
  **content-reject** signature over the current bytes, deliberately signed with
  the ref *omitted* so a renamed or moved identical copy stays rejected
  wherever it appears. Rejection beats every allow, including the first-party
  exemption.

## The decision function

One resolver, `operations.EffectiveTrust`, owns every exposure decision. It is
fed the exact **bytes** about to be exposed (never a precomputed hash — a hash
can only be compared against a file anything can write; bytes can be *verified*),
the item's `Ref`, its `Form`, and its **verified publisher `Signer`**. First
match wins; it is fail-closed:

1. **rejected** — a rejection covers this ref, or covers exactly these bytes
   (the repo/ref-agnostic content denylist) → **DENY**.
2. **retracted** — the publisher themselves withdrew this bundle, learned from
   their remote manifest at the last sync and recorded locally → **DENY**.
   Retraction is a *peer* of rejection, not a kind of it: a rejection is a
   human's decision about bytes, a retraction is the publisher's own
   withdrawal, and it must beat every allow below — including the publisher's
   own trusted signature, since a publisher has to be able to retract content
   they signed. The check is a pure local lookup; the network probe already
   ran at sync time, and exposure-time evaluation never dials out.
   - **2a. retraction state unreadable** → **DENY**, for exactly the remote
     refs the record could have spoken about. "I cannot read the retraction
     record" is not "nothing is retracted", and collapsing the two re-exposes
     content a publisher deliberately withdrew. An *absent* record is not this
     case: a project with no pins legitimately has nothing retracted.
   - **2b. the sync-time network probe itself is FAIL-STALE, not fail-open.**
     `internal/remote.CheckRetracted`'s remote-manifest read is three-valued
     (`RetractionVerdict`: clean / retracted / unknown), not a bool — a fetch
     failure (the remote is unreachable, or — indistinguishably at that seam —
     it simply publishes no manifest, the ordinary case) reports *unknown*,
     never a silent "clean". `Puller.resolveRetraction` (the caller-side half)
     turns *unknown* into a decision: fall back to the last verdict this
     project itself recorded for the ref (`LockEntry.Retracted` +
     `RetractionCheckedAt`), never to "assume cleared" — so a network
     partition cannot resurrect content the publisher already retracted. A
     fallback verdict older than 14 days (`remote.RetractionStaleAfter`), or
     one with no recorded check time at all (an entry written before this
     field existed — unknown age, not implicitly fresh), warns via `clidiag`
     but is still honored, never discarded: staleness degrades toward *more*
     caution communicated to the operator, never toward more exposure. Closes
     U088-F01, U095-F02 (fetch-failure half — the parse-failure half was
     already fixed), and U150-F04.
3. **local** — the item was authored in this project (`ctxloom:local`), any kind
   including MCP servers and hooks → **ALLOW**.
4. **builtin** — the item is shipped inside the binary itself
   (`resources/builtin_bundles`, synthetic signer `builtin:ctxloom`) → **ALLOW**.
   Builtins are deliberately **not** signed — signing bytes embedded in the
   binary that verifies them is circular.
4b. **companion** — the item came from an installed companion binary's own
   loadout (`ctxloom:companion@<bin>`) → **ALLOW**. Local-equivalent, and the
   reason is *order of operations*, not deference: ctxloom reads a loadout by
   **executing** the companion (`<bin> loadout --format json`), so by the time
   the content exists that binary has already run arbitrary code as you.
   Reviewing the content afterwards buys ~nothing while costing a review prompt
   for a tool you deliberately installed. The control point that *does* have
   purchase is **exec**, and that is where the human decision lives — see
   "Companion loadouts" below. Like builtin, this is a distinct step below
   rejection specifically so step 1 can still reach it.
5. **trusted signer** — the item's bundle carries a non-empty verified publisher
   `Signer`: a key trusted for the publish namespace signed exactly these file
   bytes, and the signature verified at load, before any parse → **ALLOW**
   (updates included). This replaces the deleted hash-blind `trust_bundles`
   source bypass. The synthetic `builtin:ctxloom` identity is explicitly
   excluded here — a builtin is allowed *as a builtin* (step 4), never laundered
   into a "trusted publisher".
6. **approved** — a valid approve countersignature covers exactly these bytes,
   at this ref, in this form, from a key trusted for the approve namespace
   → **ALLOW**. Any change to the exposed bytes drops the approval to pending.
7. **otherwise** — pending: **DENY**, withheld until reviewed, counted toward the
   startup notice. This is where unsigned content lands, where signed-but-
   untrusted-key content lands, and where content whose bytes changed lands.

Retraction is a second DENY **reason**, not a fourth item **state**: an item is
pending, approved, or rejected, and a retracted item renders as rejected —
withheld permanently, awaiting nothing.

Before step 1 even runs, the resolver checks that both physical approvals
stores (the personal `~/.ctxloom/approvals` and the committable
`.ctxloom/approvals`) can actually be read. A store directory that has never
been created is **fine** — that is the ordinary "nothing reviewed yet" shape
of a fresh project or a fresh user — but a store that *exists* and cannot be
listed, contains a record file that cannot be opened (permission denied, a
filesystem-level I/O error), or contains a `.sig` record whose bytes will not
parse as a signature at all, is treated as a fault, not as empty: it might be
hiding a **rejection**, and silently reading it as "nothing rejected" would
reopen a gate a human closed. On that fault the resolver **denies every item**
— even one that would otherwise be allowed by the local or builtin exemption —
and records a fatal `trust-store`-class finding in strict mode, exactly as the
pre-signature hash-pair ledger did for an unreadable `trust.yaml`. The fix is
the same shape either failure has always had: `fix or remove the corrupted
approvals store, then re-review (ctxloom review)`.

**A signature authenticates; it never authorizes.** A validly-signed malicious
fragment is still malicious — signed does *not* mean safe. That is why review
(steps 1 and 6) is a separate axis and why rejection outranks every signature,
including ctxloom's own. There is no "signed" item state: `Signer` is an *input*
to the decision, never a state. An item is pending, approved, or rejected; a
signed item whose key you do not trust is not a fourth thing — it is pending.

Rejection is checked first so it beats every exemption: a user can reject an
item even from a trusted publisher or a **builtin**, and step 1 is evaluated even
when the publisher signature is absent or failed to verify (a rejection is of
*bytes*, not of provenance). This is enforced, not just
documented — builtin bundles are routed through the SAME decision function as
everything else (`trust.Ref{IsBuiltin: true}`, keyed under the synthetic
identity `builtin:ctxloom` so a builtin item can never collide with a
project-local bundle of the same name), and step 1's rejection check runs
before step 4's builtin exemption. A missing countersignature for the exact
form being exposed does not satisfy step 6 — the exact materialization being
exposed was never reviewed, so it stays pending. Finding a candidate
countersignature FILE at the right index is never enough on its own: it must
still cryptographically verify against the reconstructed payload and its
signer must be trusted for the relevant namespace, or it resolves pending —
never allow.

**Degradation is only safe in one direction, and the two directions are not
symmetric.** For the ALLOW steps (4-6) a malformed `allowed_signers` file, a
corrupted approve countersignature, or a deleted countersignature store all
degrade toward *fewer* trusted decisions — more content unsigned or
unreviewed, more review — never toward more exposure. For the DENY steps (1-2)
the identical degradation would run the other way: a rejection nobody can read
is a rejection nobody enforces, so a corrupted *reject* record would silently
un-reject the item, and for a bundle carrying a verified publisher signature
that is not "back to pending" but straight to **allow** at step 5.

That inversion is closed, not accepted. `Store.Verified` deliberately has no
error channel — it answers `("", false)` for an empty store, a corrupt file, a
malformed armor and an untrusted signer alike, and it must, because from
inside a single query it cannot tell "no such record" from "the record is
corrupt": it only ever sees the candidates whose index hash that one query
reconstructed. The distinction is a whole-store question, so it is answered by
the readability gate above, which runs before step 1 and **parses every record
in both stores**. A `.sig` that will not unarmor is a fault there, and the
resolver denies everything rather than guess which decision it just lost.
A signature that parses but does not verify is *not* a fault — that is the
ordinary "not proven" outcome, and treating it as one would deny every session
carrying a single stale record.

`builtin:ctxloom` is a plain identity string, not a cryptographic signature —
nothing about a builtin bundle is verified beyond "it shipped inside this
binary" (trusting the binary trusts what it ships, same as always). It exists
purely so builtin items are addressable and rejectable through the same
identity shape the store already uses for local (`ctxloom:local`) and remote
(canonical repo URL) items. It is explicitly rejected at step 5 so it can never
be mistaken for a cryptographically-verified publisher.

## First-party sources

Four source classes are exempt from review by default (but not from
rejection — see the decision function above):

- **Local** — items authored in this project, keyed to the `ctxloom:local`
  source. Locality is honest: a seeded or cloned bundle stamps its canonical
  remote ref, so a *copy* of remote content keys as remote and is **not**
  local-trusted. "You wrote it here, you trust it; a clone of it is not yours."
- **Builtin** — bundles compiled into the binary, keyed to the synthetic
  `builtin:ctxloom` identity. Allowed by default (step 4) with no review
  friction — but, unlike local content's step-3 placement, this is a distinct
  step specifically so a rejection (step 1) can still reach it.
- **Companion** — a loadout an installed companion binary advertised about
  itself, keyed to the fixed `ctxloom:companion` token. Exempt because reading
  it *required executing the binary first*; see "Companion loadouts" below for
  the full argument and for what is gated instead.
- **Trusted publisher** — a bundle whose file bytes were signed by a key you
  trust for the publish namespace, verified at load. Updates included: change
  the bytes and the publisher's signature no longer covers them, so the bundle
  re-verifies (or falls to pending) on the next load.

### Companion loadouts

A companion (`ltk`, `taskloom`, `reprise`, or any `ctxloom-companion-*` binary
on `$PATH`) tells ctxloom what it contributes by being **run**:
`<bin> loadout --format json`.

**The posture reversed here, deliberately.** This document and
`docs/signing-design.md` previously recorded that a companion loadout is
*"withheld, never crashes, never auto-allowed"* — gated "exactly like a remote
bundle". Only **"never crashes"** survives. Companion content is
**local-equivalent**: allowed at step 4b, never withheld for want of a
signature or a review.

The reason is order of operations. Gating the *content* of a loadout puts the
review prompt strictly **after** the arbitrary code execution it would be
protecting you from: the binary already ran, as you, before a single byte of
content existed. A prompt in that position buys ~nothing, and it costs friction
on content the user deliberately installed — which is how prompt fatigue trains
people to approve without reading, blunting the prompts that *do* matter.

So the decision moved to where it has purchase: **may ctxloom execute this
binary at all**.

**Discovery is trust-on-first-use.** Discovery itself is unchanged and
deliberately permissive — it lists every first-party name plus every
`ctxloom-companion-*` found by scanning `$PATH`, filtering nothing, because it
is a *candidate list*. The gate is at exec. The first time a given companion
would be run, ctxloom asks, and records the answer (the `ssh known_hosts`
pattern). A **non-interactive session — an agent, CI, `ctxloom mcp` over stdio,
any piped invocation — is never prompted**: the unconfirmed companion is skipped
with a warning naming the file and the way to allow it. Fail-closed, matching
how the probe already degrades on failure.

This closes a real hole. `./node_modules/.bin` is on `$PATH` in a large share of
JavaScript projects, and an npm package — including a transitive dependency
nobody chose — can ship a binary under any name. Shipping
`ctxloom-companion-anything` previously earned an exec at the next session
start with no user action at all. That attacker does not control `$PATH`; they
name-squatted an auto-exec convention in a directory already on it. Every *other*
consumer of `node_modules/.bin` requires a human to type the command.

**The record is keyed on the resolved absolute path AND the binary's SHA-256.**
Path alone would let a replace-in-place swap inherit an existing approval; name
alone would let a binary earlier in `$PATH` inherit an approval granted to a
completely different file. An **approval** requires an exact `(path, sha256)`
match — any byte change at an approved path re-prompts. A **refusal** matches on
path alone, so "never run this" survives the binary being rebuilt.

**First-party companions are exempt, but pinned by location.** `ltk`, `taskloom`
and `reprise` are automatic *only* when they resolve from the directory the
running `ctxloom` binary itself lives in — the location every install shape puts
them in together (`just install` → `~/go/bin`, a Homebrew prefix, the
devcontainer image, `$GOBIN`). That keeps routine rebuilds silent, which is the
whole point of the exemption. A first-party **name** found anywhere else is a
third-party binary that picked a familiar name, and goes through the prompt like
any other: the name list is three guessable strings discovered unconditionally,
so a name-only exemption would be the same hole in a smaller costume.

**A loadout's signature is a diagnostic, not a gate.** This is the second place
the companion class parts company with remote content, and it follows from the
same fact. A publisher signature exists to protect bytes from an
**intermediary** — a forge, a network, a tampered clone object. A loadout has no
intermediary: its bytes come straight off the stdout of a binary the user
already consented to execute. So a companion loadout is admitted whatever its
signature says, and the signature facts are **reported** instead:

- **No signature** → admitted, silently. Ordinary.
- **Signature present, does not verify over the bytes** → **admitted, with a
  warning**, and the content is delivered unattributed. This is a *bug* signal,
  not an attack signal: it almost always means the companion's release shipped a
  stale or mismatched signature, and the fix belongs to the companion's authors.
  Calling it tampering would be both wrong and useless. (A **remote** bundle
  keeps the opposite posture — an invalid signature there is tamper and
  withholds — because its bytes crossed exactly the intermediary a loadout's do
  not.)
- **Signature valid, signer not trusted for publish** → admitted, with a
  warning, unattributed. The key's trust status is a fact about the key, not a
  gate on local content.

The control that actually catches a **swapped companion binary** is the
hash-keyed exec consent above, which is the right place for it: it fires before
the binary runs, rather than after it has already executed.

**What does not change.** Rejection still reaches companion content (step 1,
above the exemption). An unreadable approvals store still denies it along with
everything else. An absent, wedged, timed-out or **structurally** unusable
companion (unparseable envelope, unrecognized contract, empty or non-base64
bundle, unparseable bundle YAML) is still skipped with a warning — never fatal,
never a stalled startup. Those cases produced no content to admit in the first
place, which is what distinguishes them from a signature that merely failed to
verify. Nothing is dropped silently: reporting replaces filtering throughout.

**Where the record lives.** `~/.ctxloom/companion_consent.yaml`, personal only,
mode `0600`. There is deliberately **no committable project counterpart**, unlike
the approvals store: an approval answers "may this content be shown to the
agent", which a team can legitimately decide once and share, whereas this answers
"may ctxloom execute this file on **this machine**". A committable form would let
a repo you cloned arrive carrying pre-approved binaries. Its only authority is
filesystem permissions — the same standing the unsigned approval markers have —
which is another reason it never leaves your home directory.

Inspect and change it with `ctxloom companion list | allow <path> |
forget <path>`. `allow` is also the scriptable escape hatch for CI, and it
requires a human to type it rather than inferring consent from an environment.

### Trusted publishers

Trust is a property of a **signing key**, not of a remote. The trust root is a
union of `allowed_signers` files (OpenSSH format, verbatim): ctxloom's embedded
defaults, `~/.ctxloom/allowed_signers` (user), and `.ctxloom/allowed_signers`
(committable project store). All are unioned; precedence lives in the decision
function, never in the filesystem. A publisher signature is over the raw bundle
**file** bytes and is carried as a detached sibling `<bundle>.yaml.sig` in the
same git tree at the same pinned SHA — verified before any YAML parse.

The `namespaces="…"` option in `allowed_signers` **is** the role system: a key
trusted only to publish cannot approve content, and vice versa. A signature by a
key that is not in your trust root, or that is scoped to the wrong namespace, is
simply **unsigned content to you** — quiet, no error, it takes the review path.
A signature that is present but does **not** verify over the bytes it sits beside
(a trusted key over different bytes, or a corrupted blob) is **tamper**: every
item in that bundle is withheld, never degraded to unsigned, and never offered
for review — approving bytes an attacker got demoted from "signed" to "merely
reviewable" is the whole point of corrupting a `.sig`. A human's earlier
approval of those exact bytes does not lift it either; an approval covers bytes,
not a signature.

The bundle itself is still **read**, and reported. A reader that dropped it would
leave the user with content that is missing and no way to find out why; what
withholds it is the trust filter, which can name the reason (`tampered`) on the
same verdict the delivery path decided with. This paragraph is about **remote**
content, whose bytes crossed an intermediary. Companion loadouts — which cross
none — are the documented exception; see "Companion loadouts".

Third-party unsigned remotes default to pending; their content is reviewed like
anything else. Signer keys are managed with `ctxloom signer trust|list|show|remove`
and signatures are produced with `ctxloom bundle sign`; a hand-edited `allowed_signers`
file is still read verbatim, so editing it by hand remains equivalent.
Signing/verification is CLI-only and is
**never** exposed over MCP — handing the agent a `signer add` capability would
defeat the property this design exists to provide.

> **Removed:** the `trust_bundles: true` remote flag, the trusted-sources set,
> and `ctxloom remote trust|untrust`. Adding a remote (an address) and trusting a
> publisher (a key) are now separate acts. `init --remote` no longer flips a
> trust flag — a personal repo's content takes the review path until you sign it
> and trust your own key.

## The review ceremony

`ctxloom review` is the single porcelain. It walks every pending item, grouped by
bundle, and records a decision each by **countersigning the exact reviewed
bytes** with the reviewer's own SSH key:

- **New** items show their full content. **Updated** items (a ref you previously
  approved whose content has since changed) show a unified diff against the
  snapshot of the approved version — falling back to full content when no
  snapshot exists.
- MCP servers and hooks display as **what they run** — command, args, env,
  matcher, install — the exact executable surface the approval countersignature
  covers.
- Per item: **[a]ccept**, **[r]eject**, **[s]kip**; per bundle: **[A]** accept
  all remaining. Accepting countersigns the raw bytes always, and the distilled
  bytes too when a distilled form exists, then snapshots the approved bytes;
  rejecting countersigns the ref block plus a content-reject over the current
  bytes. Viewing never mutates — only an explicit letter acts.
- The countersigning key is resolved once per session, before the first item is
  shown, via the same zero-config discovery chain `ctxloom bundle sign` uses
  (`internal/signing/agentkey`): `git config user.signingkey` first, then the
  sole identity held by `ssh-agent` (`SSH_AUTH_SOCK`) when there is exactly
  one. (`--key` and the `sign.key` config default, which `ctxloom bundle sign` also
  honors, are not yet exposed on `ctxloom review` itself.) If the key is a
  plain software key
  (not `sk-ssh-ed25519@openssh.com` / `sk-ecdsa-sha2-nistp256@openssh.com`), the
  session warns **once**: any process holding `SSH_AUTH_SOCK` — including an
  agent ctxloom just launched — can ask that agent to sign approvals as you,
  unless the key is confirm-guarded (`ssh-add -c`) or hardware-backed. It is a
  warning, never a block.
- **No key available** degrades to an explicit, confirmed **UNSIGNED** path:
  decisions are recorded as bare markers in the personal store only — exactly
  as forgeable as the deleted `trust.yaml` design, and never written to the
  committable project store.
- `--project` writes to the committable project store instead of the personal
  one, for a team lead or CI to countersign once and have every developer
  inherit it (via the project's `allowed_signers`). It **requires** a signing
  key — there is no unsigned fallback for a shared store.
- Off a TTY, or with `--list`, it prints the pending table (bundle, ref, kind,
  new|update) and exits, so scripts and agents can see what a human still owes a
  look.
- `init`'s interview ends with a review session when anything is pending.

`ctxloom bundle trust <ref>` and `ctxloom bundle untrust <ref>` are the scriptable plumbing
beneath the porcelain — they write the same countersignatures through the same
mutation path, so the porcelain and the plumbing produce identical on-disk
results.

**The namespace check runs on the WRITE side too, and refuses.** Recording a
decision resolves the countersigning key and then asks the *same* trust root
the gate will ask whether that key is authorized for the *same* namespace the
decision will be asserted in (`operations.resolveDecisionSigner` →
`requireTrustedForAssertion`, deriving the namespace through
`signing.NamespaceForAssertion` exactly as `VerifyCountersignature` does). A
key with no approve grant cannot record an approval, and a key with no reject
grant cannot record a rejection: the command fails, naming the key, the
namespace it lacks, and the `ctxloom signer trust … --namespace
approve,reject` that grants it, and **nothing is written**.

This closes a silent no-op (taskloom `tiny-bankbook`). The gate honours a
signed decision only when its signer is trusted for that namespace, so a
decision recorded by an ordinary ssh-agent key nobody had granted anything
produced a well-formed record, a success line naming the key, exit 0 — and an
item that stayed withheld with nothing saying why. The two sides now derive the
namespace from one function and read one root, so they cannot disagree about
which grant a decision needs.

Degradation here is fail-closed in every arm: no trust root, an unreadable or
malformed `allowed_signers`, and an assertion outside the closed
approve/reject vocabulary all **refuse**. Failing to establish that a key may
decide is never a reason to record the decision. The **unsigned** degraded path
(§9.5) is deliberately outside this gate — it has no key, so there is no
namespace question to ask, and its markers are honoured by their own
trust-root-free lookup in the personal store.

## Countersignature gating

An approval is a **countersignature over the exact bytes** the gate is about to
expose — the raw form always, and the distilled form too when one exists, as
two independent signatures. There is no author-supplied field to trust: the
signed payload is built directly from the resolved content
(`bundles.ContentPayload`), never from anything a bundle author wrote. At every
exposure the gate reconstructs the exact same payload it is about to expose,
under the current effective form (distilled vs raw, per `config.use_distilled`),
and asks whether ANY countersignature verifies over exactly those bytes, from a
key trusted for the approve namespace. Any edit to the exposed form produces
different signed bytes, so no prior signature verifies and the item re-gates to
pending. Profile-variable templating cannot smuggle content past the gate,
because the signed payload is the pre-substitution bytes.

A content hash still exists, but only as an **index** — the filename prefix
under which a candidate countersignature file is found, so a lookup is a
directory glob rather than a scan of every stored signature. Finding a
candidate proves nothing on its own; only a successful cryptographic verify
counts (spec §9.3). A hand-crafted file at the right index, or a corrupted
signature body, resolves pending — never allow.

## Storage

| Location | Contents |
|------|----------|
| `~/.ctxloom/approvals/` | The **personal countersignature store**. One armored `.sig` file per approve/reject countersignature (filename `<index-hash>.<assertion>.<key-tag>.sig`, an INDEX only — never trusted as authority) plus a display-only `index.yaml` sidecar (untrusted, never a decision input). Never committed. The default write target of `ctxloom review`. |
| `.ctxloom/approvals/` | The **project (committable) countersignature store**, same shape as the personal one. `ctxloom review --project` writes here; a team/CI inherits a lead's decisions via the project's `allowed_signers`. |
| `.ctxloom/allowed_signers` (+ `~/.ctxloom/allowed_signers`, + embedded) | The **trust root**: publisher/approver keys in OpenSSH `allowed_signers` format, verbatim. Unioned across all three locations; the `namespaces="…"` option is the role system. Committable. |
| `<bundle>.yaml.sig` | Detached publisher signature, a sibling of each signed bundle in the same git tree at the same pinned SHA. Verified over the raw file bytes before parse. A missing `.sig` = unsigned. |
| `~/.ctxloom/companion_consent.yaml` | The **companion exec-consent record**: one decision per companion binary, keyed on resolved absolute path + SHA-256. Mode `0600`, personal only, **no committable twin** — it answers "may ctxloom run this file on this machine", which no repo may answer for you. Plain data, not a signature; its authority is filesystem permissions. Managed with `ctxloom companion list\|allow\|forget`. |
| `~/.ctxloom/publish_remotes.yaml` | The **publish-destination record**: one decision per remote you have confirmed as a destination for your signed content, keyed on the remote's normalized identity (so two spellings of one repository are one destination). Same shape, same store and same properties as the companion record above — mode `0600`, personal only, no committable twin, plain data whose authority is filesystem permissions. Managed with `ctxloom remote trusted\|allow\|forget`; `allow` is how a CI job or an agent host records a confirmation it has no terminal to be asked for. |
| `.ctxloom/remotes.yaml` | remotes (address + custom forges only — **no** trust flag) |
| `.ctxloom/lock.yaml` | dependency pins only: `map[canonicalRef]{sha, url, requested_version, kind, pinned, ...}` |
| `cache/trust/objects/` | content-addressed snapshots of approved bytes, keyed by a payload hash — the diff base for update review. Pure cache: deleting it only degrades update review to a full-content display. |

The decision function's approval/rejection steps (`operations.EffectiveTrust`
steps 1 and 6) read through the `ReviewRecords` seam, which takes the exposed
**bytes**, not a hash — exactly the shape a signature verification needs. The
countersignature stores are its only implementation; the hash-pair `trust.yaml`
ledger this seam was built to replace has been deleted outright — pre1 never
shipped, so there is no migration and no compatibility shim.

**Composition — reads are the UNION of both stores, with no precedence between
them.** A signature is a signature no matter which store holds it; precedence
lives entirely in the decision function's step order (rejection is step 1,
approval is step 6), so a personal rejection in the user store beats an
inherited approval sitting in the project store, and a personal approval
likewise cannot override an inherited project-level rejection — rejection wins
from EITHER store, always.

## Enforcement points

The decision is enforced at distinct chokes. A DENY is **fail-closed and silent
to the agent**: the item is simply absent. The human gets one aggregate,
content-free stderr advisory — `N item(s) awaiting review — run 'ctxloom
review'` — and nothing about withheld content is ever injected into agent
context.

| Choke | Covers | On deny |
|-------|--------|---------|
| Content gate | fragments, commands (text) — including builtin fragments | absent from assembled context |
| Executable gate — MCP | bundle MCP servers — including builtin servers | omitted from backend settings |
| Executable gate — hooks | bundle hooks — including builtin hooks | omitted from backend settings |
| Executable gate — command export | command slash-commands | not exported |
| Tooling collection (`CollectTooling`) | `tooling` declarations | withheld from Containerfile proposals |
| Listing stamp (`TrustStamper`) | JSON listings | stamped `trusted: false` + source |

There is one choke *above* all of these, and it is not a content decision at
all: **companion exec consent**. A companion whose execution nobody confirmed is
never run, so its content never exists to gate. See "Companion loadouts".

Builtin content passes through every one of these chokes exactly like
remote/local content — it is simply allowed by default at the decision
function's builtin step (see above) rather than needing review. The chokes
that resolve builtins on a caller-supplied gate (`c.execGate` for MCP/hooks,
the exposure loader's content gate for fragments) stay ungated on
management/listing paths, matching the existing convention for every other
item kind — that path never gates ANY item, builtin or not.

**Ungated by design:**

- **Profiles** — a profile definition is orchestration, never gated; its
  constituent items still gate at their own chokes.

## Lifecycle

- **Steady-state sync** installs exactly the pinned set. It stages nothing and
  exposes nothing on its own — items land in whatever state their content hash
  resolves to.
- **`remote pull`** fetches exactly what the lock already pins; it never advances
  a SHA and never rewrites the manifest.
- **`remote upgrade`** re-resolves each dependency to the newest commit its
  manifest constraint allows and writes the advance **straight to the active
  lock** — no review gate at the lock layer, held (`pinned`) entries never
  advance, a hash conflict aborts with nothing written. Any changed content then
  re-hashes to pending and is withheld by the content gate until `ctxloom review`
  accepts it.
- **`bundle hold` / `unhold`** freeze or release a dependency at its locked SHA
  (aliases `pin` / `unpin`); a held entry never advances under `upgrade`. This is
  dependency management, not trust.
- **Review** is the only exposure gate: `ctxloom review` (or the `trust` /
  `blacklist` plumbing). Recording a decision immediately re-applies the managed
  artifacts, so a newly-accepted MCP/hook appears — and a rejected one is
  scrubbed — without waiting for the next run.

## Identity

Items key as `{canonical repo URL} + {bundle}#{kind}/{name}` with no version;
hashes carry the version dimension. Local items key under the fixed
`ctxloom:local` token in place of a repo URL; builtin items key under the fixed
`builtin:ctxloom` token; companion items key under the fixed
`ctxloom:companion` token with the binary's name as the bundle component — all
three are sentinels, so none of these classes can collide with a real remote
repo URL, or with each other. Repo URLs are
canonicalized (scheme, `.git`,
`git@`, host case; path case only on case-folding forges) on both sides of every
comparison, so a URL-spelling variant cannot escape a rejection or manufacture a
match. A moved or renamed item keeps neither its approved state (new ref →
re-gates to pending, safe) nor its ref-level rejection — the content-reject
countersignature compensates when the content form matches, because it is
deliberately signed with the ref omitted. Hook identity is positional
(`{event}/{index}`), so reordering a bundle's hooks shifts later hooks'
identities (acceptances re-gate: safe, but see Known gaps).

## Threat model

Addressed:

- **Malicious bundle update** — changed remote content no longer verifies
  against any prior approval and re-gates to pending; review shows the diff
  against what was approved before it is exposed.
- **Prompt injection via shared text** — cloned fragments and commands gate exactly
  like executables (a fragment is instructions to an LLM); the countersignature
  covers the pre-substitution bytes.
- **Arbitrary execution via MCP/hooks** — per-item gating at the exec chokes,
  countersigned over the full executable surface (command, args, env, matcher,
  type).
- **URL-variant / typosquat escape of a rejection** — canonical repo URLs on both
  comparison sides; the content-reject countersignature is repo- and
  ref-agnostic (signed with the ref omitted). Canonicalization is total over
  same-repo spellings: scheme case and `http`/`https`, a `www.` prefix on known
  forges, userinfo, query, fragment, trailing slashes and a `.git` suffix in
  either order all collapse to one key, because the store address is
  `CanonicalURL()+"|"+Key()` and any divergence is not a near miss but a store
  miss.
- **Corrupted rejection records** — a `.sig` in either approvals store that will
  not parse trips the readability gate ahead of step 1, and the resolver denies
  everything. Without that, an unreadable rejection would be an unenforced one.
- **curl-pipe-sh via tooling declarations** — tooling collection is trust-gated;
  nothing is applied without per-item human countersignature.
- **`$PATH` name-squatting into an auto-exec** — a binary named
  `ctxloom-companion-*` (or one of the three first-party names) dropped into a
  directory already on `$PATH`, `./node_modules/.bin` being the realistic case,
  used to be executed at the next session start with no user action. It is now
  trust-on-first-use, keyed on absolute path + SHA-256, and skipped outright in
  any non-interactive session. The first-party name exemption is pinned to
  ctxloom's own install directory so it cannot be claimed by a shadowing
  binary. See "Companion loadouts".
- **Content-form-flip escape** — closed by requiring an independent
  countersignature over EACH exposed form; a raw approval never validates a
  distilled exposure or vice versa.
- **Publisher impersonation / supply-chain substitution** — content claiming to
  come from a trusted publisher but not signed by its key is unsigned content
  (review path); a fork, typosquat, MITM'd fetch, or tampered clone object cannot
  produce bytes that verify under the trusted key. A trusted key's signature that
  does not cover the bytes it sits beside is treated as **tamper** and the bundle
  is withheld, so corrupting a `.sig` cannot downgrade a signed bundle to an
  unsigned one.
- **Forged approvals via a writable `.ctxloom/`** — an agent (or anything else)
  that can write files can no longer manufacture an approval by editing a
  ledger row: a countersignature it cannot produce (no key, no `SSH_AUTH_SOCK`
  in a containerized run) is not an approval, full stop. This holds under the
  preconditions in the signature-envelope spec §9.4 — it does **not** hold
  against a host-run agent with a bare `ssh-agent` (see below).

Explicitly **not** addressed by signing: **signed ≠ safe** (a signature says
*who*, never *whether it is good for you* — the release key can sign a malicious
fragment, which is why review and rejection are separate axes); a **writable
trust root** (an attacker who can append to `allowed_signers` can name their own
key); and a **host agent holding `SSH_AUTH_SOCK`** (approvals are off-by-default
against your own agent unless you use `ssh-add -c`, a hardware key, or a
container — see the signature-envelope spec §9). The **unsigned degraded path**
(no key available) is exactly as forgeable as the deleted `trust.yaml` — it is a
labelled, confirmed opt-in for users who have no key, never the default, and
never permitted in the committable project store.

## Known gaps and accepted risks

1. **Content-form-specific content-reject.** A rejection content-rejects the
   raw and distilled forms present at rejection time. If a moved copy is later
   exposed in a *different* form than was signed against (and its ref differs,
   so the sticky ref block does not apply), the copy can escape the content
   component in that form. The ref-level rejection still catches the same ref.
   Unchanged by countersigning — inherited unmodified from the deleted hash
   denylist (spec §5.3).
2. **Positional hook identity.** `{event}/{index}` keying means inserting or
   reordering hooks shifts identities; a sticky ref block can land on a different
   hook than the one rejected. The content-reject countersignature still catches
   identical content. Content-derived hook IDs would be more robust.
3. **Inherited trust is broad.** Trusting a publisher key exposes everything that
   key ever signs — text *and* executables, all future updates — without per-item
   review. This is narrower than the deleted `trust_bundles` (an identity cannot
   be forked or typosquatted the way a URL could) but it is not *narrow*: a
   careless or compromised key auto-exposes into your agent. Mitigations: scope
   keys with `namespaces="…"`, keep reviewer keys hardware-backed, and remember
   rejection is supreme (a developer can always reject unilaterally). The same
   applies to an inherited approve key: trusting `lead@team.example` for approve
   inherits every approval they ever countersign.
4. **`$PAGER` during review** is user-controlled code execution at review time;
   acknowledged in code as an accepted, OS-conventional risk.
5. **Countersignature posture warning is per-session, not persisted.** Spec
   §9.1.2 describes a persisted `approvals.posture` acknowledgment so the
   software-key warning fires once ever; the current implementation fires once
   per `ctxloom review` invocation instead (never blocking). Tracked as deferred
   work.
6. **`ctxloom review` does not expose `--key` / `sign.key` config.** The
   discovery chain itself is unified: `ctxloom review` and `ctxloom bundle sign` both
   resolve through the same `internal/signing/agentkey.Discoverer` (git
   `user.signingkey` → sole `ssh-agent` identity). What `ctxloom review` does
   not do is pass an explicit key into that chain, so — unlike `ctxloom bundle sign`
   — an operator cannot override discovery with `--key` or the `sign.key`
   config default on `review` itself; only git config and ssh-agent are
   consulted. Narrowing this remaining gap means threading an explicit-key
   override through `resolveReviewSigner`.
7. **No filesystem load path verifies a publisher signature.** Publisher
   verification is wired into exactly two load paths — the remote-git seed
   (`config.loadRemoteBundleSeed`) and the companion loadout — the two places
   `Bundle.StampSigner` is called. A signed `(x.yaml, x.yaml.sig)` pair placed in
   a directory ctxloom reads is therefore *not* verified: it is either first-party
   local content (allowed unverified) or carries no signer. The consequence is
   that the **organization drop-in / MDM flow of spec §4.4 and §7A.6 does not
   work** — an org cannot yet ship signed context through a channel other than a
   git remote. This fails safe (an unverified bundle is unsigned, and unsigned
   content is reviewed), so it is a missing feature, not a hole.
8. **ctxloom's own embedded key cannot be untrusted.** The compiled-in trust root
   is unconditionally unioned into every lookup (`config.TrustRoot`), and
   `operations.RemoveSigner` only rewrites the user/project *file*. There is no
   negative-entry mechanism, so `ctxloom signer untrust ben+ctxloom@abbitt.me` does
   not stop ctxloom-published bundles being auto-trusted. Spec §7 says the embedded
   defaults are removable; they are not. A user who wants to review ctxloom's own
   content by hand currently has no supported way to ask for that.
9. **One key signs every surface.** Spec §6 calls for three keys with three
   compromise radii (release binaries / bundle content / per-companion loadouts).
   In fact a **single** embedded publish key signs both the ctxloom-default bundles
   and both companion loadouts, and `.goreleaser.yml` carries no `signs:` block at
   all — **release artifacts are unsigned**. The compromise radius of that one key
   is therefore every signed surface at once.
10. **An agent can write to the repository it works in — and `.git` is code, not
    just data.** ctxloom runs an agent in its own git worktree, and the git
    *common* directory is exposed to that agent read-write: in a container it is
    bind-mounted at its identical host path (`gitCommonDirMount` in
    `internal/lm/isolation/container.go` → `rt.ExposeIdentical(common, false)`,
    and the project mount itself is `ExposeIdentical(cw.dir, false)`), and on the
    host runtime nothing is in the way at all. That directory holds `hooks/` and
    the repo-local `config`, whose `core.hooksPath`, `core.fsmonitor`,
    `core.sshCommand`, `core.pager` and `[alias]` keys all name commands git
    executes. An agent that replaces `hooks/pre-commit` has planted a program
    that runs the next time a **human** commits in the primary checkout — on the
    host, outside the container, as that user. This repository carries live
    `pre-commit` and `prepare-commit-msg` hooks in that directory today, so the
    file is real, not a `.sample`. The accurate statement of the residual is
    therefore not "code and git state are corruptable": under accident it is a
    spoiled branch, and under malice it is **host code execution**. Which control
    owns which question: the **review/trust pipeline at ingest** (fragment and
    skill review, publisher signatures, countersignatures) is the control against
    a malicious *instruction* reaching an agent; the **container at runtime**
    isolates the process and the host filesystem; the **worktree and branch**
    bound the blast radius of *accident* and ordinary agent error, and are not a
    control against malice; the **read-write git mount** is not a control at all
    — it is this accepted residual. An agent handed a working repository can
    write to it; that is inherent to the job, not a defect ctxloom intends to
    close. The mitigation is upstream (point agents at repositories you would be
    willing to restore from their remote; inspect `.git/hooks` and `.git/config`
    after a run you have reason to doubt), and the risk sits with the user. Ruled
    and accepted 2026-07-31; ratifies the in-code "Over-mount blast radius,
    ACCEPTED" decision.
11. **The review gate covers what ctxloom delivers, not every ingress.** Review,
    signing and countersigning cover content ctxloom itself resolves and hands to
    an agent: fragments, skills, bundles, hooks, MCP declarations. They do not
    cover a poisoned file already committed in the repository the agent works in,
    web content the agent fetches, the contents of an upstream dependency, or an
    injection carried in data the agent merely processes. None of those pass
    through the gate, because ctxloom never resolved them. Stated explicitly
    because gap 10 leans on review as the upstream control: it is strong on one
    ingress, not on all of them.
12. **Repo-local git config selects the signing identity.** Step 2 of the
    zero-config chain (`internal/signing/agentkey`) resolves
    `git config --get user.signingkey`, and `execGitConfig` runs with `cmd.Dir`
    set to the working repository — so git answers from that repository's own
    `.git/config`, a file that arrives with a clone. A cloned repository can
    therefore influence **which of your controlled keys signs**. It cannot cause
    an attestation from a key you do not hold: every signer is a live `ssh-agent`
    identity, and the attacker's key is not in your agent. A ctxloom signature is
    an attestation from a controlled key/identity, and so is a git signature —
    `git commit -S` resolves `user.signingkey` from the same file and asserts the
    same kind of property — so inheriting git's resolution boundary is principled
    rather than an appeal to precedent. The residual is git's residual: the
    attestation remains from an identity you control, but potentially a
    *different* controlled identity than intended (a personal key where a work
    key was meant). That is attribution, not a break of the attestation property.
    Ruled and accepted 2026-07-31; pinned by
    `TestGitSigningKey_RepoLocalConfigNamesTheFileRead`. Rejected alternatives,
    recorded so they are not re-litigated: restricting the lookup to
    global/system config (breaks per-repository identities, a legitimate setup,
    and guts the documented zero-config chain), and requiring confirmation when
    the value came from repo-local config (adds a consent surface to a flow that
    most often runs unattended in CI). The related unbounded-read hazard — a
    named path such as `/dev/zero` that never reaches EOF — is closed: reads are
    capped at `maxPublicKeyBytes` (64 KiB). No content is exfiltrated by the
    error path; `ssh.ParseAuthorizedKey`'s error names the path, never the bytes.
