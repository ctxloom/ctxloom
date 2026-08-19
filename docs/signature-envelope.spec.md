# ctxloom Signature Envelope — Specification

**Status:** **Accepted — normative.** Landed 2026-07-13.
**Contract version:** `1` (see §12 — this is a public contract; third parties depend on it).
**Base:** v0.7.0-pre1. The design has shipped in substantial part: `ctxloom sign`
and `ctxloom signer` exist, publishing writes exact bytes, authored content lives
under `.ctxloom/content/`, `ctxloom bundle move` relocates a bundle with its
signature, and ctxloom-default's 43 bundles are signed and pushed.
**Companion to [trust-model.md](trust-model.md)**, which is the normative account
of what the system *does*. This document is the account of *why these exact bytes*,
and it is where the wire contracts third parties bind to are pinned. Where the two
disagree, **trust-model.md wins** — it is derived from the enforcement code.
**Migration burden:** none. pre1 has never been released; there were zero users of
the trust model this replaced. This spec takes the clean design, not the compatible one.

---

## 0. How to read this document — what ships and what does not

This spec was written as a design document *before* the implementation, and it was
published only after the code was audited against it. **The audit found the code
faithful and this document, in places, over-claiming.** Those places are now marked
inline, and they are collected here so that no reader has to discover them by
building against a promise.

Every claim in this document is one of two things, and it says which:

- **Unmarked = shipped.** The behavior is in the tree and is enforced by tests.
- **Marked `> **PLANNED — not yet implemented.**`** — design intent, retained
  because it is the direction, but **not true of the code today.** Do not build
  against it, do not cite it as behavior, and do not let it into user-facing docs
  as fact.

The planned-not-implemented set, in full, as of 2026-07-13:

| § | Claim | Actual state |
|---|---|---|
| §4.4, §7A.6 | Org drop-in channel: a signed `(x.yaml, x.yaml.sig)` pair dropped into a directory ctxloom reads is verified | **No filesystem load path verifies a `.sig`.** Verification is wired for the remote-git seed and the companion loadout only. A local pair is treated as first-party (allowed unverified); a dropped-in one gets no signer. |
| §6 | Three keys, three pipelines (release / bundle / per-companion) | **One key.** `internal/config/embedded_signers.allowed_signers` ships a single publish key, which signs both the ctxloom-default bundles and both companion loadouts. `.goreleaser.yml` has **no `signs:` block** — release artifacts are unsigned. |
| §7 | The embedded defaults are removable (`signer remove` writes a negative entry) | **Not removable.** The embedded trust root is compiled in and unconditionally unioned (`config.TrustRoot`). `operations.RemoveSigner` rewrites only the user/project *file*; there is no negative-entry mechanism, so ctxloom's own key cannot be untrusted. |
| §7A.4 | `--key` / `sign.key` honored on `review` | **Honored by `sign`, not by `review`.** `resolveReviewSigner` passes an empty explicit key into the discovery chain, so `review` resolves via git config → ssh-agent only. (trust-model.md Known gap 6.) |
| §9.1.2 | Persisted posture acknowledgment (`approvals.posture`, the `[c]/[p]/[q]` prompt, "warn once ever") | **Warns once per `review` invocation.** There is no `approvals.posture` config key and no three-way prompt. (trust-model.md Known gap 5.) |

Two further corrections the audit forced, where **the code was right and this
document was wrong**. Both are now fixed in place, and both are called out because
shipping the original text would have invited someone to "fix" working code back
into a broken shape:

- **§9.2's approvals filename scheme** said `sha256(payload_bytes)`. That collides
  approve-raw with approve-distilled over identical content at the same ref, and
  collides *every* ref-reject onto one filename. The code hashes the **full framed
  payload** and adds a key-tag. §9.2 now specifies what the code does.
- **§3.2's `ref:` field** was never serialized in this document — it just said
  "canonical item ref". A third party implementing from that text would have emitted
  a different string and produced signatures that never verify, against a framing
  §12 declares a public contract. §3.2 now pins it byte for byte.

---

## 1. What this is

Two assertions, one primitive.

| Assertion | Who makes it | Means | Namespace |
|---|---|---|---|
| **publish** | the author of content | "I, key K, published these bytes" — **authentication** | `publish.v1.ctxloom.dev` |
| **approve** | the human running ctxloom | "I, key K, reviewed these exact bytes and allow them to reach my agent" — **authorization** | `approve.v1.ctxloom.dev` |
| **reject** | the human running ctxloom | "I, key K, refuse these exact bytes / this ref, permanently" — **authorization** | `reject.v1.ctxloom.dev` |

All three are SSH signatures (`ssh-keygen -Y sign -n <namespace>`, PROTOCOL.sshsig
armored blobs, `allowed_signers` for trust roots). Namespaces are what keep the
assertions from being replayed as one another; this is exactly what SSH signature
namespaces exist for (git uses namespace `git`). We do not invent a parallel
mechanism.

**Signatures authenticate; review authorizes.** A validly-signed malicious
fragment is still malicious. There is no "signed" item state. The three states —
pending, approved, rejected — survive unchanged; what changes is that the
*record* of an approval or a rejection becomes a signature instead of a hash-pair
row in a plain file.

### 1.1 Why the approval record becomes a signature

The current design records an approval as a row in `.ctxloom/trust.yaml`: a
`(repo, ref, raw_hash, distilled_hash, accepted)` tuple. Replacing that row with a
countersignature over the approved bytes buys four things:

1. **Hash-binding falls out for free.** A signature is over bytes. Change the
   bytes, the signature no longer verifies, the item returns to pending. That *is*
   today's "acceptance binds to the exact `(raw, distilled)` pair; any change
   re-gates to pending" — obtained without maintaining a second ledger.
2. **Rename-immunity falls out for free.** A rejection countersignature over
   content bytes fails to verify against nothing and succeeds against those bytes
   *wherever they appear* — which is precisely the content-hash denylist property
   ("a renamed identical copy stays rejected"). Red line 5 is satisfied *by* the
   design rather than bolted onto it. (See §5.3 — this holds only because we
   deliberately sign a payload that omits the ref for the content-reject
   assertion. It is a design choice, not an accident.)
3. **It closes an agent-shaped hole that exists today.** `trust.yaml` is a plain
   file. Anything that can write it can insert `state: accepted` — *including the
   agent ctxloom just launched*, which has file-write tools. Today an agent can
   fetch a fragment, write an acceptance for it, and read it back into its own
   context on the next assembly. A countersignature cannot be forged without the
   key. (This property has hard preconditions — §9.4. Read them before believing
   it.)
4. **Approvals become portable.** A signed approval is a transferable artifact: a
   team lead approves, commits the signatures, and every developer who trusts that
   lead's key inherits the approval without re-reviewing. This answers the open
   "non-interactive / CI story" item in `docs/trust-simplify-plan.md` (now superseded — see its banner).

---

## 2. Threat model

### Defended

- **Malicious bundle update.** Changed bytes → the publisher's signature covers
  the old bytes and the approval countersignature covers the old bytes → item
  returns to pending → withheld until a human reviews the diff.
