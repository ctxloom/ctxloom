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

Three source classes are exempt from review by default (but not from
rejection — see the decision function above):

- **Local** — items authored in this project, keyed to the `ctxloom:local`
  source. Locality is honest: a seeded or cloned bundle stamps its canonical
  remote ref, so a *copy* of remote content keys as remote and is **not**
  local-trusted. "You wrote it here, you trust it; a clone of it is not yours."
- **Builtin** — bundles compiled into the binary, keyed to the synthetic
  `builtin:ctxloom` identity. Allowed by default (step 4) with no review
  friction — but, unlike local content's step-3 placement, this is a distinct
  step specifically so a rejection (step 1) can still reach it.
- **Trusted publisher** — a bundle whose file bytes were signed by a key you
  trust for the publish namespace, verified at load. Updates included: change
  the bytes and the publisher's signature no longer covers them, so the bundle
  re-verifies (or falls to pending) on the next load.

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
(a trusted key over different bytes, or a corrupted blob) is **tamper**: the
bundle is withheld entirely, never degraded to unsigned.

Third-party unsigned remotes default to pending; their content is reviewed like
anything else. Signer keys are managed with `ctxloom signer add|list|show|remove`
and signatures are produced with `ctxloom sign`; a hand-edited `allowed_signers`
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
  shown, via the same zero-config discovery chain `ctxloom sign` uses
  (`internal/signing/agentkey`): `git config user.signingkey` first, then the
  sole identity held by `ssh-agent` (`SSH_AUTH_SOCK`) when there is exactly
  one. (`--key` and the `sign.key` config default, which `ctxloom sign` also
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

`ctxloom trust <ref>` and `ctxloom blacklist <ref>` are the scriptable plumbing
beneath the porcelain — they write the same countersignatures through the same
mutation path, so the porcelain and the plumbing produce identical on-disk
results.

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
`builtin:ctxloom` token — both sentinels so local and builtin items can never
collide with a real remote repo URL, or with each other. Repo URLs are
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
   discovery chain itself is unified: `ctxloom review` and `ctxloom sign` both
   resolve through the same `internal/signing/agentkey.Discoverer` (git
   `user.signingkey` → sole `ssh-agent` identity). What `ctxloom review` does
   not do is pass an explicit key into that chain, so — unlike `ctxloom sign`
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
   negative-entry mechanism, so `ctxloom signer remove ben+ctxloom@abbitt.me` does
   not stop ctxloom-published bundles being auto-trusted. Spec §7 says the embedded
   defaults are removable; they are not. A user who wants to review ctxloom's own
   content by hand currently has no supported way to ask for that.
9. **One key signs every surface.** Spec §6 calls for three keys with three
   compromise radii (release binaries / bundle content / per-companion loadouts).
   In fact a **single** embedded publish key signs both the ctxloom-default bundles
   and both companion loadouts, and `.goreleaser.yml` carries no `signs:` block at
   all — **release artifacts are unsigned**. The compromise radius of that one key
   is therefore every signed surface at once.