- **Content injected into the trust path by the agent itself.** An agent that
  writes a fragment into a bundle cannot approve it: approval requires a signature
  under a key in `allowed_signers`, and (under §9.4's preconditions) the agent
  cannot reach that key. *This is the headline new property.*
- **Forged approvals generally.** No key, no approval. A hand-edited approval
  store is inert — a row whose signature does not verify is not an approval, it is
  noise, and the item resolves pending (fail closed).
- **Rejection escape by rename/move/republish.** Content-reject signs bytes, not
  refs (§5.3).
- **Impersonation of a publisher.** Content claiming to come from
  `ctxloom-default` but not signed by its key is not trusted content; it is
  unsigned content, and takes the review path.
- **Supply-chain substitution at rest.** A tampered clone object, a
  man-in-the-middle on a `git fetch`, or a compromised forge cannot produce
  content that verifies under a trusted publisher key.

### NOT defended — state these plainly in user-facing docs

- **Signed ≠ safe.** A signature says *who*, never *whether it is good for you*.
  The ctxloom release key can sign a fragment that talks your agent into
  exfiltrating secrets. That is why review (authorization) is a separate,
  non-optional axis and why rejection outranks every signature, including ours.
- **A malicious binary already on your PATH.** If a hostile `taskloom` is on your
  PATH, its hooks and MCP servers *execute*. Signing its loadout output changes
  nothing about that: executing a binary is a strictly larger grant than reading
  its context. The companion-loadout signature (§6, surface 3) is about *provenance and
  gate-routing*, not about containing a hostile companion.
- **An attacker with arbitrary write access to your home directory.** Such an
  attacker can add their own key to `allowed_signers`, or replace the `ctxloom`
  binary outright. Signing raises the bar from "write one YAML row" to "tamper
  with the trust root", but it is not a boundary against full host compromise. The
  boundary is real for a *containerized* agent (§9.4), which is the case ctxloom
  actually cares about.
- **A host-run agent that holds `SSH_AUTH_SOCK`, when the key is in a bare
  ssh-agent with no confirmation.** Stated plainly because it is the single most
  over-claimable property in this document: **ctxloom never stores a private key,
  but that alone does not stop an agent from using yours.** Any process holding
  `SSH_AUTH_SOCK` can ask the agent to sign arbitrary bytes. It cannot *read* the
  key — and it does not need to. It needs a signature, and the agent will hand it
  one. A coding agent with that variable in its environment can therefore forge an
  approval for content it just wrote. **`ssh-add -c` or a hardware token closes
  this; nothing else does.** See §9.1, which grades every posture.
- **A publisher who signs and then goes bad.** Key trust is trust. Revocation is
  §10.3, and it is coarse.
- **Timestamps.** SSH signatures carry no trusted time (PROTOCOL.sshsig has no
  timestamp field). We cannot prove *when* an approval was made. Any `reviewed_at`
  we store is untrusted metadata for humans, never an input to a decision.

---

## 3. THE CENTRAL DECISION — what bytes are signed

> **We sign raw bytes. We never canonicalize YAML or JSON to produce a signing
> payload.** Canonicalization schemes are a signature-bypass factory: any
> disagreement between the signer's serializer and the verifier's serializer is a
> forgery primitive. Every payload below is either (a) bytes that already exist on
> disk / on the wire, or (b) bytes that ctxloom *already* uses as a hash preimage
> in shipped code.

There are two payload shapes, because there are two things being asserted about
two different byte sequences. This is the single most important thing in this
document.

### 3.0 INVARIANT: the signature is transport-agnostic

> **A signed artifact verifies identically, byte-for-byte, regardless of how it
> arrived.** The signature covers content bytes and *nothing about the channel*.
> Move it from a git remote to a tarball to an MDM push to a companion's stdout and
> back: same bytes, same signature, same verdict.

This is not a nice-to-have; it is what makes an organization able to publish signed
context once and deliver it through whatever channel it already has. It forbids, by
name:

- **No signing over the git blob/commit SHA.** That binds the artifact to git and
  dies the moment it moves to stdout or a registry.
- **No binding to the URL, remote name, repo, or path it arrived from.** A bundle
  that verifies from `github.com/acme/ctxloom` must verify identically when the
  same bytes are handed over on a USB stick. (Note this is *also* why key trust
  beats `trust_bundles` — §11 — which binds trust to a location by construction.)
- **No re-serialization anywhere between publisher and verifier.** Not "parse the
  YAML and re-emit it canonically", not "normalize line endings", not "strip
  trailing whitespace". The bytes the publisher signed are the bytes the verifier
  hashes. Any transform in that path is a signature bypass waiting to be found.
- **No transport-supplied metadata as a signature input.** HTTP headers, git
  committer identity, file mtime, the JSON envelope's own `signer` field (§4.3) —
  all advisory, none load-bearing.

The consequence for the design is simple and it is the reason the raw-bytes
decision holds up: **the signed artifact is the pair `(content bytes, detached
signature)`.** Every transport carries that pair in its own idiom, and the pair is
inert with respect to the idiom. §4 checks this against all four channels.

### 3.1 Publisher payload — the bundle FILE bytes, unframed

**Payload = the exact bytes of the bundle YAML file, as they exist in the git tree
at the pinned SHA. Nothing prepended, nothing appended, nothing normalized.**

```
signed_payload(publish) := <bundle file bytes, verbatim>
```

The SSH signature already binds the namespace (`publish.v1.ctxloom.dev`) inside
its own signed blob, so no framing header is needed and none is added. This
payload is **parser-independent**: verification is a byte comparison against a
signature, and it happens *before* any YAML parse. A publisher signs a file; a
verifier verifies a file. There is no serializer in the loop on either side.

### 3.2 Countersignature payload — the exposed ITEM bytes, framed

Here is the impedance mismatch that must be understood, or the implementation will
be wrong:

**The publisher signs a *file*. The gate exposes an *item*.**
`Loader.gateContent` (`internal/bundles/loader.go`) never sees file bytes. It is
fed, from the fragment and command resolution paths in
`internal/bundles/loader_content.go`, the *decoded scalar value* of one item —
`BundleFragment.ContentPayload` / `BundleCommand.ContentPayload` — and the gate
covers exactly those bytes. A human approves *that fragment*, in *that form*, not
the whole file.

So the countersignature payload is **the exact byte sequence that ctxloom already
uses as its content-hash preimage** — the bytes about to be exposed to the agent,
pre-mustache-substitution — with a small fixed-shape header that binds the
assertion's scope.

```
signed_payload(approve|reject) :=
    "ctxloom-countersign/2\n"          # contract version, ASCII, fixed
    "assertion: " <approve|reject> "\n"
    "ref: " <canonical item ref, or empty> "\n"
    "form: " <an attestation form, or empty> "\n"
    "len: " <decimal length of payload_bytes> "\n"
    "\n"                                # blank line terminates the header
    <payload_bytes>                     # verbatim, exactly len bytes
```

The `form` field is the **attestation form**: a single composite value naming both
the item's ROLE and, for the two distillable kinds, which materialization was
reviewed (§3.2.2). There is deliberately no separate `kind:` line — one field, one
closed vocabulary, and no way to emit a header whose kind and form disagree.

The header is **not a canonicalization**. It is a fixed, length-prefixed,
LF-delimited ASCII preamble with a closed field set, emitted and parsed by the
same function on both sides (`signing.CountersignPayload`), containing no
user-controlled structure beyond the ref — the assertion and the form are drawn
from closed vocabularies, and `len` makes the payload boundary unambiguous
regardless of what the payload contains. It exists because the ref and the form are
*not* present in the payload bytes and must be bound to the assertion (otherwise an
approval of fragment A's bytes would be replayable as an approval of identical
bytes exposed in a different form or at a different ref — see §5.2/§5.3 for exactly
which of these we bind and which we deliberately do not).

#### 3.2.1 The `ref:` field — pinned, byte for byte

**This is a public contract (§12). A third party who emits a different `ref` string
produces signatures that will never verify.** "Canonical item ref" is not a
specification; here is the serialization, normatively:

```
ref := CanonicalURL "|" Bundle "#" KindDir "/" Name
```

Emitted by `operations.countersignRef` from a `trust.Ref`. Component by component:

| Component | Value | Source |
|---|---|---|
| `CanonicalURL` | the canonicalized source repo URL; or the literal `ctxloom:local` for project-authored items; or `builtin:ctxloom` for in-binary items; or `ctxloom:companion` for a companion loadout | `trust.Ref.CanonicalURL` → `trust.CanonicalRepoURL` |
| `Bundle` | the bundle name (e.g. `go-tools`) — **not** a path, **not** a canonical ref | `trust.Ref.Bundle` |
| `KindDir` | one of `fragments`, `prompts`, `mcp`, `hooks` | `trust.ItemKind.Dir` |
| `Name` | the item name within the bundle | `trust.Ref.Name` |

The separator between the URL and the item key is a single ASCII pipe (`|`).
**`Bundle#KindDir/Name` alone is not sufficient and must not be used**: two repos
publishing a same-named bundle would collide, so an approval of one repo's
`go-tools#fragments/go-testing` would silently authorize the other's. The URL is
prepended for exactly that reason.

`CanonicalRepoURL` is itself normative, because a verifier that canonicalizes
differently computes a different ref and fails to verify. It unifies scheme,
rewrites `git@host:owner/repo` to `https://host/owner/repo`, strips a trailing
`.git` for http(s), lowercases the host, trims a trailing slash, and — for the
known case-insensitive forges (`github.com`, `www.github.com`, `gitlab.com`,
`bitbucket.org`) and **only** those — lowercases the owner/repo path. Other
transports (`file://`, `ssh://`, `git://`) keep their path case verbatim, where it
may be significant. The `ctxloom:local` and `ctxloom:companion` tokens pass through
untouched.

**Worked example.** The fragment `go-testing` in bundle `go-tools`, published by
`https://github.com/ctxloom/ctxloom-default`, yields exactly:

```
https://github.com/ctxloom/ctxloom-default|go-tools#fragments/go-testing
```

and a locally authored fragment yields:

```
ctxloom:local|my-tools#fragments/go-testing
```

**One vocabulary trap, and it is deliberate, not a bug.** The attestation form says
`command/*`, while the ref's `KindDir` component embeds `prompts`
(`trust.ItemKind.Dir` — the on-disk directory name, which predates the
prompt→command rename and is what the ref grammar has always used). Both appear in
the same signed payload, and both are load-bearing. An implementer who "unifies"
them changes the signed bytes and invalidates every command approval in existence.

#### 3.2.2 The attestation form, and `payload_bytes` per kind

The `form:` field is a **closed composite vocabulary** (`signing.AttestationForm`),
derived from the item's live kind and the layout form its bytes are in. This is the
complete, normative mapping — a third party that emits any other value produces
signatures that will never verify:

| item kind (`trust.ItemKind`) | layout form | `form:` field | `payload_bytes` | preimage builder |
|---|---|---|---|---|
| `fragment` | raw | `fragment/raw` | the fragment's authored content | `BundleFragment.ContentPayload` |
| `fragment` | distilled | `fragment/distilled` | the fragment's distilled rewrite | `BundleFragment.ContentPayload` |
| `prompt` (a command) | raw | `command/raw` | the command's authored content | `BundleCommand.ContentPayload` |
| `prompt` (a command) | distilled | `command/distilled` | the command's distilled rewrite | `BundleCommand.ContentPayload` |
| `mcp` | raw (its only form) | `exec/mcp` | `BundleMCP` canonical JSON (Command, Args, Env, Installation) | `BundleMCP.ContentPayload` |
| `hook` | raw (its only form) | `exec/hook` | `BundleHook` canonical JSON (executable surface) | `BundleHook.ContentPayload` |
| `skill` | raw (its only form) | `skill` | `SkillManifest` canonical JSON (the whole package tree) | `BundleSkill.ContentPayload` |

A ref-reject binds no content, so its `form:` field is empty (§5.3).

**Why the form is composite, and why this is security-load-bearing.** An approval
attests bytes IN A ROLE, and the role is not recoverable from the bytes. A
fragment's and a command's payloads are BARE content bytes — no tag, no length
prefix, no delimiter — while exec and skill payloads are deterministic JSON with
every field always emitted. So a publisher can ship a FRAGMENT whose body is
literally an MCP server's preimage alongside the matching MCP server: byte
EQUALITY, no collision search. A reviewer is shown that fragment as TEXT
(fragments render as content; executables render as "what they run") and approves
it. If the two shared an approval key, the executable would then be trusted having
NEVER been displayed as an executable — the dangerous rendering is exactly the step
skipped for an already-approved item. Folding the role into the signed form value
makes them different signed bytes, so no such transfer is possible. The same holds
one axis over, for fragment vs command, which are otherwise indistinguishable.

**Routing never reads this value.** What an item IS, and how it is rendered and
dispatched, comes from the surface-type registry. A registry name with no
attestation form is INERT by construction: it can be neither approved nor exposed
through the gate, so extending the vocabulary of things a bundle can carry adds no
security surface. That is also why the attestation vocabulary is CLOSED — what
verifies must never depend on which plugins are loaded.

There is **exactly one** definition of "the bytes of item X in form F": the
`ContentPayload` method for that kind. `ComputeContentHash` hashes precisely that
function's output, and the signer signs precisely that function's output. Two
definitions is the bug.

### 3.3 The two honest caveats — do not paper over these

1. **The countersignature payload for text items is parser-dependent.** It is the
   YAML scalar *after* `yaml.v3` decodes block scalars, escapes, and line endings.
   If the YAML parser's decoding of the same file bytes ever changes (a library
   upgrade, a CRLF handling fix), previously-valid approvals stop verifying.
   **This is fail-safe** — the item returns to pending and a human re-reviews; it
   is never a bypass. And it is not a new exposure: it is *precisely* the property
   today's `EffectiveContentHash` already has, since it hashes the same decoded
   scalar. We are not adding a canonicalization; we are signing the preimage of a
   hash that already exists. The publisher signature (§3.1), which is the one an
   attacker would want to forge, has no parser in its verification path at all.
2. **The exec-item payload IS a canonicalization — an existing one.**
   `BundleMCP.ContentPayload` already builds a canonical JSON struct. MCP servers
   and hooks have no "raw bytes"; they are structured fields. There is no way to
   sign them without a serialization, and we adopt the one that already ships
   rather than inventing a second. The risk is real but narrow and fail-safe:
   **adding a field to `BundleMCP` changes the preimage and silently invalidates
   every approval of every MCP server**, sending them all back to pending. That is
   safe (fail-closed) but is a nasty surprise. Therefore the exec preimage is
   **versioned** — see §3.3.2. This is the one place the "no canonicalization" rule
   cannot be honored, and it must be called out in the implementer brief.

#### 3.3.2 The versioned exec preimage — `ctxloom-exec/1`

The canonical exec struct carries the contract version as its **first field**:

```json
{"preimage":"ctxloom-exec/1","command":"…","args":[…],"env":{…},"installation":"…"}
```

The version is not defensive against an attacker — a forged preimage gains nothing
by naming a version. **It is defensive against us:** any change to the exec field
set requires bumping this string, which converts an accidental, unannounced,
silent mass-invalidation of every MCP and hook approval into a deliberate,
announced act. Position is part of the contract; a version carrier buried mid-object
would satisfy an order-insensitive compare and still be wrong.

The constant is `signing.ExecPreimageContract`. Third parties depend on this string;
it is a public contract (§12), not an implementation detail.

---

## 4. Carriers — where the signature physically lives

**The artifact is always the same pair: `(content bytes, detached sshsig blob)`.**
The carrier only decides how those two travel together. Checked against all four
channels the org story requires:

| Channel | Content bytes | Signature | Same signature blob? |
|---|---|---|---|
| **git remote** (today's remote bundles) | `.ctxloom/content/bundles/x.yaml` in the tree at the pinned SHA | `.ctxloom/content/bundles/x.yaml.sig`, same tree, same SHA | **yes** |
| **companion stdout** (`<bin> loadout --format json`) | base64 field, decoded before verify | `signature` field, armored | **yes** |
| **plain file** handed to someone | `x.yaml` | `x.yaml.sig` next to it | **yes** |
| **artifact registry / object store / MDM push** | the blob | the `.sig` blob (or the JSON envelope, publisher's choice) | **yes** |

The signature blob is **identical in all four rows** — it is an sshsig over the
bundle's raw bytes and it knows nothing about how it got there. A publisher signs
once; the artifact can then be republished through any channel, by anyone, without
re-signing and without the publisher's involvement. **That is the enterprise
property**, and it falls directly out of §3.0. A carrier that could not do this —
anything binding the signature to a git SHA, a URL, or a re-serialization — would
be a failed design, which is exactly why the in-document envelope (§4.1, rejected)
loses on more than one count.

### 4.1 Remote bundles: a detached sibling `.sig` in the same git tree. **SHIPPED.**

**Path:** `<bundle path>.sig` — e.g. `.ctxloom/content/bundles/agent-roles.yaml.sig`,
sitting next to `.ctxloom/content/bundles/agent-roles.yaml`, committed to the same
repo, read at the **same pinned SHA**. This is live: ctxloom-default publishes 43
bundles and 43 sibling signatures on `main`.

A previous investigation concluded that because ADR 0028 reads content out of the
clone's object store at a pinned SHA and never extracts bundles to disk, a
detached `.sig` sibling "has nowhere to live." **That conclusion was wrong**, and
the refutation is what this carrier rests on. A sibling path is just another path
in the same git tree at the same SHA, and the read path already takes an arbitrary
path:

- `Fetcher.FetchFile(ctx, owner, repo, path, ref)` (`internal/remote/fetcher.go`)
  takes an **arbitrary repo-relative path** and an arbitrary ref.
- `GitCloneFetcher.FetchFile` (`internal/remote/git_clone_fetcher.go`) implements
  it as `tree.File(filePath)` against the tree at that ref. Zero network calls.
- `cacheFetcher.FetchFile` (`internal/remote/cached_fetcher_factory.go`) routes
  through `RepoCache.EnsureRef` / `ensureClone` (`internal/remote/repo_cache.go`):
  an existing clone is used as-is. The SHA is already present, because the bundle
  bytes were just read from it.
- `BundleReader.ReadBundleBytes` (`internal/remote/bundle_reader.go`) computes
  `filePath := ref.BuildFilePath(ref.ItemType)` and calls
  `fetcher.FetchFile(ctx, owner, repo, filePath, entry.SHA)`. **Reading the
  signature is the identical call with `filePath + ".sig"`.**

Three properties hold, and the design depends on all three: (i) an arbitrary
sibling path resolves at a pinned historical SHA, offline, through the production
fetcher; (ii) a *missing* `.sig` returns a clean, typed
`errs.ErrRemoteContentNotFound` — a distinguishable "unsigned" signal, not a crash;
(iii) the whole thing costs one extra `tree.File()` on an already-open tree.

**Therefore: detached sibling `.sig`, and no in-document envelope.** This is
strictly safer, because the signed bytes are the file bytes and the verifier never
has to remove a signature field from a document it is about to verify. The
in-document alternative (sign the document with the signature field spliced out at
a byte offset) is a correctness hazard for zero benefit and is **rejected**.

**Bundle-level, not repo-level.** One `.sig` per bundle file. A repo-wide manifest
signature would re-introduce a canonicalization (of the manifest) and would couple
unrelated bundles' trust together.

**Detection of unsigned content is by absence:** no `.sig` at the pinned SHA →
unsigned → review path (§7 step 5). A `.sig` that is present but does not verify
is **not** the same as absent: it is a hard failure (§10.2).

### 4.2 Local filesystem bundles

Same rule, same shape: `.ctxloom/content/bundles/foo.yaml.sig` next to `foo.yaml`.
Local project content is first-party and exempt from review anyway (§8 step 2), so
this carrier exists only for the case where a user wants to sign bundles *they
publish from* a local checkout before pushing. `ctxloom sign` writes it.

**The pair invariant is enforced on write.** Every bundle mutation lands in
`fsStore.Save`, which will not leave a signature on disk that no longer covers the
bytes beside it: a `.sig` that still covers an idempotent re-save survives
byte-for-byte, and one that has been outdated is **removed, loudly**
(`invalidateStaleSignature`). A stale pair is not harmless staleness — downstream it
is indistinguishable from an attack, and it would point every consumer of the bundle
at tampering that never happened.

**Note that no filesystem *load* path verifies a `.sig` today** — see §4.4, which
is the flow that would need it.

### 4.3 Companion loadout (stdout JSON envelope). **SHIPPED.**

This is the one surface where the emitter controls the bytes, so it is an
envelope rather than a sibling file.

The companion binary owns and emits its own bundle (`cmd/ltk/loadout.go`,
`cmd/taskloom/loadout.go`), mirroring the `<bin> version --format json` probe
convention, and ctxloom discovers it at boot (`internal/config/companions.go`).
This replaced an earlier design in which companion bundles were vendored into
ctxloom's own binary under `resources/builtin_bundles/`; **those vendored bundles
are deleted** — that directory now holds only a README.

**Contract — `<companion> loadout --format json`, on stdout.** The same JSON
envelope is *also* the recommended carrier for a registry / object-store / MDM
channel (§4.5), because it keeps the pair together in one transportable object:

```json
{
  "contract": "ctxloom-loadout/1",
  "bundle": "<base64(std, padded) of the exact bundle YAML bytes>",
  "signature": "-----BEGIN SSH SIGNATURE-----\n...\n-----END SSH SIGNATURE-----\n",
  "signer": "releases@ctxloom.dev"
}
```

- `bundle` is base64 **only** to survive JSON transport. The signed payload is the
  **decoded bytes**, verbatim — identical in kind to §3.1. The verifier decodes,
  verifies the signature over the decoded bytes, and only then parses YAML.
- `signature` is over the decoded bundle bytes under namespace
  `publish.v1.ctxloom.dev`. It is produced at **companion build time** and embedded
  in the companion binary (the companion does not hold a private key at runtime).
- `signer` is advisory — a hint for error messages. The **key** is resolved from
  `allowed_signers`, never from the envelope. An envelope naming a signer that is
  not in `allowed_signers` is unsigned content, full stop. *(Implementer trap #3.)*
- Unknown top-level fields **must** be ignored (forward compatibility, §12).

### 4.4 Organization-published loadouts (registry / object store / MDM)

> **PLANNED — not yet implemented.** The channel-agnosticism argued below is a real
> property of the *signature* (§3.0), and it is why this will be cheap to build. But
> **no filesystem load path verifies a `.sig` today.** Publisher verification is
> wired into exactly two load paths — the remote-git seed
> (`config.loadRemoteBundleSeed`) and the companion loadout
> (`config.companions.go`), the two places `Bundle.StampSigner` is called. A signed
> `(x.yaml, x.yaml.sig)` pair dropped into a directory ctxloom reads is treated as
> **first-party local content** (allowed at step 2, unverified) or stamped with no
> signer at all. **The enterprise drop-in flow described in this section and §7A.6
> does not work as written.** Closing it means verifying the sibling `.sig` in the
> filesystem load path and stamping the signer there, exactly as the remote seed
> already does; the carrier and the payload need no change.

An org publishes signed bundle content once and ships it by whatever channel it
already runs. **The channel itself needs no new code** — the org drops either the
`(x.yaml, x.yaml.sig)` pair into a directory ctxloom already reads, or the JSON
envelope of §4.3. Verification is identical in both cases because the signed payload
is identical (§3.0).

This is the enterprise flow end to end:

1. Org signs its bundles with the **org key** (`ctxloom sign`, or plain
   `ssh-keygen -Y sign` — our tooling is never required).
2. Org **countersigns the approvals** once, centrally, with a reviewer key (§5.2).
3. Org ships an `allowed_signers` naming the org key (publish) and the reviewer key
   (approve) — see §7.3 for how that file gets onto the machine, which is the one
   genuinely hard step.
4. A developer clones, runs ctxloom, and the content is allowed at **step 4**
   (trusted publisher) or **step 5** (inherited approval). **Zero review prompts,
   zero secrets on the developer's machine, and the developer needs no key of their
   own** — they are verifying signatures, not making them.

The same flow *is* the CI flow (§9.2): CI is just a developer who never approves
anything.

### 4.5 Builtin (in-binary) bundles

**Builtins are not signed, and must not be.** Signing bytes that are embedded in
the binary that is doing the verifying is circular: if you can trust the binary's
verifier, you can trust the binary's embedded bytes. The integrity of the embedded
bytes is exactly the integrity of the binary, and it is the **release-artifact
signature** (§6, surface 1) that is supposed to establish that.

> **Note the load-bearing caveat:** §6 surface 1 **is not implemented** — there is no
> `signs:` block in `.goreleaser.yml`, so **release artifacts are unsigned**. The
> "covered transitively" argument is therefore currently an argument about a
> signature that does not exist. Builtins are trusted because you ran the binary, and
> for no cryptographic reason beyond that. That is a defensible position — it is the
> same trust you extend by executing the binary at all — but it is not the position
> this section originally claimed.

What builtins **do** need is to stop bypassing the gate. They are assigned the
synthetic signer identity `builtin:ctxloom`, and they are routed through the
decision function like everything else, where they are allowed at step 3 — *below*
rejection. See §8.

---

## 5. The assertions in detail

### 5.1 publish

- Namespace: `publish.v1.ctxloom.dev`
- Payload: §3.1 — bundle file bytes, unframed.
- Made by: a bundle publisher, at publish time, with `ctxloom sign`
  (a thin wrapper over `ssh-keygen -Y sign`) or by `ssh-keygen` directly. The
  format is plain sshsig; publishers are never required to use our tooling.
- Verified: once per bundle, at load, **before parse**.
- Effect: attaches a **verified signer identity** to every item parsed out of that
  bundle. Nothing more. It does not authorize anything by itself.

### 5.2 approve — ref-scoped, form-scoped

- Namespace: `approve.v1.ctxloom.dev`
- Payload: §3.2 with `ref` = the canonical item ref and `form` = the exposed form.
- **The ref is bound deliberately.** This preserves today's semantics exactly: an
  approval is of *this item at this ref in this form*. Moving an item to a new ref
  re-gates it to pending (safe), and approving a fragment's raw form does not
  approve its distilled form. That last property is today's `(raw, distilled)` pair
  binding, and it survives as **two signatures rather than two hash columns**: an
  approval of an item that has both forms emits an approve signature over the raw
  payload *and* one over the distilled payload. The pair semantics are unchanged;
  only the storage changes.

### 5.3 reject — two components, mirroring today's two-component rejection

`operations.SetBlacklist` (`internal/operations/trust.go`) records **both** a
ref-level block and the item's content bytes. The countersigned form keeps both, as
two distinct signatures:

- **ref-reject:** payload header with `ref` set, `form` empty, `len: 0`, and
  **empty payload bytes**. Blocks that ref regardless of what its content becomes.
  This is the "sticky block" — it survives the content changing under the ref.
- **content-reject:** payload header with `ref` **empty**, `form` set, and the item
  bytes as payload. Emitted **once per form** the item currently has (raw and
  distilled), exactly as `SetBlacklist` denylists both hashes today. Because the
  ref is omitted from the signed payload, this rejection verifies against *those
  bytes wherever they appear* — a renamed, moved, or republished-under-another-key
  identical copy still matches. **Red line 5 is satisfied by construction:
  re-signing rejected bytes under a different key does not change the bytes, and it
  is the bytes that are rejected.**

The asymmetry between approve (ref bound) and reject (ref omitted for the content
component) is the whole trick, and it is intentional. Approval should be narrow;
rejection should be broad. An implementer who "cleans this up" by making the two
payloads symmetric breaks one property or the other. *(Implementer trap #1.)*

**Known gap, unchanged and inherited:** content-reject binds the forms present *at
rejection time*. A copy later exposed in a form that was not denylisted, at a
different ref, escapes the content component (the ref component still catches the
original ref). This is exactly Known Gap #1 in trust-model.md; countersigning does
not close it and does not widen it.

---

## 6. Three signing surfaces — separate keys, separate pipelines, do not fuse

> **PLANNED — not yet implemented.** The **key separation described here does not
> exist.** `internal/config/embedded_signers.allowed_signers` ships **exactly one
> key**, trusted for the publish namespace, and that single key signs both the
> ctxloom-default bundles (surface 2) and both companion loadouts (surface 3).
> `.goreleaser.yml` has **no `signs:` block at all** — surface 1 does not exist:
> **release artifacts are unsigned.** There is no per-companion key pipeline.
>
> The argument below is the direction, and the reason to do the work: the surfaces
> have genuinely different compromise radii and a single key fuses them. Until they
> are split, **the compromise radius of the one embedded key is all three surfaces
> at once**, and that is the honest current statement of the risk.

| # | Surface | What is signed | Key (intended) | Pipeline (intended) | Status |
|---|---|---|---|---|---|
| 1 | **Release artifacts** | `checksums.txt` of the ctxloom binaries | **ctxloom release key** | `signs:` block in `.goreleaser.yml`. goreleaser is devcontainer-only here (`just release-check` / `release-snapshot`), so the signing step would run there, never on the host. | **not implemented** — no `signs:` block; releases are unsigned |
| 2 | **Bundle content in the ctxloom-default repo** | each `.ctxloom/content/bundles/*.yaml` file | **ctxloom bundle-publishing key** — a *different* key from #1 | CI in the `ctxloom-default` repo, on merge to main; commits the `.sig` siblings. | **signing shipped** (43 bundles signed and pushed) — but under the single embedded key, not a distinct one |
| 3 | **Companion loadout** | the bundle bytes each companion emits | **each companion's own key** (taskloom's key, ltk's key) | that companion's release build embeds the signature (§4.3). | **signing shipped** (both loadouts signed, committed, and verifying) — but under the same single embedded key, not per-companion keys |

**These should be three keys and three pipelines.** They should be separate because
their compromise radii are different: the release key signs executables (worst), the
bundle key signs text an agent reads (bad), a companion key signs one companion's
loadout (contained). Reusing one key across them turns any one compromise into all
three — which is precisely the state we are in today. A user may want to trust the
release key and *not* the bundle key; those are different decisions, and
`allowed_signers` namespace options (§7) make the distinction expressible as soon as
there is more than one key to distinguish.

---

## 7. The allowed-signers store

**Format: the `ssh-keygen` `allowed_signers` format, verbatim.** No new format, no
new vocabulary (red line 7).

```
# ~/.ctxloom/allowed_signers
releases@ctxloom.dev  namespaces="publish.v1.ctxloom.dev" ssh-ed25519 AAAAC3Nza... ctxloom release key
bundles@ctxloom.dev   namespaces="publish.v1.ctxloom.dev" ssh-ed25519 AAAAC3Nza... ctxloom-default bundle key
ben@abbitt.me         namespaces="approve.v1.ctxloom.dev,reject.v1.ctxloom.dev,publish.v1.ctxloom.dev" sk-ssh-ed25519@openssh.com AAAAG...
lead@team.example     namespaces="approve.v1.ctxloom.dev,reject.v1.ctxloom.dev" ssh-ed25519 AAAAC3Nza... team lead
```

The `namespaces="..."` option **is the role system, for free**: a publisher key
listed only under the publish namespace *cannot* approve content, even if it is
compromised. This is standard OpenSSH, not an invention. `valid-after` /
`valid-before` are also available (with the timestamp caveat in §10.3).

**Locations, in precedence order (all are unioned; a key in any of them counts for
the namespaces it lists there):**

1. **Embedded defaults** — ctxloom's own release + bundle public keys, compiled
   into the binary. This is the root of trust and it ships with the binary,
   which is the only honest place for it: a trust root delivered over the same
   channel as the thing it validates is no trust root at all.
2. **User** — `~/.ctxloom/allowed_signers`. Third-party publisher keys and the
   user's own approval key.
3. **Project** — `.ctxloom/allowed_signers`, committable. This is how a team
   distributes "trust our lead's approval key" and how the CI story works.

**The embedded defaults should be removable.**

> **PLANNED — not yet implemented.** They are **not removable today.** The embedded
> trust root is compiled in and **unconditionally unioned** into every lookup
> (`config.TrustRoot`), and `operations.RemoveSigner` only rewrites the user or
> project *file* — it has no way to subtract a key it did not write. There is no
> negative-entry mechanism, so **`ctxloom signer remove ben+ctxloom@abbitt.me`
> cannot untrust ctxloom's own key.** A user who does not want to auto-trust
> ctxloom's published bundles currently has no supported way to say so.
>
> The intended design: `signer remove` writes a **negative entry** in the user store,
> which `TrustRoot` subtracts after the union, and then every item from that key
> takes the review path like any other. (Note this is *distinct* from rejecting
> content: removing a key means "I will review this myself", not "deny".)
>
> This matters more than it looks. §7.3 row D justifies the embedded key as "trust in
> the binary — circular by construction, and that is fine". That justification is
> honest only if the user can *decline*. Without removal, the embedded key is not a
> default; it is a mandate.

### 7.1 Signer identities are organizational, not just personal

The `allowed_signers` **principal** is an arbitrary identity string. That is all
the identity model we need, and we invent nothing alongside it (ADR 0027):

| Kind of signer | Principal (example) | Typically trusted for |
|---|---|---|
| an individual | `ben@abbitt.me` | approve, reject, publish |
| a team lead / security reviewer | `lead@team.example` | **approve, reject** (inherited approvals — §9.2) |
| an organization / company | `context@acme.com` | **publish** (org-published loadouts — §4.4) |
| a release pipeline | `releases@ctxloom.dev` | publish |
| a bundle-publishing pipeline | `bundles@ctxloom.dev` | publish |

An org key and a personal key are the same kind of object, differing only in the
principal string and the namespaces they are trusted for. `namespaces="..."` is the
whole role system (§7 above): an org key trusted only to *publish* cannot approve;
a reviewer key trusted only to *approve* cannot publish under the org's name.

### 7.2 CLI

**Trusting a publisher or reviewer key: explicit add, never a first-sight prompt.**

```
ctxloom signer add <principal> --key <path|->  [--namespace publish|approve|reject]   # default: publish
ctxloom signer add <principal> --key <path> --project     # write to the committable project store
ctxloom signer list [--json]
ctxloom signer show <principal>                           # fingerprint, namespaces, source store
ctxloom signer remove <principal>
```

**Consequence-naming confirmation, mirroring `ctxloom remote trust` today.** Adding
a signer is the single most dangerous command in this feature and it must say so,
naming the *actual* consequence and showing the fingerprint the user is supposed to
be checking out of band:

```
Trust context@acme.com as a PUBLISHER?

  SHA256:8Ja1xkV9…q0Zc   (ssh-ed25519)

  Everything this signer ever publishes — text AND executables (MCP servers,
  hooks), now and in every future update — will reach your agent WITHOUT REVIEW.
  Verify this fingerprint out of band before you continue.

  [y/N]
```

and for an approve-namespace key, the consequence is different and must be worded
as such: *"Everything this signer ever **approves** reaches your agent unreviewed
— you are delegating your review decisions to them, forever."*

**Key/signer management is CLI-only. It is never exposed over MCP** (red line 6,
ADR 0024). The agent must not be able to enumerate, add, or remove signers — an
`approve` tool or a `signer add` tool would hand the agent the exact capability
this design exists to deny it.

### 7.3 THE BOOTSTRAP PROBLEM — the one link cryptography cannot forge for us

A signature proves who signed. **It cannot tell you whose key to trust in the first
place.** Every signing scheme bottoms out here, and an enterprise story that
hand-waves it is not a story. The paths, with what each actually guarantees:

| Path | Mechanism | What it actually guarantees | Where it fails |
|---|---|---|---|
| **A. `allowed_signers` committed in the project repo** *(recommended default)* | `.ctxloom/allowed_signers`, checked in | **Trust-on-first-clone.** Exactly as strong as your trust in the repo — which is *already* strong enough that you run its build scripts, its CI, its `Makefile`, and its devcontainer. A repo that can execute code on your machine can certainly name a key. | An attacker who can push to the repo can add a key. But such an attacker can already put code in the repo — so this adds **no new exposure**; it inherits the repo's existing trust boundary. It does **not** protect against a malicious *fork* you cloned by mistake, or a typosquatted repo — those are prior questions the signature was never going to answer. |
| **B. Org-managed config / MDM push** *(recommended for enterprises)* | the file is placed at `~/.ctxloom/allowed_signers` by the same fleet-management channel that provisions the laptop | **The strongest practical option.** The org already controls the machine image; a key delivered by that channel is as trustworthy as the machine itself. | Requires an org that has such a channel. Useless for individuals and OSS. |
| **C. Explicit CLI add with an out-of-band fingerprint check** | `ctxloom signer add`, user compares `SHA256:…` against a fingerprint published on the org's website / in a signed announcement / read aloud | **The only path with an independent verification step** — and therefore the only one that resists a compromised repo *and* a compromised distribution channel simultaneously. | Depends entirely on the human actually checking the fingerprint. Most will not. This is SSH's own known weakness and we inherit it honestly. |
| **D. Embedded in the binary** *(ctxloom's own keys only)* | compiled-in defaults (§7, location 1) | Trust in the binary. Circular by construction, and that is fine and unavoidable: if you do not trust the binary, nothing it says can help you. | Cannot be used by third parties. Not a general path. |

**Recommended default: (A) for teams and OSS, (B) for enterprises, with (C) as the
verification step anyone security-conscious should perform on top of either.**

The reasoning for (A) as the default deserves to be stated plainly rather than
apologized for: **the project repo is already a trust root you have accepted.** You
cloned it, you will run its code, and its devcontainer will execute as you. A key
in `.ctxloom/allowed_signers` in that same repo does not widen your exposure by one
inch — it is strictly *inside* a boundary you already crossed. Pretending otherwise
would be security cosplay. What (A) does *not* do is protect you from cloning the
wrong repo, and no key-distribution scheme can, because at that point the attacker
*is* your trust root.

**Rejected: TOFU / first-sight prompting.** "This bundle is signed by unknown key
`SHA256:abc…` — trust it? [y/N]" is a prompt fired at the moment the user is least
equipped to answer, triggerable by the attacker at will, and it trains
click-through. Unknown-key content is simply **unsigned content**: it takes the
review path, where a human reads the actual bytes. That is the correct default and
it needs no prompt.

---

## 7A. Signing UX — signing must be the easy path

**The failure mode this section exists to prevent:** signing is even slightly
annoying → publishers ship unsigned → every user gets a review prompt for every
item → review fatigue → click-through → the entire trust model is theater. **An
unsigned bundle is not a neutral outcome; it is a tax levied on every one of that
bundle's users.** Ease of signing is therefore a security property, not a UX nicety.

### 7A.1 The unit of a publisher signature is the BUNDLE FILE — a correction

The proposed CLI shape implies `ctxloom sign <bundle>#fragments/<name>` produces a
signature over that *fragment*. **It cannot, and it must not.** The publisher
signature is over **raw bundle-file bytes** (§3.1) — that is precisely what makes it
parser-independent and transport-agnostic (§3.0). A per-item publish signature would
need a second payload shape over parser-decoded scalars (§3.3), reintroducing the
canonicalization hazard we spent the whole design avoiding.

**Resolution — keep the ergonomic verb, fix the semantics:** `ctxloom sign` accepts
any ref, and **an item ref resolves to its containing bundle and signs that**,
saying so plainly. One verb, one payload shape, no surprise:

```
$ ctxloom sign my-tools#fragments/go-testing
Signing bundle my-tools (contains fragments/go-testing) — signatures cover whole bundles.
  .ctxloom/content/bundles/my-tools.yaml  →  .ctxloom/content/bundles/my-tools.yaml.sig
  signed by ben@abbitt.me (sk-ssh-ed25519, touch required)  ✓
```

### 7A.2 Ref grammar — the proposed examples were malformed; corrected

Checked against `docs/reference-grammar.md` (bundle refs) and ADR 0032.
A bundle ref is `<name>` (bare/local), `<alias>/<name>`, or
`<canonical-url>//bundles/<name>`. **`ctxloom-default` is a remote *alias*, not a
bundle**, so `ctxloom sign ctxloom-default#fragments/go-testing` does not parse as
intended — it would read `ctxloom-default` as a bare *bundle name*. Corrected:

```
# WRONG (alias used where a bundle-ref belongs)
ctxloom sign ctxloom-default#fragments/go-testing
ctxloom sign ctxloom-default

# RIGHT — <alias>/<bundle>, or fully qualified
ctxloom sign ctxloom-default/go-tools#fragments/go-testing     # → signs bundle go-tools
ctxloom sign ctxloom-default/go-tools                          # whole bundle
ctxloom sign ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/go-tools
ctxloom sign my-tools                                          # bare = local bundle (the common case)
```

`ctxloom sign` reuses the **existing** item-ref parser
(`operations.parseTrustItemRef`, `internal/operations/trust.go`), which already
implements exactly this grammar for `trust`/`blacklist`. **No second grammar is
introduced** — which is the trap ADR 0032 exists to prevent, and which the malformed
examples above show is a live risk.

In practice the overwhelmingly common ref is a **bare local bundle name**: you sign
what you authored, in your own project, before pushing it.

### 7A.3 The CLI

```
ctxloom sign <ref>                  # bundle-ref or item-ref (resolves to its bundle)
ctxloom sign --all                  # every local bundle this project publishes
ctxloom sign --key <path|fingerprint>
ctxloom fragment push <name> --sign # sign at the moment of publishing
ctxloom command push <name> --sign
ctxloom config set sign.default true    # set once, every push signs
```

Top-level verb, mirroring git, where signing is a flag on the producing action
(`git commit -S`, `git tag -s`) and never a separate ceremony (ADR 0027). It is
top-level rather than `bundle sign` partly because `ctxloom bundle` is currently
hidden from `--help`, and a signing story hidden behind a hidden command is not a
signing story.

**`sign.default: true` should be recommended in `init` for anyone who publishes.**
The best signing ceremony is the one that already happened.

### 7A.4 Zero-config key discovery — the resolution chain

The common case must require **no ctxloom configuration at all**. Anyone already
signing git commits with SSH has a key configured, and we find it:

| # | Source | Notes |
|---|---|---|
| 1 | **`git config user.signingkey`** (when `gpg.format = ssh`, or the value is an SSH key path/`key::` literal) | **The zero-config path.** Already set for everyone who signs commits, and they *expect* tools to find it. Try this first, always. |
| 2 | **ssh-agent**, when it holds exactly **one** identity | Unambiguous → use it. |
| 3 | **`sign.key`** in ctxloom config, or **`--key`** | Explicit override; wins over 1 and 2 when given. |

The chain itself is one implementation (`internal/signing/agentkey.Discoverer`) and
both `ctxloom sign` and `ctxloom review` resolve through it.

> **PARTIALLY IMPLEMENTED.** Row 3 is honored by **`ctxloom sign`** (which passes
> `--key`, falling back to the `sign.key` config default, into the chain) and **not
> by `ctxloom review`**: `resolveReviewSigner` passes an empty explicit key, so
> `review` resolves via git config → ssh-agent only. An operator therefore **cannot
> override key discovery on `review`** — the command that actually countersigns. On a
> machine whose agent holds more than one identity and whose git config names no
> signing key, `review` fails to resolve rather than letting the user say which key
> to use. (trust-model.md Known gap 6.)

**Ambiguous (agent holds >1 identity, and no git signingkey):** do **not** guess.
Signing with the wrong identity produces a signature nobody trusts and the publisher
will not notice until a user complains. Fail with the choice made trivial:

```
ctxloom: multiple keys in ssh-agent — which should sign?

  SHA256:8Ja1…q0Zc  sk-ssh-ed25519  (yubikey)
  SHA256:pQ2v…7mXd  ssh-ed25519     (id_ed25519)

Pick one, and make it stick:
  ctxloom config set sign.key SHA256:8Ja1…q0Zc
  git config gpg.format ssh && git config user.signingkey ~/.ssh/id_ed25519.pub
```

**Empty (no key anywhere):** actionable error naming the fix, and **never a silent
unsigned publish**:

```
ctxloom: cannot sign — no signing key found.

  Looked for: git config user.signingkey, then ssh-agent identities.

  ssh-add ~/.ssh/id_ed25519            # load a key you already have
  ssh-keygen -t ed25519-sk             # or a hardware key (recommended)

Publishing unsigned means every user of this bundle must review it by hand.
To publish unsigned anyway: ctxloom fragment push my-frag --no-sign
```

**Normative: `--sign` (or `sign.default`) that cannot find a key is a HARD ERROR.**
It never degrades to an unsigned publish. An accidentally-unsigned bundle is the
one outcome we most need to prevent, because the publisher pays nothing for it and
every user pays.

### 7A.5 Companion loadout signing (taskloom, ltk, reprise, third parties)

Companions sign their loadout bytes at **build time**, and the signature is embedded
in the binary. The private key never ships and never runs on a user's machine.

**Our companions: shipped and signed.** `cmd/ltk/loadout.yaml.sig` and
`cmd/taskloom/loadout.yaml.sig` are committed, and both verify against the embedded
publish key under `publish.v1.ctxloom.dev`. `<bin> loadout --format json` emits the
envelope (§4.3) by echoing two embedded artifacts — it computes nothing and holds no
key.

Two caveats on the *pipeline*, both real:

- **The signature is committed, not produced by the release build.** There is no
  `before` hook in the release pipeline that re-signs `loadout.yaml`; the `.sig` is a
  checked-in artifact that a human refreshed with `ssh-keygen -Y sign`. This is fine
  while the loadout changes rarely and a stale pair fails closed (a loadout edited
  without re-signing simply stops verifying, and its content takes the review path) —
  but it means **an unsigned or stale loadout is a silent possibility at any commit,**
  and only a test guards it.
- **The `.sig` is embedded via a wildcard `//go:embed loadout.yaml*`, deliberately**,
  because a literal `//go:embed loadout.yaml.sig` fails the build outright when the
  file is absent. The wildcard keeps the signature *optional* — which is what allows
  an unsigned build to exist at all. That is the correct trade for a third-party
  companion (§4.3: absence means unsigned, which is legal), but for *our* companions
  it means the build will not stop us shipping unsigned.
- **The key is not per-companion** (§6): both loadouts are signed by the same single
  embedded ctxloom key, not by distinct taskloom/ltk keys.

**Third-party companion authors are first-class and must have a documented one-liner.**
If signing a loadout is hard, third parties ship unsigned and every user of them eats
a review prompt. The whole flow, published as a copy-pasteable snippet:

```
# build time, in your release pipeline:
ssh-keygen -Y sign -f ci_signing_key -n publish.v1.ctxloom.dev loadout.yaml
# → loadout.yaml.sig ; embed both with go:embed, emit them from `loadout --format json`
```

That is the entire integration contract: **`ssh-keygen -Y sign`, one namespace
string, two embedded files.** It requires no ctxloom dependency, no ctxloom
tooling, and no coordination with us — which is exactly what a public contract
should require. `ctxloom loadout verify <bin>` is provided so an author can check
their own work before shipping.

### 7A.6 Org / team publishing — a documented first-class flow

> **PLANNED — not yet implemented**, for the same reason as §4.4: nothing verifies a
> `.sig` on a filesystem load path, so an org's signed content dropped onto a
> developer's machine is not verified as signed. The *commands* below exist
> (`ctxloom sign --all`, `ctxloom review --project`), and the committable stores exist
> — what is missing is the verification step at the receiving end.

Not inferred, not improvised. The three commands an org runs, and the three files it
ships (§4.4, §9.2.1):

```
# 1. sign the content (org key, in CI)
ctxloom sign --all --key $ORG_SIGNING_KEY

# 2. review once, centrally, countersigning with the reviewer's key
ctxloom review --project

# 3. publish the trust root alongside the content
#    .ctxloom/allowed_signers   ← org publish key + reviewer approve key
#    .ctxloom/approvals/        ← the reviewer's countersignatures
#    (both committed; see §7.3 for how the trust root reaches the developer)
```

Every developer who clones gets verified, pre-approved content with **no key of their
own, no prompts, and no secrets** (§9.5).

---

## 8. The new decision function

Implemented as `operations.EffectiveTrust` (`internal/operations/trust.go`).
First-match-wins. Fail-closed. The request gains one field —
`Signer` (the verified publisher identity attached to the item's source document at
load, or `builtin:ctxloom`, or empty for unsigned) — and the store lookups become
signature verifications.

```
EffectiveTrust(ref, payload_bytes, form, signer) -> (Decision, Source)

1. REJECTED → DENY   (source: rejected)
     a valid reject-countersignature exists over THIS ref            (ref-reject), OR
     a valid reject-countersignature exists over THESE payload bytes (content-reject),
     from any key trusted for the reject namespace.
   -- Checked FIRST, ahead of every allow. A user can reject content signed by the
      ctxloom release key, and can reject a builtin. Rejection is supreme.

2. LOCAL → ALLOW     (source: local)
     ref.IsLocal — authored in this project. Unchanged from today.

3. BUILTIN → ALLOW   (source: builtin)
     signer == "builtin:ctxloom" — compiled into this binary. Authenticated BY the
     binary (§4.5); reachable by step 1's veto, which is the change from today.

4. TRUSTED SIGNER → ALLOW   (source: trusted-signer)
     signer is non-empty AND signer's key is trusted for the publish namespace.
   -- This REPLACES the trust_bundles hash-blind source bypass. Trust is keyed by
      the signing identity, not by the repo the bytes arrived from.

5. APPROVED → ALLOW  (source: approved)
     a valid approve-countersignature exists over THESE payload bytes, for THIS
     ref and THIS form, from a key trusted for the approve namespace.

6. OTHERWISE → DENY  (source: pending)
     Withheld. Counted toward the startup notice. This is where unsigned content
     lands until a human reviews it, and where signed-but-untrusted-key content
     lands, and where content whose bytes changed lands.
```

**Red line 1 holds:** no state was added. `signer` is an *input* to the decision,
never a state of the item. An item is pending, approved, or rejected — the same
three states. A signed item with an untrusted key is not a fourth thing; it is
pending.

**Red line 2 holds:** step 1 is still step 1 and still checks a rejection before
any signature can allow anything. Note carefully that step 1 must be evaluated
**even when the publisher signature failed to verify or is absent** — a rejection
of bytes is a rejection of those bytes regardless of their provenance.

**Red line 3 holds:** nothing here reads `lock.yaml`, and nothing writes it. The
lockfile pins dependencies (ADR 0033) and is not a security surface. Verification
happens at the **exposure choke** — `Loader.gateContent` — not at pull or lock
time. Concretely: `remote pull` and `remote upgrade` do not verify signatures and
do not care about them; they move SHAs. A pull of an unsigned, or badly-signed, or
rejected bundle **succeeds**, and its content is then withheld at exposure.

### 8.1 Plumbing the signer to the gate

`ContentGate` (`internal/bundles/loader.go`) is:

```go
type ContentGate func(ref string, payload []byte, form, signer string) bool
```

The `Bundle` struct carries a signer alongside its existing `sourceRef`, stamped at
parse time by the load path from the verified `.sig` — the *same* place `sourceRef`
is stamped (`Bundle.StampSigner` / `Bundle.Signer`). `Loader.gateContent` passes it
through. The hash is not the gate's input; the **bytes** are (the gate hashes them
itself, for the store index — §9.3).

Every load path that produces a `Bundle` must stamp `signer`. Today two do: the
seeded remote path (`config.loadRemoteBundleSeed`, which is where
`remote.LoadAllBytes` hands over `map[canonical][]byte` and is therefore the natural
place to verify, since **the raw bytes are in hand there and nowhere else**), and the
companion loadout path. **The plain filesystem path does not** — which is the whole
of the §4.4 gap.

A load path that forgets to stamp it yields `signer == ""` → unsigned → review path.
**Fail-safe by construction: the failure mode of forgetting is "more review", never
"more exposure"** — which is exactly why the §4.4 gap is a missing *feature* and not
a vulnerability.

---

## 9. Keys — the honest analysis

### 9.1 The countersigning key lives in ssh-agent. ctxloom never touches key material. (question a)

**Normative:** countersigning is performed with the user's **existing SSH key, held
by `ssh-agent`**, reached over `SSH_AUTH_SOCK`. ctxloom **does not generate, does
not store, does not read, and does not prompt for a private key.** There is no key
material under `~/.ctxloom`, ever, and no passphrase prompt of our own. The user's
identity is `git config user.signingkey` when it names an SSH key, else the single
key in the agent, else an explicit `--key` selecting one of the agent's identities
by fingerprint.

This is the right call — it means a ctxloom compromise cannot leak a key we never
had, and it inherits ssh-agent's whole ecosystem (confirmation, FIDO2, PKCS#11,
remote forwarding policy) for free.

**It also, by itself, delivers NO anti-self-approval property. This must not be
glossed.**

`ssh-agent` is a *signing oracle*. Any process holding `SSH_AUTH_SOCK` may ask it
to sign arbitrary bytes under any namespace. It cannot exfiltrate the key — and it
does not need to. It needs one signature, and a bare agent gives it one, silently.
The coding agents this feature defends against are *exactly* the processes that
hold `SSH_AUTH_SOCK` on a host run: the host runner (`internal/lm/isolation/runner.go`)
builds the child environment as `cmd.Env = append(os.Environ(), kv...)`, so a host-run
agent inherits the full parent environment — **`SSH_AUTH_SOCK` included. Nothing
strips it, and nothing gates it.** That is the deliberate decision of §9.1.1, not an
oversight.

So the honest grading. **The property is a function of the agent's confirmation
policy, not of where the key lives:**

| Posture | Setup | Can a host-run coding agent forge your approval? |
|---|---|---|
| **P0 — no key / no agent** | unsigned approval records (§9.5) | **Yes.** Writes the record directly. Identical to today's `trust.yaml`. |
| **P1 — bare ssh-agent** (`ssh-add`) | the common developer default | **YES — silently.** It holds `SSH_AUTH_SOCK`, asks the agent to sign the approve payload, gets a valid signature, and self-approves. *The key never leaves the agent, and the attack works anyway.* **This posture provides no protection and must never be described as if it does.** |
| **P2 — confirm-before-use** (`ssh-add -c`) | one flag when adding the key | **No, not silently.** Every signature raises an `SSH_ASKPASS` confirmation naming the request. A background agent trying to self-approve produces a dialog the user never asked for — which is both the block and the alarm. **This is the minimum bar for the property to be real, and it is the recommended default posture.** |
| **P3 — hardware-backed key** (`sk-ssh-ed25519`, `sk-ecdsa-sha2-nistp256`, touch required) | `ssh-keygen -t ed25519-sk` | **No.** Signing requires a physical touch on the token. No software process can produce it. **Gold standard; recommend it to anyone who cares.** |
| **P4 — containerized agent** | ctxloom's existing container isolation | **No — structurally.** The agent has no `SSH_AUTH_SOCK` at all (below). |

**P2 is the default we recommend and the one `ctxloom review` should nudge toward.**
Detecting it is cheap: an agent identity added with `-c` is flagged in the agent's
identity list, and ctxloom can warn once, at first countersignature, when the key
it is about to use is *not* confirm-guarded and *not* hardware-backed:

> `ctxloom: your signing key is in a bare ssh-agent — any process with SSH_AUTH_SOCK,
> including agents ctxloom launches, can sign approvals as you. Re-add it with
> 'ssh-add -c' (confirm each use) or use a hardware key.`

**The warning ships** (`warnIfSoftwareKey`, fired by `ctxloom review` before it
countersigns), and it is the difference between a security feature and security
theater. What does **not** ship is its *persistence* — see §9.1.2, which specifies a
recorded acknowledgment that this spec originally called a hard requirement and which
the code does not yet satisfy. The warning currently fires **once per `review`
invocation**, never blocking.

**P3 costs us nothing to support.** FIDO2 signing is native to `ssh-keygen -Y sign`;
we do not write a line of code for it. "Touch your key to approve a fragment" is
precisely the ceremony this trust model wants, and getting it for free is a genuine
argument for SSH signatures over a hash ledger. Put it in the README.

**P4 — containerized agents structurally cannot countersign. This is intended
behavior, specified, not an accident.** The container path forwards env by a
*scoped name-allowlist* (`envPassthrough` in `internal/lm/isolation/auth.go` —
it carries names, not the whole environ) and does not mount the agent socket, so
`SSH_AUTH_SOCK` is absent inside the container and there is no signing oracle to
reach. **Do not "fix" this by forwarding the socket into containers.** A
containerized agent that cannot approve its own content is the correct default and
one of the strongest arguments for running agents in containers at all. Any future
work that plumbs the agent socket into a container must be treated as a
security-relevant change to this spec, not a convenience.

### 9.1.1 DECIDED: `SSH_AUTH_SOCK` is NOT stripped from host-run agents

**Decision (final): host-run agents keep `SSH_AUTH_SOCK`.** Stripping it would break
legitimate agent workflows — `git push` over SSH, signing its own commits — and
those are real and common. We do not break them.

**The consequence, stated without euphemism:**

> **The anti-self-approval property is OFF BY DEFAULT.**
>
> A user who has not run `ssh-add -c` and is not using a hardware key is in posture
> **P1**. Their coding agent holds `SSH_AUTH_SOCK`, can ask ssh-agent to sign an
> approval payload, and will get a valid signature. **That agent can forge its own
> approvals — exactly as it can today by writing `trust.yaml`. For that user, on
> that axis, this feature buys nothing.**
>
> The property exists **only** for users who configure confirm-before-use
> (`ssh-add -c`) or a hardware token — or who run their agents in containers, where
> it holds structurally.

This is a deliberate trade: keep the workflows, and make the security posture a
**choice the user is told about** rather than a default we silently break things to
enforce. The spec's job is then to *tell them*, which is §9.1.2 — and that warning
is not a nicety, it is the load-bearing part of the honesty.

What the feature still buys a P1 user, so the picture is complete: publisher
authentication (steps 3/4 — content from ctxloom, an org, or a team lead is
verified, and that is worth a great deal on its own), portable/inheritable
approvals (§9.2), tamper-evidence for content in transit, and rejection that
survives rename. **The one thing it does not buy them is protection from their own
agent.** Everything else in this document holds for them.

### 9.1.2 Posture detection and the one-time warning (design)

> **PARTIALLY IMPLEMENTED.** What ships: the hardware-key detection below, and the
> warning itself (`warnIfSoftwareKey`), fired by `ctxloom review` when the key it is
> about to countersign with is not an `sk-*` key. It is a warning, never a block.
>
> What does **not** ship: **the persistence.** There is no `approvals.posture` config
> key, no `[c]/[p]/[q]` prompt, and no recorded acknowledgment. The warning therefore
> fires **once per `ctxloom review` invocation**, not once ever.
>
> **This section originally called the persisted acknowledgment "a hard requirement
> of this spec". It is not met.** The honest reading of that, given §13's own
> argument that a repeated security warning is a security warning that gets ignored:
> the warning is *present* but its design is *unfinished*, and a warning the user
> cannot dismiss by fixing their posture is on the road to becoming noise. This is a
> real shortfall, not a cosmetic one, and it is tracked as Known gap 5 in
> trust-model.md.

**What we can detect, honestly:**

| Signal | Detectable? | How |
|---|---|---|
| Key is **hardware-backed** | **YES — the key type is self-identifying** | The public key algorithm is literally `sk-ssh-ed25519@openssh.com` or `sk-ecdsa-sha2-nistp256@openssh.com`. We already hold the public key (from the agent's identity list, or from `allowed_signers`). `golang.org/x/crypto/ssh` parses both (`KeyAlgoSKED25519` / `KeyAlgoSKECDSA256`) and implements `Verify` for both. Zero work for us. |
| Key is **confirm-required** (`ssh-add -c`) | **NO** | The ssh-agent protocol does not expose per-identity constraints. `agent.Agent.List()` returns key blob + comment, nothing else. There is no request that asks "is this key confirm-guarded?" **I looked for another honest signal and there is none.** Timing a signature to infer a confirm dialog is a heuristic that would be wrong for anyone with a slow ASKPASS or a fast click, and inferring security posture from a stopwatch is not something I am willing to specify. |
| Touch-required on an `sk-` key | **NO** (assumed) | A key generated with `-O no-touch-required` is indistinguishable from a touch-required one by its public blob. Touch-required is the `ssh-keygen` default; we assume it and say so. |

**Therefore the design: detect what is knowable (hardware), and let the user
*declare* what is not (confirm).** *(The detection ships; everything from here to the
end of §9.1.2 — the config key, the prompt, the `status` line — is design, not
behavior.)*

`approvals.posture` in config, one of:
- `unknown` (default, never written by us — it is the absence of a declaration)
- `confirm` — "my key is `ssh-add -c` guarded" (self-attested)
- `hardware` — auto-set when we observe an `sk-*` key; never needs attesting

**Where the warning fires:** **once, at the start of a `ctxloom review` session that
is about to countersign** — never on `run`, never on assembly, never on every
operation. Review is the only moment the user is thinking about trust, is present
at a TTY, and is about to use the key. It fires when the key we are about to sign
with is **not** `sk-*` **and** `approvals.posture` is not `confirm`. After the user
answers, we record their declaration and **never ask again** (a security warning
that repeats is a security warning that gets ignored).

**Exact message:**

```
Your approval key is a software key held in ssh-agent.

  SHA256:8Ja1xkV9…q0Zc   ssh-ed25519   (id_ed25519)

Any process holding SSH_AUTH_SOCK — including the coding agents ctxloom
launches — can ask your agent to sign with this key. It cannot read the key,
and it does not need to: it can simply request a signature. That means an
agent can approve content for itself, which is the thing approvals exist to
prevent.

Two fixes, either one closes it:

  ssh-add -c ~/.ssh/id_ed25519      confirm each use — you get a prompt, the
                                    agent gets nothing without your click
  ssh-keygen -t ed25519-sk          hardware key — signing needs a physical touch

Or run your agents in containers, where SSH_AUTH_SOCK is never forwarded.

  [c] I've set up confirm-before-use (ssh-add -c) — don't warn me again
  [p] Proceed anyway — I understand my agents can approve as me
  [q] Quit and fix this first
```

**It is a warning, never a block.** `[p]` proceeds and records
`approvals.posture: acknowledged` so the warning does not recur. The user chose
this posture deliberately; our job is to make sure they chose it *knowingly*, and
then get out of the way.

`ctxloom status` shows the posture line permanently — that is where it stays
visible without nagging:

```
Approvals:  signed by ben@abbitt.me (ssh-ed25519, software key in ssh-agent)
            ⚠ agents you launch can sign as you — see 'ctxloom help approvals'
```

versus, for `sk-*`:

```
Approvals:  signed by ben@abbitt.me (sk-ssh-ed25519, hardware — touch required)
```

### 9.2 Where do countersignatures live? (question b)

**Both stores. They answer different questions and neither subsumes the other.**

- **User store — `~/.ctxloom/approvals/`.** "My approvals follow me." Personal
  decisions, across every project. This is the default write target for
  `ctxloom review`.
- **Project store — `.ctxloom/approvals/`, committable.** "Our team's approvals."
  A lead reviews, commits signatures, CI and every developer inherit them without
  re-reviewing — *provided* they trust the lead's key, which the project
  `allowed_signers` (§7) distributes in the same commit. This is the CI story that
  `docs/trust-simplify-plan.md` left open. `ctxloom review --project` writes
  here.

**Composition — how the two stores combine, precisely:**

At gate time both stores are read and **unioned into one candidate set**. There is
**no precedence between the stores** — precedence lives in the *decision function*,
not in the filesystem. A signature is a signature no matter which store it was
found in; what decides the outcome is §8's step order.

The conflict cases, resolved:

| Case | Outcome | Why |
|---|---|---|
| Project store approves X; user has personally **rejected** X | **DENY (rejected)** | Step 1 is evaluated before step 5 and unions rejections from *both* stores. **A personal rejection beats an inherited approval — always.** Rejection is supreme, and it is supreme across stores, not merely within one. |
| User approves X; project store **rejects** X | **DENY (rejected)** | Same rule, symmetric. A rejection anywhere, from any trusted key, wins. This is intentional: an org must be able to blacklist content its developers cannot un-blacklist locally by approving it. |
| Project store approves X; user has *not* reviewed X | **ALLOW (approved)** — *iff* the signing key is trusted for the approve namespace | This is the inheritance, and it is the point of the feature. |
| Project store approves X, signed by a key **not** in the user's `allowed_signers` | **pending** | Inert. Not an error, not a warning — just not an approval. |
| Both stores approve X | ALLOW | Trivially. |

**Rejection being supreme across stores has one sharp edge worth naming:** a user
who wants to *locally override* an org-wide rejection cannot, by design. Their
recourse is to remove the org's reviewer key from their `allowed_signers`, which
drops **all** of that key's decisions — approvals and rejections together. That is
coarse (see §10.3, same root cause), and it is the correct trade: the alternative
is a per-item "I override my org's security team", which is precisely the escape
hatch an attacker would aim the agent at.

**The project store being committable is not a new attack surface.** An approval
signed by a key that is not in *your* `allowed_signers` is inert. "Commit a
malicious approval" requires the victim to have already trusted the attacker's key
— at which point the attacker did not need the approval, they could have signed the
content itself. The store is safe to commit precisely because its authority comes
from the key, not from its location on disk.

### 9.2.1 The CI / non-interactive flow — concretely

This closes the open item recorded in `docs/trust-simplify-plan.md` (now superseded —
see its banner): "non-interactive / CI story beyond committed `trust.yaml`".

**CI holds no key, signs nothing, and approves nothing. It only verifies.** That
asymmetry is the whole answer, and it is why no secret is needed:

```
repo/
  .ctxloom/allowed_signers      # committed: names the org publish key + the reviewer's approve key
  .ctxloom/approvals/           # committed: the reviewer's countersignatures
```

1. A human reviewer runs `ctxloom review --project`, approving with their key
   (ssh-agent, ideally `-c` or hardware — §9.1). Signatures land in
   `.ctxloom/approvals/`.
2. They commit `.ctxloom/approvals/` and `.ctxloom/allowed_signers`.
3. CI clones and runs. Every item is either publisher-trusted (step 4) or covered
   by a committed approval signed by a key in the committed `allowed_signers`
   (step 5). **Nothing is pending. No prompt, no TTY, no secret, no key on the
   runner.**
4. If a bundle is upgraded and its bytes change, the committed approval no longer
   verifies, the item goes **pending → withheld**, and CI's context is quietly
   missing it. **Recommended: CI sets strict mode** so pending becomes a *fatal*
   finding (`strictness.ClassTrust`) rather than a silent withhold — a red build
   telling a human "review this" is exactly the intended behavior, and it is the
   `env override to treat pending as fatal` that trust-simplify-plan left open.

The same three files are what an **org** ships (§4.4) — a team lead and an
organization are the same actor at different scale, which is the sign that the
design is factored correctly.

**Format** — one file per signature is simplest and merge-conflict-free, which
matters for a committable, team-written store:

```
.ctxloom/approvals/
  <index_hash>.<assertion>.<key_tag>.sig
```

where, normatively (`internal/signing/countersign/store.go`):

```
index_hash := hex(sha256( CountersignPayload(header, payload_bytes) ))   # the FULL FRAMED payload — §3.2
key_tag    := hex(sha256( pub.Marshal() ))[:12]                          # 12 hex chars of the signer's public key
assertion  := "approve" | "reject"
```

**The hash covers the full framed payload — header and bytes — not the bare payload
bytes.** This is not an optimization and it is not incidental; an earlier draft of
this spec specified `sha256(payload_bytes)` and **that scheme is broken**:

- it collides an **approve-raw** with an **approve-distilled** signature over
  identical content at the same ref (the bytes are the same; only the header's
  `form` differs), and
- it collides **every ref-reject onto a single filename**, because a ref-reject's
  payload is always empty (§5.3) — so every ref-rejection in a store would share one
  name.

In both cases the second write silently clobbers the first. Hashing the framed
payload makes the key unique per `(assertion, ref, form, bytes)` tuple — and since
the form names the item's ROLE, that granularity separates byte-identical items of
different kinds, which is exactly the granularity a countersignature is scoped to. **Do not "simplify" this
back.**

The `key_tag` disambiguates multiple signers countersigning the same content — an
org's reviewer key and a second maintainer's key approving the same item. It is
**untrusted** and is never read back as identity; identity always comes from
re-verifying the signature blob.

The filename is an **index, never an authority** (§9.3). The `.sig` file contains the
armored SSH signature. The signed payload is reconstructed at verify time from the
header fields + the item's live bytes, and the index hash is recomputed from *that*
— never parsed out of a filename read off disk. This is why a tampered filename gains
an attacker nothing: the candidate must still verify.

**Unsigned records** (the §9.5 degraded path) use the same index hash with the
`.unsigned` extension and no key tag: `<index_hash>.<assertion>.unsigned`. Existence
is the entire record; it carries no cryptographic weight, and it must **never** be
written to the committable project store.

A sidecar `index.yaml` MAY cache `{ref, kind, form, principal, reviewed_at}` for
`ctxloom approvals list` to render without verifying everything; it is **untrusted
display metadata** and must never be an input to steps 1–6.

Its `kind` and `form` fields are DISPLAY labels — the item's live kind and its plain
LAYOUT form (`raw`/`distilled`) — deliberately not the attestation vocabulary the
preimage binds, and lookups into it key on `(ref, layout form)` only. That is what
makes a contract bump (§12) land as STALE rather than ABSENT: a record framed under
a superseded contract can never verify again, but "a human approved something here
once" is still true, and the index is the only thing left that can say so, which is
what labels the item an UPDATE to re-review instead of a first-time item. Such a
record is only ever REPORTED, never re-keyed onto the current vocabulary —
re-keying would have to guess the role it was approved in.

### 9.3 What is left of the content hash? (question c)

**The hash stops being the authority and becomes the index. It does not vanish, and
pretending otherwise would be dishonest.**

- **Index:** `sha256(payload_bytes)` is how the gate finds *which* signature files
  are even candidates for these bytes, in O(1), instead of trying to verify every
  signature in the store against every item. At realistic sizes this is the
  difference between a map lookup and a quadratic scan.
- **Not the authority:** finding a candidate file proves nothing. The signature is
  then verified over the reconstructed payload. A hash collision, a renamed file, a
  hand-crafted index entry — none of them produce an approval, because none of them
  produce a valid signature. **A hash match with an invalid signature is pending,
  not approved.** *(Implementer trap #2: it is very easy to write the fast path as
  "hash found in store → allow" and never reach the verify. That is the whole bug,
  and it silently restores exactly the forgeable-file weakness we are removing.)*
- The `content_hash` field authored in bundle YAML remains what it is today:
  distillation bookkeeping, never trusted for anything security-relevant.

### 9.4 The preconditions on the anti-self-approval property — read this

The "an agent cannot approve content for itself" property is **conditional**, and
the conditions must be stated wherever the property is claimed:

1. **The agent cannot obtain a signature from your key.** Note the wording: *not*
   "cannot read the key" — reading is irrelevant, since ssh-agent will sign on
   request for anyone holding the socket. Satisfied by **P3** (hardware, touch)
   unconditionally, by **P2** (`ssh-add -c`) modulo the user actually reading the
   confirmation dialog before clicking it, and by **P4** (containerized: no socket).
   **Not satisfied by P1** (bare agent), which is the common developer default and
   therefore the posture most users will be in unless we warn them (§9.1).
2. **The agent cannot write `allowed_signers`.** This is the sharp one. An agent
   that can append its own public key to `~/.ctxloom/allowed_signers` can then
   generate its own key, approve anything with it, and be believed. **Signatures do
   not fix a writable trust root.**

So the honest scope: **the property is strong for a containerized agent** — no
`SSH_AUTH_SOCK`, no mounted `~/.ctxloom`, so neither precondition can be violated —
**strong on a host with a hardware key or `ssh-add -c`**, and **absent for a
host-run agent with a bare ssh-agent and unrestricted file-write**, where the agent
can either ask your agent to sign or simply add its own key to the trust root.

The feature's guarantee is therefore *coupled to the posture*, and the docs must
say so rather than implying signatures are magic. The two postures that make it
real — a container, or a touch-required key — are both cheap, and both should be
recommended loudly.

### 9.5 The degraded path: no ssh-agent, no key, headless (question d)

**Signing must never become a barrier to plain local use, and it does not.
Verification needs no key; only *making* an approval does.** That asymmetry carries
almost every no-key case:

| Situation | Needs a key? | What happens |
|---|---|---|
| Authoring fragments in your own project | **No** | `ctxloom:local` is first-party, allowed at step 2. Never gated. This is the overwhelmingly common case and it is untouched by this entire spec. |
| Using builtin bundles | **No** | Allowed at step 3. |
| Using `ctxloom-default` | **No** | Signed by *our* key, verified against the **embedded public key**. Public-key asymmetry: you verify without possessing anything. |
| Using an org's / team lead's published + approved content | **No** | Steps 4/5. The org made the signatures; you only check them. This is why the enterprise flow (§4.4) puts **no key on the developer's machine**. |
| CI / headless runners | **No** | CI verifies, never approves (§9.2.1). No agent, no socket, no secret. |
| **Approving third-party unsigned content yourself** | **Yes** | The only case. See below. |

**When there is genuinely no agent and no key** (no `SSH_AUTH_SOCK`, or the agent
holds no usable identity), `ctxloom review` must not hard-fail. It:

1. **Detects it up front**, before showing the first item — never after the human
   has read twenty fragments and pressed `[a]`. A review session that cannot record
   its result is a waste of a human's attention and an insult besides.
2. Explains the two real options: start an agent and add a key (`ssh-add -c ~/.ssh/id_ed25519`),
   **or** proceed with **unsigned approval records** — the current `trust.yaml`
   design, verbatim — behind an explicit, persistent opt-in
   (`approvals.unsigned: true`, set by a confirmation that names the consequence).

The consequence, printed at that confirmation and again in `ctxloom status`:

> *"Approvals will be recorded UNSIGNED. Any process that can write `.ctxloom/` —
> including the coding agents ctxloom launches — can approve content on your behalf.
> This is the same protection level as ctxloom before signing existed."*

This is **exactly as safe as today, and no less**. It is a labelled degradation, not
the default, and it keeps the door open for the user who simply does not want a
key — a real user, whose refusal is legitimate, and who should not be locked out of
their own tool over it.

**Unsigned approvals are strictly local.** An unsigned approval record must **never**
be written to the *project* store and must never be inherited by anyone: a shareable
approval with no signature is a forgery primitive with a friendly name. `ctxloom
review --project` therefore **requires** a key and refuses to run without one.

### 9.6 Performance (question e)

Ed25519 verification is ~50µs; the SHA-512 over a few KB is noise. The real costs
are file reads and the store scan, not crypto.

Realistic session: ~10-30 bundles, ~50-200 exposed items.

| Work | Count | Cost |
|---|---|---|
| Publisher sig verify | once per **bundle** (not per item) | 30 × 50µs = **1.5ms** |
| Approve/reject countersig verify | once per exposed item, only for items reaching steps 1/5 | 200 × 50µs = **10ms** |
| `allowed_signers` parse | once per process | negligible |
| Store index build (readdir) | once per process | one `readdir`, ~ms |

**Worst case is well under 20ms on a cold session.** For comparison, the boot-time
companion probe (`companionProbeTimeout`, `internal/config/companions.go`) budgets **3 seconds**. This is
two orders of magnitude below anything the user can perceive, and it is dwarfed by
the git object reads happening alongside it.

**Recommendation: no verification cache. Verify every time.** A cache of
"signature X verified OK" is a file, and a file is forgeable — a persisted
verification cache would re-introduce the exact weakness (a writable file that
grants exposure) that this design exists to remove. In-process memoization per
`(payload_hash, assertion)` for the duration of one process is fine and free; the
publisher signature is naturally memoized already by verifying once per bundle
rather than once per item. **Do not persist verification results to disk.**

---

## 10. Failure modes

### 10.1 Unsigned content

Legal, ordinary, and the common case today. No `.sig` → `signer == ""` → falls
through steps 3/4 → lands at step 5 (approved?) or step 6 (pending). **Unsigned
content is not "untrusted content"; it is content that a human must review.** Most
bundles in the world will be unsigned for a long time and everything must work for
them. Local authoring requires no key (§9.5).

### 10.2 Signature present but invalid

A `.sig` exists and does not verify — a corrupt blob, a truncated file, a key not
in `allowed_signers`, or an actual attack.

**Two different cases, two different behaviors:**

- **Signed by a key not in `allowed_signers`** — that is just *unsigned content to
  you*. Not an error. `signer` is left empty (with a debug-level note), and the item
  takes the review path. This is the third-party-publisher case and it must be
  quiet: a scary error for every unknown publisher trains users to ignore errors.
- **Signature structurally invalid, or valid signature by a trusted key over
  DIFFERENT bytes** — this is a tamper signal and it is **loud**. The bundle is
  withheld entirely (not merely un-attributed), and it is a **fatal-class finding
  in strict mode** via `strictness.Fail(strictness.ClassTrust, ...)`, the same
  channel `EffectiveTrust` already uses for an unreadable trust store
  (`internal/operations/trust.go`). "The bytes do not match the signature that
  claims to cover them" is never a benign condition, and silently degrading it to
  "unsigned, please review" would let an attacker downgrade a signed bundle to an
  unsigned one by corrupting its signature.

### 10.3 Key revocation — the coarse part, stated honestly

Because sshsig carries **no trusted timestamp**, we cannot ask "was this signature
made *before* the key was compromised?" `allowed_signers`' `valid-after` /
`valid-before` options only compare against a **verifier-supplied** time
(`ssh-keygen -Y verify -Overify-time=...`), which the attacker's signature does not
constrain.

Consequence, plainly: **removing a key from `allowed_signers` invalidates every
approval that key ever made.** Those items all return to pending and must be
re-reviewed. There is no fine-grained "revoke as of date T".

This is acceptable — a compromised approval key means every approval it made is
genuinely suspect, and a mass re-review is the *correct* response, not a regression
— but it is a real operational cost and the docs must not hide it. A future
upgrade path (Sigstore/Rekor, or any transparency log) is precisely a system that
adds trusted time; that is the strongest argument for the cosign migration later.

### 10.4 Re-distill invalidates approvals — fail safe, and say so LOUDLY

**The binding is not weakened.** A re-distill that changes an item's distilled bytes
returns that item to **pending**, because the bytes the agent will see genuinely
changed and **no human has ever approved those bytes**. A distiller is an LLM
rewriting content that will be fed to an LLM; "the distiller changed it, so it's
fine" is exactly the reasoning we must refuse. The approval binds to the exposed
form (§5.2), and the exposed form changed. Full stop.

What must change is that this is currently *silent*: the user re-distills, and later
discovers a pile of re-review with no explanation of where it came from. **Silent
re-review is how a security ceremony becomes noise.**

**Normative: the invalidation is reported at the moment it is caused — at the end of
the `distill` run, by the command that did it**, not at the next assembly:

```
$ ctxloom distill --all
Distilled 12 items in 3 bundles.

⚠ 7 approvals invalidated.

  Re-distilling rewrote the DISTILLED form of these items. Your approvals covered
  the previous bytes — the agent would now see text nobody has reviewed, so they
  are back to pending and are withheld until you review them.

    go-tools#fragments/go-testing        (distilled form changed)
    go-tools#fragments/go-errors         (distilled form changed)
    …5 more

  Review them:            ctxloom review
  See exactly what moved: ctxloom review --diff
  (Raw forms are unaffected — their approvals still stand.)
```

Three properties this message must have, and they are the whole design:

1. **It fires at the cause, not at the symptom.** The `distill` command names the
   consequence it just created. The user is standing right there.
2. **It explains *why*, in one sentence** — "the agent would now see text nobody has
   reviewed" — because a user who does not understand why will look for a flag to
   turn it off, and we will have taught them to disable the gate.
3. **It offers the diff.** Review of a re-distilled item is a *diff against the
   approved bytes* (the snapshot cache already exists for exactly this,
   `cache/trust/objects/`), so re-approval is a quick read, not a re-read of
   everything from scratch. **This is what keeps the loud path from being a burden.**

The same message fires, in the same shape, from any command that rewrites exposed
bytes — `distill`, a bundle `apply` that re-runs distillation, or a sync that pulls
new distilled content.

**Config note:** flipping `config.use_distilled` does *not* invalidate anything — it
selects which form is exposed, and both forms' approvals are recorded independently
(§5.2). A user who has approved only the raw form and then switches to distilled will
find those items pending, which is correct and which the startup notice already
reports.

### 10.5 Deletion of records is fail-safe

An attacker (or a careless `rm`) who deletes signature files degrades toward
**pending**, which is DENY. Deleting an approval withholds content. Deleting a
rejection returns the item to pending — still withheld. Deleting `allowed_signers`
denies everything. **Every deletion moves toward less exposure.** This is a real
and pleasant property of the design and it should be tested explicitly.

---

## 11. Retiring `trust_bundles`

**`trust_bundles` is deleted. Not deprecated — deleted.** No migration, no shim, no
legacy read path. pre1 has never been released; there are zero users of it, and the
project's standing rule is to break old users rather than carry legacy formats.

| Removed | Replaced by |
|---|---|
| `trust_bundles: true` in `remotes.yaml` | a publisher key in `allowed_signers` |
| `ctxloom remote trust <name>` / `untrust` | `ctxloom signer add` / `signer remove` |
| `EffectiveTrust` step 3 (trusted source → hash-blind ALLOW) | step 4 (trusted **signer** → ALLOW) |
| `remoteTrusted()` (deleted from `internal/operations/trust.go`) | `allowed_signers` lookup |
| `Remote.TrustBundles` field | *(gone)* |
| `trust.yaml` v2 (`items[]` + `denylist[]`) | the countersignature stores (§9.2) |
| `trust.Store` v1→v2 in-memory migration | *(gone — delete it)* |

**The semantic change is the point, not an incidental.** `trust_bundles` trusts a
**location** — everything that repo publishes, forever, hash-blind. Key trust
trusts an **identity** — everything that *keyholder* signs, wherever it appears. A
repo can be compromised, a fork can be substituted, a URL can be typosquatted; a
key cannot be any of those things without being stolen. Trusting "whatever arrives
from this URL" was always the weakest link in the trust-simplify model (it is
listed as Known Gap #3 in trust-model.md: *"Trusted sources are broad"*), and this
retires it.

**What a user who wants their own personal remote auto-trusted now does:** sign
their bundles. Their approval key and their publishing key can be the *same* key —
the namespaces distinguish the assertions. `ctxloom sign` on publish; their
own key is in their own `allowed_signers` for the publish namespace; their content
is allowed at step 4. This is a *better* answer than `trust_bundles` because it
survives someone else pushing to their repo.

`init --remote` no longer flips a trust flag. It offers to add the remote's
publisher key if the remote ships one — showing the fingerprint, requiring an
explicit yes — and otherwise the remote's content simply takes the review path.

---

## 11A. Implementation: the sshsig dependency

**Two parsers are needed, and they are different problems:**

1. the **sshsig signature blob** (armored `-----BEGIN SSH SIGNATURE-----`, PROTOCOL.sshsig wire format), and
2. the **`allowed_signers` file** (principals, `namespaces=`, `valid-after/before`, key blobs).

### 11A.1 Candidates (evidence)

| | `hiddeco/sshsig` | `42wim/sshsig` | `pault.ag/go/sshsig` | in-tree | shell out to `ssh-keygen` |
|---|---|---|---|---|---|
| License | **Apache-2.0** ✓ | Apache-2.0 ✓ | (unverified) | — | — |
| Latest release | **v0.2.0, Apr 2025** (tagged) | **none** — pseudo-version only (`v0.0.0-2026…`) | unverified | — | — |
| Imported by | few | 16 | few | — | — |
| Signature blob parse/verify | **yes** | yes | yes | we write it | yes |
| `allowed_signers` parse | **NO** | **NO** | **NO** | we write it | yes (delegated) |
| `namespaces=` restriction | **NO** (no allowed_signers) | NO | NO | we write it | yes |
| `sk-*` hardware keys | **yes, via `ssh.PublicKey`** | yes, same | yes | yes | yes |
| ssh-agent signing | via `ssh.Signer` (agent client satisfies it) | `SignWithAgent()` helper | — | — | yes |
| Offline / no openssh binary | **yes** | yes | yes | yes | **NO — requires the binary** |
| Deps beyond `x/crypto` | none material | none material | unverified | none | — |

**API of the recommendation** (`hiddeco/sshsig` v0.2.0):

```go
func Sign(m io.Reader, signer ssh.Signer, h HashAlgorithm, namespace string) (*Signature, error)
func Verify(m io.Reader, sig *Signature, pub ssh.PublicKey, h HashAlgorithm, namespace string) error
func Armor(s *Signature) []byte
func Unarmor(b []byte) (*Signature, error)
```

This is an exact fit for both payload shapes: `m io.Reader` over raw bundle bytes
(§3.1) or over the framed countersign payload (§3.2); `namespace` is our assertion
separator (§1); `ssh.Signer` is satisfied by `x/crypto/ssh/agent`'s client, so
**ssh-agent signing needs no extra library** and no key material (§9.1).

### 11A.2 Recommendation — **adopted**

**`hiddeco/sshsig` v0.2.0 is the shipped dependency** (a direct requirement in
`go.mod`), and the `allowed_signers` parser of §11A.3 is ours
(`internal/signing/allowedsigners`). The evaluation below is retained as the
rationale record.

Rationale: it is the only candidate with a **tagged release** (an unreleased
pseudo-version dependency in a security path is an unforced risk), Apache-2.0 is
clean, its API is the tightest fit, and it is a thin layer over `x/crypto/ssh` —
which we **already depend on** (`golang.org/x/crypto`, now a direct requirement in
`go.mod`). `42wim/sshsig` is the fallback: functionally equivalent, more widely
imported, but unreleased and its `Sign(pemBytes …)` API wants a private key file,
which is precisely the shape we refuse (§9.1). Its `SignWithAgent` is fine, but we
get the same thing from `ssh.Signer` anyway.

**Rejected: shelling out to `ssh-keygen -Y verify`.** It requires the openssh binary
in every agent container (the containers are minimal and may not have it), it costs
a process spawn per verification at the exposure choke, and it makes verification
depend on argv/exit-code parsing. **Verification must be pure Go, offline, and
in-process.** (Signing may *optionally* shell out for exotic key types, but with
`ssh.Signer` + the agent client we do not need to.)

**Verified offline, in the module cache:** `golang.org/x/crypto` v0.51.0 defines
`KeyAlgoSKED25519` / `KeyAlgoSKECDSA256` in `ssh/keys.go` and implements `Verify` for
both `skEd25519PublicKey` and `skECDSAPublicKey`; `ssh/agent` exposes `ExtendedAgent`
and `SignWithFlags`.
**Hardware-key verification and agent signing are already available to us today.**

### 11A.3 What we own regardless — the `allowed_signers` parser

**No library parses `allowed_signers`. We own it, no matter which dep we take.** This
is the single most important finding of the dependency evaluation, because it is the
parser that carries our **role system** (`namespaces=`, §7) — the thing that stops a
compromised publish key from approving content. Delegating the blob format and owning
the trust-root format is the right split anyway: the blob is fiddly crypto wire
format (take the library), the trust root is *policy* (own it, test it hard).

**Sizing, honestly:**

| Component | LOC (impl) | Notes |
|---|---|---|
| `allowed_signers` parser | ~180 | principals, `namespaces="a,b"`, `cert-authority`, `valid-after/before`, quoting/escaping, comments. `ssh.ParseAuthorizedKey` does the key-blob half for us. |
| Namespace/role enforcement | ~40 | the check that a key is trusted *for this assertion* |
| Countersign payload framing (§3.2) | ~60 | emit + parse the fixed header |
| Store (read/index/write signatures, two stores, union) | ~200 | §9.2 |
| Decision-function rewiring (§8) + gate/loader plumbing | ~250 | replaces existing trust.go/store.go code |
| Key discovery chain (§7A.4) | ~120 | git config → agent → explicit |
| Posture detection + warning (§9.1.2) | ~60 | `sk-*` type check |
| **Total we own** | **~900 LOC** | plus tests, which for this code should outweigh it |

The dependency saves us the ~150 LOC of sshsig wire-format handling and, more
importantly, the *bugs* in it. It does not save us the parser that actually enforces
our policy. **Anyone who believes taking the library means "we don't write crypto
code" has misread the split.**

---

## 12. Contract versioning

Third parties (bundle publishers, companion authors, CI) depend on these. Each is
versioned independently, because they change for independent reasons:

| Contract | Version carrier | Bump means |
|---|---|---|
| Publisher signature | SSH **namespace**: `publish.v1.ctxloom.dev` | old signatures no longer verify → publishers must re-sign |
| Countersignature payload framing | header line `ctxloom-countersign/2` **and** namespace `approve.v1.ctxloom.dev` | all existing approvals invalidate → mass re-review |
| Countersignature **`ref` serialization** (§3.2.1) | the framing above | a different `ref` string is a different signed payload — signatures will not verify |
| Exec-item preimage | `"preimage":"ctxloom-exec/1"` **first field** (§3.3.2) | all MCP/hook approvals invalidate |
| Companion loadout envelope | `"contract":"ctxloom-loadout/1"` | companions must re-emit |
| Sibling path convention | `<bundle>.yaml.sig` | a new path is a new contract |

All six are emitted by the code today. A third party binding to any of them should
bind to the strings above, not to a paraphrase of them: the `ref` serialization in
particular is easy to reinvent incorrectly, and §3.2.1 pins it byte for byte for that
reason.

**Rules:**
- A verifier **must reject** a payload whose contract string it does not recognize
  (fail closed — never "best effort parse an unknown version").
- A verifier **must ignore unknown JSON fields** in the loadout envelope (forward
  compatibility for additive change).
- Version strings are matched **exactly**, byte for byte. No range parsing, no
  "v1.x compatible". A version is an identity, not a constraint.
- **Never reuse a namespace with changed payload semantics.** The namespace is the
  domain separator; silently changing what `approve.v1` means over the same
  namespace is how you build a signature-confusion bug.

---

## 13. Does countersigning weaken anything the hash ledger had? — YES, four things.

Stated as refutation, because a spec that only lists its own wins is not
trustworthy.

1. **A universal forgery credential now exists, and in the default posture the
   agent can use it.** The hash ledger had no key; its weakness was local (write
   `trust.yaml` in *this* project, forge approvals in *this* project). Now there is
   a key, and a signature from it forges approvals in **every project, retroactively
   and prospectively**. Because the key lives in **ssh-agent** and ctxloom never
   touches it, the key cannot be *stolen* by an agent — but under **P1 (bare
   ssh-agent)** it does not need to be stolen: the agent simply asks for a signature
   and gets one (§9.1). **So in the posture most developers are actually in, this
   design does not remove the forgery — it merely changes its shape, and adds a
   credential whose compromise is global rather than per-project.** That is a *bad*
   trade, and it is only a good one under P2/P3/P4. This is the strongest argument
   against the whole feature and I am putting it in writing: **if we ship without
   the P1 warning and a real push toward `ssh-add -c` / hardware / containers, we
   will have made things worse while claiming to make them better.** The warning is
   not polish. It is the feature.
2. **Revocation got worse.** Deleting a hash-ledger row is surgical. Revoking a key
   invalidates everything it ever signed (§10.3), because sshsig has no trusted
   timestamp. The hash ledger's approvals were independent facts; a key's approvals
   are a correlated bundle that fails together.
3. **The store stopped being human-readable.** `trust.yaml` could be read, diffed,
   and audited in a text editor. A directory of armored signature blobs cannot.
   This is a genuine usability regression and it is *not* free to fix: any
   human-readable index we add (§9.2's `index.yaml`) is untrusted metadata that
   must never feed the decision, so we now maintain a display artifact that can
   drift from the truth. Mitigation is a real `ctxloom approvals list` porcelain
   that renders from *verified* signatures, and the discipline to never let the
   cached index answer a security question.

4. **Inherited approvals move a decision away from the person it protects.** Sharing
   is a first-class goal (§9.2) and it is genuinely valuable — but "trust everything
   this reviewer ever approves, forever" is a *broad* grant, and it is the same
   breadth that made `trust_bundles` a known gap (§11). We have replaced trusting a
   **location** with trusting an **identity**, which is better, but we have not made
   the grant narrower. A compromised or careless team-lead key auto-approves content
   into every developer's agent. Mitigations available and recommended: keep the
   reviewer key hardware-backed; scope it with `namespaces="approve…"` only so it
   cannot also publish; and remember that **any developer can still reject
   unilaterally** (§9.2 — rejection is supreme across stores), which is the release
   valve.

**A fifth thing that is NOT a weakness, contra intuition:** the countersignature
does not weaken the (raw, distilled) pair binding, the ref-scoping of approvals, or
the rename-immunity of rejections. All three survive exactly (§5.2, §5.3) — as
signatures rather than as columns.

---

## 14. Implementer traps — the top 3

These are cited from the code as §14.1, §14.2, and §14.3.

### 14.1 Making the approve and reject payloads symmetric

They are deliberately asymmetric: approve **binds the ref** (narrow — an approval is
of *this item here*), content-reject **omits the ref** (broad — a rejection is of
*these bytes anywhere*). This asymmetry is the entire mechanism by which the
content-hash denylist property survives. An implementer who unifies them into one
payload builder "for cleanliness" will silently either (a) make rejections escapable
by rename, or (b) make one approval leak across every ref with identical content.

**Test both directions explicitly:** approve fragment A at ref R, assert it is still
pending at ref R'; reject fragment B at ref R, assert it is *still rejected* at ref
R' and under a different publisher key.

### 14.2 Letting the hash index short-circuit the verification

The store is indexed by a content-addressed hash (§9.2). It is seductive — and fast,
and wrong — to write `if store.Has(hash) { return Allow }`. The hash finds the
*candidate*; the **signature** decides. If the verify step can ever be skipped, the
store is a forgeable file again and the entire feature is decorative.

**Test:** a store entry with a correct filename/hash and a corrupted signature body
must resolve **pending**, not approved.

### 14.3 Trusting the `signer` field in the envelope instead of `allowed_signers`

The companion loadout carries a `"signer"` string, and the temptation is to read it
and match it against a trusted-names list. That field is **advisory, for error
messages only**. The key must be resolved from `allowed_signers` and the signature
verified against *that key*. Anyone can write `"signer": "releases@ctxloom.dev"` into
a JSON blob.

The same trap wears a second costume: verifying a signature and never checking that
the key that made it is *trusted for that namespace* — a valid signature from a key
you have never heard of is not an authorization, and a valid *publish* signature from
a key that is only trusted to *approve* is not one either.

**Runner-up traps, worth listing in the brief:**

- **Re-serializing between verify and use.** Verify, **then parse, then use what you
  parsed** — never parse, re-emit, and verify the re-emitted bytes. The moment a
  serializer appears anywhere between the signature check and the exposure, the
  scheme is back to canonicalization and is broken.
- **Coupling the signature to the transport** (§3.0). Signing the git blob SHA,
  folding the repo URL into the payload, or "normalizing" bytes on the way out of
  the object store all look harmless and all destroy the transport-agnostic property
  that the entire org/loadout-sharing story rests on. **Test it directly: sign a
  bundle once, then verify the identical signature through the git path, the stdout
  envelope, and a plain file on disk. All three must pass with the same blob.** If
  they do not, the design has been broken in implementation.
- **Skipping step 1 when the publisher signature is absent or invalid.** Rejection
  must be evaluated for *every* item, including unsigned ones and ones whose
  signature failed. A rejection is of bytes, not of provenance.
- **Letting `--sign` silently degrade to an unsigned publish** when no key is found
  (§7A.4). It must be a hard error. The publisher pays nothing for an accidentally
  unsigned bundle; every one of their users pays.
- **Verifying a signature without checking the key is trusted *for that namespace*.**
  A valid `publish` signature from a key trusted only to `approve` is not a publish
  authorization. This check lives in the `allowed_signers` parser we own (§11A.3),
  which is why that parser gets the harshest tests in the suite.
