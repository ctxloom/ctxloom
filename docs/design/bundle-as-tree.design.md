# Canonical signing seam — design for build

> **READ FIRST (adversarial review, 2026-07-29). This document contradicts itself in eight
> places because later decisions were not propagated backwards. Only §5 carries a SUPERSEDED
> banner; FOUR other regions are equally dead and an implementer reading linearly WILL build
> retracted designs.** Retracted-but-unmarked: the registry struct sketch with
> `Detect func(fsys afero.Fs, path string)` / `Decode func(raw []byte)` (superseded by the
> "`Detect` takes a `Source`, never an `afero.Fs`" decision); the two places saying hooks keep
> an ORDINAL filename `hooks/<event>/NN-name` (superseded by file-per-hook with a required
> NAME — `NN-` was explicitly REJECTED); the `Signable`/`Recognizer` I/O-seam sections near the
> end (superseded by "`Signable`/`Recognizer` DISSOLVE into Item"); and the `index.yaml`
> keep-it-and-make-it-per-entry paragraph (superseded 50 lines later by "DELETE index.yaml").
> **The `s1/content-access` branch implements the LATER decisions — when the document and the
> code disagree, the code is right.** Full contradiction list with both locations is in the
> review-findings section at the end of this file.
>
> **ALSO (2026-07-30): S1 AND S2 ARE BUILT.** `bundle/s2` carries L0's bundle-level manifest API,
> L1 (`BuildManifest`/`VerifyContents`), and L2 (`internal/content/attest`). Review findings F1, F2,
> F3 and bundle-level F10 are CLOSED. Sections carrying a "BUILT 2026-07-30" banner state what
> actually shipped; the code blocks above those banners are historical sketches. **And this
> document NO LONGER SPECIFIES THE COUNTERSIGN KEY** — that vocabulary shipped separately at
> `231c5804` (`signing.AttestationForm`, `ctxloom-countersign/2`) and the old §"PRECISE KEY" bullet
> is now a citation, not a specification. `signing.ItemKind`, `signingKindOf`, `FormExec`,
> `contentRejectForms` and the `signing.Kind*` constants DO NOT EXIST.



**Direction (human, 2026-07-29):** signing must be the ONE canonical way to sign "whatevers" — bundles, skills, and future kinds (opencode-specific items). Not skill-specific. Extensible: a new item kind plugs in without bespoke signing code.

## [SUPERSEDED — kept only for history; do NOT build this] The Signable seam
Superseded by "THE ACCESS SURFACE" below: `Signable`/`Recognizer` dissolved into `Item` + the
surface-type registry, and `PublisherPreimage` is gone entirely (signatures cover FILE BYTES).
Skip to the access-surface section.

## The seam (REVISED 2026-07-29 — three decoupled layers, no signing-side kind enum)

**1. `Signable` — a unified I/O seam.** Owns WHAT bytes are signed and WHERE its
signature lives; reads its own preimage, reads and writes its own signature. No
`Kind()` (verified zero consumers), no path or `afero.Fs` leaked to callers.
```
type Signable interface {
    PublisherPreimage() ([]byte, error) // canonical bytes under NamespacePublish
    ReadSignature() ([]byte, error)     // load detached sig; fs.ErrNotExist when unsigned
    WriteSignature(sig []byte) error    // persist the armored detached sig
}
```
- **bundle** preimage = the bundle file's on-disk bytes (unchanged). Sig I/O = `bundle.Path + bundles.SigSuffix`.
- **skill** preimage = `SkillManifest.Serialize()` (already what `skill export --sign` / `PublisherSkillSignatureVerifier` use — NO new contract). Sig I/O = a `.sig` sibling in the skill tree.
- Signable is NOT self-describing: the concrete type IS the identity; nothing switches on a returned enum. This deletes the stage-1 `trust.ItemKind("bundle")` literal and the "does bundle belong in the trust enum" question.

**2. Disambiguator — a PLUGGABLE resolver: file/directory path → `Signable`.**
```
type Recognizer interface {
    // Recognize claims a resolved path as its kind, or declines with (nil, nil).
    Recognize(fs afero.Fs, path string) (Signable, error)
}
func (r *SignableRegistry) Register(Recognizer)
func (r *SignableRegistry) Resolve(fs afero.Fs, path string) (Signable, error) // ErrUnrecognized if none claim
```
- `bundleRecognizer` (bundle file → bundleSignable), `skillRecognizer` (dir w/ manifest → skillSignable); first-match-wins, ordered.
- A new item kind plugs in by registering a `Recognizer` + implementing `Signable` — no edit to `ctxloom sign` or the mechanics. THIS is the extension point (incl. future opencode kinds).

**3. Mechanics — kind-agnostic, one implementation each:**
```
func SignItem(item Signable, signer ssh.Signer) error
func VerifyItem(item Signable, root signing.TrustRoot, now time.Time) (principal string, err error)
```
All flows compose these: `ctxloom sign <path>` = Resolve → SignItem; exposure = Resolve/ref → VerifyItem → EffectiveTrust; import = Resolve in staging → VerifyItem.

All three flows go through the seam:
- **`ctxloom sign <ref>`** — resolve ref → `Signable` → `signing.Sign(preimage, signer, NamespacePublish)` → write `SigPath()`. Unified verb; `bundle sign` becomes an alias/dispatch. ssh-agent path unchanged.
- **Exposure** — the content gate loads a `Signable`'s `.sig`, `VerifyPublisher(preimage, sig, cfg.TrustRoot())`, and feeds the per-item verdict into `EffectiveTrust`.
- **Import / transfer** — verify via the seam in staging; persist the `.sig` via `SigPath()`.

## Trust decisions (unchanged model, now per-item where a sig exists)
- item `.sig` verifies vs trust root → **trusted-signer → allow**, in any container.
- `.sig` present, fails → **tampered → reject**, in any container. (closes signed/tampered laundering)
- no `.sig` → **provenance fallback**: local → trust; remote/imported → **pending review** (see below).
- A signed BUNDLE still transitively governs the items inside it — NOT via bundle.yaml
  stored hashes (CORRECTED 2026-07-29: `content_hash` is "an INDEX, never an authority",
  bundles.go:409; it drives re-distillation change-detection only and EffectiveTrust never
  reads it). The real mechanism: the whole-bundle publisher sig covers the EXACT file bytes,
  so tampering any item breaks the bundle sig, and exposure re-hashes the live served bytes
  (EffectiveContentHash). Per-item PUBLISHER sigs COMPLEMENT for exactly one gap: a
  loose/imported item with NO governing bundle sig (F26(b)); none exist today. Item-level
  human trust is already carried by per-item COUNTERSIGNED approval records over live
  bytes+form — separate from publisher sigs, and unaffected by this work.
- Per-item sig storage = sibling `.sig` ("on"), NOT embedded in bundle.yaml ("in") — forced by
  the no-schema-change constraint below. `ctxloom sign <bundle>` emits NO recursive per-child
  sigs (the whole-bundle exact-byte sig already governs every item). Subcomponent extraction is
  a VERIFY-side capability (decompose → check each item), not a signing-side one.

## F26, folded in
- **(a) tamper-reject on import** — verify in the staging callback; `ErrSignatureTampered` → reject + clean up, never lands.
- **(b) unsigned imports PENDING regardless of destination** — an imported skill with no valid trusted sig does not inherit local-trust from its destination bundle; it stays pending until reviewed/signed. (Closes the unsigned laundering the signing seam alone doesn't.)
- **(c) local item manifest-mismatch → warn + allow** — prototyping: an edited in-repo skill stays usable with a loud `clidiag.Warn`; remote unaffected.

## Confirmed constraints (from scouts)
- NO new dependency (reuse `internal/signing`, ssh-agent, allowed_signers trust root, manifest serializer).
- NO new preimage contract for skills (reuse `manifest.Serialize()` under publish.v1) — existing `--sign` outputs stay valid.
- Storage = detached `.sig` sibling (human-chosen), additive, NOT a bundle.yaml schema change.
- Preimage binds content bytes only (not name/location) — correct for a content attestation.

## BUNDLE-AS-TREE pivot (2026-07-29) — supersedes embed-in-bundle
Restructure monolithic bundle.yaml → on-disk DIRECTORY TREE (one file per item/surface), sign each with detached sigs. Eliminates the whole-bundle YAML document parse → FS-walk. Ground truth (scouts):
- Directory-form bundles ALREADY exist (`Loader.Find`: `<name>.yaml` then `<name>/bundle.yaml`; skills FORCE directory form). Pivot = push fragments/commands/mcp/hooks out of inline maps into files, like skills already are.
- Reuse `internal/bundles/skill_archive.go` (deterministic pack + HardenedExtract + `SkillManifest{sha256,mode}`).
- Pin today = git SHA in `.ctxloom/lock.yaml`; transfer = YAML-in-git-tree + sibling `.yaml.sig`, pulled bytes-only at pinned SHA. Signed manifest-of-hashes is a NEW stronger object layered over/replacing the SHA pin.
- ORDERING: profile `FragmentRef.priority` ordering must survive (verified a non-issue —
  `profiles.FragmentRef` already round-trips losslessly).
- **HOOKS MOVE TO NAME-BASED IDENTITY (decided 2026-07-29 — option B; supersedes "encode the
  ordinal").** Today a hook's trust identity is its ORDINAL position (`"<event>/<index>"`). That is
  connascence of POSITION: insert a hook at the top of an event and every hook below it silently
  changes identity, invalidating approvals for items that did not change. New model:
  - layout `hooks/<event>/<name>.yaml`; **ref = `<event>/<name>`** — identity is the NAME;
  - ~~**order is DECLARED METADATA** (`order: N` inside the hook's own file)~~ **RETRACTED
    2026-07-29 — a per-hook `order` field is UNIMPLEMENTABLE and would be SILENTLY IGNORED.**
    Verified by the S1a implementer: the only readers of a per-hook ordinal are IDENTITY
    (`HookEntry.ID()` / `EntryByID`, consumed at `operations/trust.go:1027`); `hookEventOrder` orders
    EVENTS, not hooks; and within an event hooks merge by **pure append across every source**
    (`wire.UnifiedHooks.Append`, `agent.MergeHooksConfig`) — nothing sorts, nothing consults a
    per-hook field. **Sequence is a property of MERGE ORDER, which a single hook's own file cannot
    express and must not appear to.** So hooks carry NO metadata at all and get NO sidecar, and a
    leftover `order:` sidecar is refused LOUDLY rather than ignored.
    This was a coordinator invention that survived several rounds of review because it sounded
    reasonable; only reading the merge path disproved it.
  - Rejected: (A) preserve ordinals — keeps the fragility forever; (C) `NN-` filename prefix carrying
    order — magic filenames where explicit data is clearer.
  - Migration cost is ALREADY PAID: countersign staling was accepted, so hook approvals invalidate
    once regardless. This is the one moment fixing it is free.
  - **CHECKED 2026-07-29 (adversarial refutation) — VERDICT: file-per-hook is BLOCKED; the REF-SHAPE
    half alone is SAFE-WITH-CAVEATS.** Adopt only the ref-shape change now (hooks stay INLINE in the
    bundle document; add `name` required+unique-per-event validated at parse, and `order` as
    metadata; `HookEntry.ID()` returns `<event>/<name>`; `EntryByID` becomes a name lookup — the ONLY
    place that parses the index as an integer, `bundles.go:292-305`, consumed by `trust.go:1027`).
  - **`name`/`order` MUST be excluded from `ContentPayload`.** `hookContentPayload`
    (`bundles.go:760-766`) carries only Preimage/Matcher/Type/Command/Prompt/PreToolFallback — no
    event, no index. Keep it that way and NO `signing.ExecPreimageContract` bump is needed and
    content-rejects survive. Put `name` in the payload and you invalidate every hook approval AND
    every content-reject, and the contract bump becomes mandatory.
  - Also fix the two INDEPENDENT ref producers or accept two coexisting shapes:
    `managed.go:374` (profile-inline hooks) and `managed.go:392-398` (plugin hooks, a THREE-segment
    shape whose events aren't in `hookEventOrder`). Both are already un-`trust`-able from the CLI.
  - **Hooks have NO name field today** (`bundles.go:185-199`, and `:259-262` says so explicitly).
    Every hook in the wild is nameless (`cmd/ltk/loadout.yaml:57`, `cmd/taskloom/loadout.yaml:57`,
    every published remote bundle). A migration must synthesise names — and a synthesised name IS
    positional identity by another name. Be explicit about that rather than pretending otherwise.

### DECISIONS 2026-07-29 (late) — layout+manifest atomic, sig dirs, uniform poly-file, approvals
- **HOOKS: file-per-hook ACCEPTED** (overrides the inline-only recommendation), WITH a required
  `name`. **LLM-SYNTHESISED names are acceptable** for the migration of today's nameless hooks —
  but state plainly in the migration that a synthesised name is positional identity renamed.
  Blockers resolve as: (1) unsigned-file-inherits-signer → dissolved by shipping layout+manifest
  ATOMICALLY (below); (2) `fsStore.Save` re-marshal → follow the skills pattern, no hydrated content
  in the marshaled struct; (3) companion loadouts have no filesystem → solved by `Source`: a
  companion loadout is a DOCUMENT-BACKED Source whose List/Open read an embedded file map (same
  model, different transport, NOT a dual format); (4) nameless hooks in the wild → migration.
- **STAGE ORDER: layout + manifest ship TOGETHER, atomically.** Not manifest-first, not layout-first.
  This closes the window in which an item lives in a file no signature covers while still inheriting
  the bundle's trusted signer.
- **UNIFORM POLY-FILE INTERFACE — no single-file special case.** Everything is multi-component;
  single-file items are simply N=1 and MUST be conformant without special handling. Therefore
  `Content()` is ALWAYS the deterministic component digest (never "raw bytes when there is one
  file"), raw bytes are ALWAYS reached via `Components()[i].Bytes`, and every item carries a
  `SHA256SUMS` manifest even at N=1. One rule, no branch.
- ~~**SIGNATURE STORAGE: a `.<filename>.sig/` DIRECTORY, universally**, one file per
  signer/namespace named `<namespace>.<principal-short>.sig`.~~
  **RETRACTED 2026-07-29 — OVER-DESIGNED. Do not build this.** Challenged with "if we've already got
  the content-addressed store, why are we messing with `.file.sig/` at all?" — correctly. Revised:
- **SIGNATURE STORAGE, MINIMAL (decided 2026-07-29).** Split by what each assertion actually needs:
  - **`approve` + `reject` → the EXISTING content-addressed store. Add nothing.** It already gives
    content addressing, mixed provenance (`keyTag`, whose comment names the two-maintainer case),
    per-assertion namespacing, the user/project split with union reads, path-independence, and
    index-never-authority. Putting `approve` in an in-tree sig directory was pure duplication.
  - **`publish` → in-tree, because it is the ONE assertion that must travel to a consumer who has
    never seen the content.** The store is user-local (never committed) or repo-local, so a remote
    `pull` cannot obtain the publisher's attestation from it. This is precisely why
    `<bundle>.yaml.sig` exists today.
  - **Signatures attach to MANIFESTS, not to arbitrary files.** Since every item already carries a
    `SHA256SUMS` manifest under the uniform poly-file model, the sig locations collapse to: ONE at
    the bundle root (`SHA256SUMS` + sig — the common case, and just `<bundle>.yaml.sig` generalized
    from a document to a tree), plus one per item ONLY when that item has a different signer (the
    rare mixed-provenance case). A per-FILE directory scheme was solving a problem the manifest
    already solves.
  - Net: NOTHING new for approve/reject; ONE existing convention reused for publish.
- **SIGS ARE KEYED BY CONTENT HASH; THE AUTHORITATIVE STORE IS CONTENT-SHAPED (decided 2026-07-29).**
  Supersedes "sigs attach to manifests" as a separate rule — a manifest is just content with a hash,
  so it falls out as a special case. ONE store shape for ALL THREE assertions, keyed by content hash,
  which is exactly what the existing approvals store already is
  (`indexHash = sha256(CountersignPayload(header, payload))`). Publish stops being a different
  mechanism and becomes the same mechanism with a different HEADER.
  - TWO LAYERS, separate jobs: **`SHA256SUMS` manifest = path → hash** (the tree's shape);
    **sig store = hash → signatures** (content attestation).
  - Why over path-attached siblings: renaming/moving a file cannot orphan its signature (the key is
    the hash); identical bytes in two places share one signature; and it FOLLOWS THROUGH on the
    already-committed property that the preimage "binds content bytes only, not name/location"
    instead of contradicting it in storage. Path-attachment is the same fragility that bit hook
    identity.
  - **NO file→sig link is needed** — the manifest IS the link (path → hash → signature). An explicit
    pointer would be a second, drift-prone source of truth for a question the manifest already
    answers.
  - SCOPING LIVES IN THE HEADER, not the storage (already how the store works): publish =
    content-only header (location-independent); approve = ref-scoped header (deliberate, unchanged);
    reject = either (`blacklist` writes both today).
  - **DOCUMENT THIS SEMANTIC:** a content-keyed publish signature means "this publisher attests these
    BYTES", NOT "this publisher put these bytes HERE". The same bytes in a different bundle carry the
    same attestation. Correct for a content attestation, but a real property — write it down rather
    than letting it be discovered.
  - Publish STILL ships in-tree (a consumer pulling your bundle cannot read your local store), so the
    published tree carries a CONTENT-ADDRESSED sig store of the same shape, travelling with the
    artifact — not path-attached siblings.
- **WHOLE-BUNDLE SIGNING IS THE COMMON CASE; per-file countersigning is the exception** (human,
  2026-07-29). Consequence for verification: MOST items will have NO `.<filename>.sig/` directory,
  and that must read as NORMAL, not "unsigned". `VerifyItem` resolves in order: per-item signature
  if present → else the bundle manifest's coverage → else genuinely unattested. Getting this
  fallback wrong either floods users with false "unsigned" warnings or silently treats
  manifest-covered items as unverified. Most-specific-wins already gives the order; this makes the
  ABSENCE case explicit. `ctxloom sign <bundle>` (sign the manifest, covers everything) stays the
  one-action default path.
- **REJECTIONS DO NOT LIVE IN THE PER-FILE `.sig/` DIRECTORY** (decided 2026-07-29). The mechanism
  supports rejection — `NamespaceReject = "reject.v1.ctxloom.dev"` (`signing/publisher.go:27`) and
  the store encodes the assertion in the filename, `indexHash.<assertion>.<keyTag>.sig`
  (`countersign/store.go:174`). But rejection is deliberately PATH- AND REF-INDEPENDENT: the code
  enforces "an APPROVE must pin bytes, a REJECT need not" (`store.go:178`, `:191-194`), and
  `ctxloom blacklist` writes BOTH a ref-reject and a content-reject that omits the ref entirely —
  which is exactly why blacklists survive a rename.
  Therefore: `.<filename>.sig/` carries ONLY `publish` and `approve` (both legitimately attached to a
  specific artifact). Rejections stay in the CONTENT-ADDRESSED approvals store. Two reasons this is
  not negotiable: (1) a blacklist must OUTLIVE the content — a path-attached reject would be deleted
  by deleting the file, i.e. you could un-blacklist something by removing it; (2) you must be able to
  reject content you have NOT fetched. Rejection remains supreme, checked before approval.
- **APPROVAL PORTABILITY — mostly ALREADY BUILT; verify before building anything.**
  `HomeApprovalsPath` is `~/.ctxloom/approvals`, USER-scoped and GLOBAL across projects ("My
  approvals follow me", `paths.go:347-349`), and reads are the UNION of the user and project stores
  with NO precedence (`countersign_records.go:9`). So approving in project A ALREADY approves the
  same item in project B on the same machine. Team inheritance is ALSO already built: the project
  store `.ctxloom/approvals` is committable and documented for exactly "a lead reviews, commits the
  signatures, every developer/CI trusting the lead's key inherits the approval without re-reviewing"
  (`paths.go:338-342`).
  - REAL GAP 1: cross-MACHINE portability of the personal store (it is not in git) → a personal
    approvals REPO. NOT git subtrees/submodules: they couple histories and would leak personal review
    decisions into shared project repos.
  - **HOW SHARED SIGS WORK — NO NEW ctxloom CODE REQUIRED (decided 2026-07-29).** The store is a
    CONTENT-ADDRESSED SET OF IMMUTABLE FILES, which makes it trivially syncable:
    - lookups are a hash GLOB (`indexHash(header,payload)+".*.sig"`, `countersign/store.go:258`) —
      the store hashes what it already holds and globs; it NEVER reads a filename as identity;
    - each record's filename derives from its own content, so two records CANNOT conflict. Adding is
      the only operation and **union is always the correct merge** — no merge logic, ever;
    - non-record files are explicitly tolerated (`store.go:105`).
    Mechanism: make `~/.ctxloom/approvals` a GIT REPO with a private remote. `.gitignore` the one
    mutable file, `index.yaml` — the code states it is untrusted display metadata that "must never be
    an input to step 1-6" (`store.go:404-413`), used only to label NEW vs UPDATE and offer a diff
    base, so losing it costs only labelling until the next review.
    **Safe over untrusted transport:** records are signed, a tampered record fails verification, and
    an injected record you did not sign will not verify against your key in `allowed_signers`. So the
    sync channel needs no trust (git, rsync, Syncthing, anything).
    Combined with CONTENT-ONLY keying, checking out ANY repo inherits your approvals automatically —
    no per-repo step. That is the payoff of the content-only decision.
    Team/CI sharing is a DIFFERENT case and is ALREADY BUILT: commit to `<repo>/.ctxloom/approvals`;
    anyone trusting that key via the project `allowed_signers` inherits it without re-reviewing.
    Do the PLAIN-GIT version first (zero new code, proves the workflow); only add a thin
    `ctxloom approvals sync` or a remote-backed third store if that turns out to be friction.
  - **DECIDED 2026-07-29: APPROVALS BECOME CONTENT-ONLY** (human: "content only"). This REVERSES
    today's ref-scoping and makes `approve` symmetric with `reject`, which is already content-scoped
    (`blacklist` writes a content-reject that omits the ref — that is why blacklists survive a
    rename). Consequences, stated deliberately because this is a security-relevant loosening:
    - GAINED: approve once, approved everywhere — the same bytes are approved regardless of which
      bundle/repo/project they arrive in, and across machines once the personal store is portable.
      This is defensible on its own terms: you reviewed those exact BYTES and found them acceptable,
      and the bytes are what gets injected or executed.
    - COST: identical bytes shipped by an UNTRUSTED publisher inherit your approval. A hostile
      bundle can include content you already approved and have that content pass the gate. It only
      gains the items you actually approved, and each item is still judged separately — but the
      approval no longer says anything about the SOURCE.
    - **[SUPERSEDED BY SHIPPED CODE — this document no longer specifies the countersign key.]**
      This bullet used to specify its own header shape (`{Assertion, Kind, Ref, Form}`, keyed
      `(assertion, kind, form, bytes)`). That specification is DEAD. The trust vocabulary landed on
      `release/0.7` at `231c5804` and the shipped code is now the only authority; where this document
      and the code disagree, the code wins.
      **THE INVARIANT THIS DOCUMENT DEFENDS, restated as an invariant rather than an encoding:**
      *an approval binds bytes IN A ROLE, and the role is not recoverable from the bytes.* Approving a
      text FRAGMENT must never approve a byte-identical HOOK command or MCP config, because those are
      executable surfaces where identical bytes have entirely different consequences. That is the part
      that must not be simplified away. HOW it is encoded is the code's business.
      **CITATIONS (do not re-specify — read these):**
      - `signing.AttestationForm` (`internal/signing/payload.go`) — the CLOSED COMPOSITE vocabulary
        that carries the role: `fragment/raw`, `fragment/distilled`, `command/raw`,
        `command/distilled`, `exec/mcp`, `exec/hook`, `skill`, plus `AttestNone("")` for a ref-reject.
        `kind` is GONE from the header as a separate field; the composite form value carries it.
      - `signing.CountersignContract = "ctxloom-countersign/2"` (same file) — the `/2` bump is what
        stales every pre-composite record.
      - `signing.CountersignHeader` (same file) — the shipped header shape.
      - `attestationFormFor` (`internal/operations/countersign_records.go`) — the ONE exhaustive
        derivation site from (live item kind, layout form) to attestation form. Every read and every
        write goes through it, which is what makes the two sides agree.
      **SYMBOLS THIS DOCUMENT ONCE NAMED THAT NO LONGER EXIST:** `signing.ItemKind`, `signingKindOf`,
      `FormExec`, `contentRejectForms`, and the `signing.Kind*` constants. Any passage still using
      them is describing a build that never shipped.
    - Removing `ref` from the countersign header is a preimage change: every existing approval
      stales. Already acceptable (see countersign staling) but it must not be discovered at migration
      time.
    - **CONSEQUENCE: content-only makes `index.yaml` MORE valuable, not less.** Once `ref` leaves the
      signed record, the index is the ONLY record of the context in which you approved something
      ("these bytes, as item X from repo Y, on date Z") — i.e. your own audit trail. A third reason
      not to delete it (see the index discussion), and a further reason to make it PER-ENTRY files so
      it survives a sync instead of being the one unmergeable file.
    - **GOVERNING INVARIANT (human, 2026-07-29): "REVIEW ONCE, GLOBALLY." A signature blesses that
      content in ANY project where I trust that key. I look at it once, and only once.** This follows
      the principle already stated in this codebase — "precedence lives in the DECISION FUNCTION,
      never in the filesystem": location is a lookup convenience, THE SIGNATURE IS THE AUTHORITY.
      Four things are required, of which only #3 is missing:
      1. content-only keying (decided above) — same bytes, same approval, whatever bundle/repo;
      2. signature-as-authority — ALREADY how it works (a record counts if it verifies against the
         trust root, not because of where it sits);
      3. **READ APPROVALS FROM REFERENCED REMOTES — the actual gap.** Verified cheap: the repo cache
         is a FULL clone (no sparse/partial/depth flags anywhere in `internal/remote`), so if project
         A commits `.ctxloom/approvals`, those records are ALREADY ON DISK in B's cache after a pull.
         Nothing reads them. (An earlier framing of this as "de-special-case the home dir" was WRONG —
         the home store is not the gap; unread remote stores are.)
         SAFE BY CONSTRUCTION: a remote's record only counts if its signer verifies against YOUR
         `allowed_signers`. Your key counts; a stranger's key committed in their repo verifies as
         THEIR signature and grants nothing. The crypto gates it, not the path — so this cannot be a
         trust escalation.
      4. portable personal store (the approvals git repo) for cross-MACHINE reuse.
    - **PRECISION on "only once": once per DISTINCT BYTE-SEQUENCE, not once per item forever.** When
      content changes you review again — that is the UPDATE-with-diff flow, the point rather than a
      violation. It is also the third reason `index.yaml` earns its place: it is what says "you have
      seen a previous version of this item, here is the diff".
    - READS generalize to N sources. An UNREADABLE source must fail closed — "could not read" is NOT
      "has no records" (a missed REJECTION is the dangerous direction). A genuinely ABSENT source is
      legitimately "no approvals" and re-gates to pending; do not conflate the two.
    - **DROP THE HOME-DIR STORE (decided 2026-07-29).** With remote approval inheritance (#3 above),
      `~/.ctxloom/approvals` is obsolete. Replacement is strictly more uniform: **approvals are
      content in repos you REFERENCE**, read through the same mechanism as every other referenced
      artifact; your personal approvals repo is one source among N rather than a magic path. This also
      FIXES the container/CI gap properly instead of working around it — a container that pulls the
      referenced remotes gets their approvals with everything else (no host-store mount, no bespoke
      setup step). Supersedes the earlier "keep home as a default entry" recommendation.
      TWO FRICTIONS to design for, not discover:
      - **Writes need a destination.** Reads generalize; a write must go somewhere. With no home
        store that is the project store (committable, sane default) or a personal approvals-repo
        checkout — i.e. a CONFIG setting, not a hardcoded path. Reviewing then implies a COMMIT if the
        approval is to travel; today it is just a file write. Bootstrapping (first review, no
        configured approvals repo) should default to the project store.
      - **Propagation follows the REFERENCE GRAPH.** An approval reaches a project only if that
        project references a source carrying it. Two projects independently referencing the same third
        party do NOT share approvals unless both also reference your approvals repo. More explicit
        than a global store, but a real behavioural change from "automatically everywhere on this
        machine" — and it means "review once, globally" holds only for projects that reference your
        approvals source.

  - **DELETE `index.yaml`; APPROVED-MANIFEST SNAPSHOTS REPLACE IT (decided 2026-07-29 — unparked).**
    What it does today: it is the **ref → previous-payload-hash** map. The diff base works in two hops
    — `LatestApprove(kind, ref, form)` reads index.yaml for `PayloadHash`, then
    `readTrustSnapshot(fs, baseDir, hash)` fetches bytes from `.ctxloom/cache/trust/objects/<hash>`,
    which is CONTENT-addressed (`review_snapshots.go:20,64-68`).
    Trust-wise nothing needs it: under content-only keying, changed bytes simply match no approval →
    pending → re-review. Correct and safe. The only casualty would be the DIFF — without a ref→hash
    map the old bytes remain on disk but you cannot tell which blob was this item's previous version
    (and the records no longer carry a ref to recover it from).
    **The tree design already supplies a better map: the APPROVED MANIFEST IS the ref→hash index.**
    Snapshot the `SHA256SUMS` that was approved → path→hash for every item; on change, take the prior
    hash for that path from the approved manifest and the prior bytes from the existing
    content-addressed snapshot store, then diff. Strictly better than index.yaml because it is
    CONTENT (immutable, hash-addressed, syncs conflict-free — removing the one unmergeable file from
    the store), it is SIGNED (integrity-bearing rather than explicitly-untrusted metadata), and it is
    ONE mechanism instead of two (manifests already exist for signing). It also DEFENDS the threat
    `store.go:505-511` names — UPDATE relabelled as NEW hiding substituted bytes — BETTER, because the
    mapping is attested rather than an editable cache.
    BLAST RADIUS for the implementer: delete `IndexEntry`, `readIndex`/`appendIndex`/`indexPath`,
    `Store.LatestApprove` and their tests; rewire the two production callers
    (`operations/review.go:382` via `latestApproveEntry`, `operations/countersign_records.go:252`);
    add manifest snapshotting beside the existing `writeTrustSnapshot`. Net-negative change.
    ACCEPTED LOSSES: (1) loose/imported items with no bundle manifest get no diff base — minor and
    arguably correct. That is the ONLY real loss:
    - **`principal` is DERIVABLE, and the derived value is the AUTHORITATIVE one.** SSHSIG embeds the
      signer's public key, so verification yields the principal directly; the code already requires
      this — `keyTag` "is never read back as identity; the identity always comes from re-verifying the
      signature blob itself". index.yaml's copy was a display shortcut over the real source.
    - **`reviewed_at` is NOT derivable, and NOT NEEDED.** `CountersignHeader` is
      `{Assertion, Kind, Ref, Form}` — NO timestamp, so signing time was never in the signed data. Its
      only two consumers are the index-ordering comparisons (`store.go:524`, `review.go:387`, both
      `e.ReviewedAt > latest.ReviewedAt`) which exist solely to pick the latest INDEX ENTRY. Deleting
      the index deletes the requirement. Pure index bookkeeping.
    - FUTURE NOTE: if "when did I approve this" is ever wanted, it CANNOT come from the signature —
      only from file mtime (unreliable across sync/copy) or by adding a timestamp to the signed
      header, which is a PREIMAGE CHANGE. Do not assume it is available.
  - SUPERSEDED CONSTRAINT (kept for rationale): approvals WERE REF-SCOPED — the key is
    `(assertion, kind, ref, form, payload-bytes)` where ref = canonical repo URL + `<bundle>#<kind>/<name>`.
    So the same BYTES fetched under a DIFFERENT bundle or repo are NOT approved. "Fetch it elsewhere
    and it is approved" holds only for the same source+bundle+item. Making approvals content-only
    would auto-approve identical bytes from an untrusted publisher — a security weakening, not a
    convenience. Decide deliberately if that is wanted.

### THREE CONSTRAINTS THIS UNCOVERED THAT BIND THE WHOLE TREE DESIGN
1. **SIGNING MUST LAND BEFORE (OR WITH) THE FILE LAYOUT — this reorders the stages.** Today
   `SignBundleFile` signs the bundle FILE bytes verbatim (`operations/sign.go:132-165`). Any item
   moved OUT of that file is unsigned yet still inherits the bundle's verified signer
   (`config_bundles.go:745` hands `bundle.Signer()` to the gate; a trusted signer auto-allows). For
   hooks that means **an arbitrary command admitted under someone else's signature.** So S1 (tree
   format) CANNOT precede S2 (manifest signing) — the gap between them is a security hole. Either
   ship the manifest first, or ship layout+manifest atomically. Skills already avoid this via a
   generated `files:` manifest inside bundle.yaml + load-time `VerifyExtractedManifest`.
2. **COMPANION LOADOUTS HAVE NO FILESYSTEM.** `ProbeCompanionLoadouts` (`config/companions.go:331+`)
   execs `<bin> loadout --format json` and parses ONE document; profile-inline hooks
   (`wire.HooksConfig`) likewise. So the bundle format MUST remain expressible as a single document,
   or companions need a separate path. Worse: ltk/taskloom ship as independently released binaries,
   so an OLD binary emits nameless hooks and a fail-closed name lookup would SILENTLY WITHHOLD ltk's
   `pre_tool` guardrail — a security degradation that fails quiet.
3. **`fsStore.Save` re-marshals the WHOLE Bundle struct** on every mutation
   (`bundles/store.go:50-64`). Any item hydrated into a yaml-tagged field gets written back INLINE
   on the next `bundle edit`/`fragment add`/`distill`, duplicating it and invalidating the `.sig`.
   Items-in-files must follow the skills pattern: no hydrated content in the marshaled struct.
   ALSO: `ParseBundle` is a NON-STRICT unmarshal, so an old client seeing a file-based bundle sees
   **no items at all** — a silent disappearance, not a fail-closed error.

Signing model (decided):
- Tool = internal sshsig (internal/signing), NOT the ssh-keygen binary (keeps ssh binary dep out; still ssh-keygen-compatible format). No new dep.
- Two granularities × two roles, freely combinable: publisher(initial) + approval(countersign), each per-file `.sig` AND/OR per-bundle signed manifest-of-hashes.
- PRECEDENCE = MOST-SPECIFIC-WINS: per-bundle manifest is the blanket baseline (a changed file fails its manifest hash, manifest sig prevents re-forge); on conflict a per-file sig OVERRIDES the manifest.
- RESIDENCY: publisher `.sig`s co-locate IN-TREE (travel with bundle); USER-level countersign approvals STAY out-of-tree (~/.ctxloom/approvals, never committed); project approvals may co-locate. No blanket collapse.
- By-surface approval ALREADY EXISTS as per-item detached sshsig in the approvals stores — the tree just gives each item a file to sit beside.

RESOLVED (2026-07-29):
- Mixed-provenance is FINE — different signers per file OK; a bundle can carry files from different publishers; VerifyItem reports the per-file signer, each file judged on its own trust under most-specific-wins.
- **METADATA RESIDENCY IS PER-TYPE, DECLARED BY THE REGISTRY ENTRY (final, 2026-07-29).** There is NO
  global rule — an earlier "front-matter for .md, native fields for .yaml" note was wrong, and its
  replacement ("sidecar is the rule, md the exception") was ALSO unnecessary. **Each `SurfaceType`
  decides, and the interface already expresses it with no extra method:** `Encode(s) ([]Component,...)`
  emits one component (front-matter inside the file), two (content + sidecar), or a whole tree; and
  `Decode(src Source)` reads back whatever it wrote.
  GUIDANCE for type authors (not law):
  - **mcp/hook → `<name>.meta.yaml` SIDECAR.** Their content file is consumed by OTHER tools with their
    own schemas; injecting ctxloom keys (`name`, `order`, `executable`) pollutes a foreign contract and
    many JSON/YAML consumers reject unknown keys. Sidecar keeps the content file PURE VENDOR CONFIG,
    directly consumable by the tool that owns its schema.
  - **`.md` kinds → YAML FRONT-MATTER**, because Agent Skills' `SKILL.md` is front-matter-based and we
    conform to that external standard rather than diverging from it.
  - a future kind picks whatever its own ecosystem expects.
  **SIDECARS ARE DOT-PREFIXED: `.<name>.meta.yaml` (decided 2026-07-29).** Keeps the authoring UX clean
  — a loose `ls` or editor file-tree shows content files, not metadata noise — and matches this repo's
  existing convention of dot-prefixing machinery (`.ctxloom/`).
  - **SECURITY TRAP, test it explicitly:** dotfiles are excluded by default in much glob/walk code
    (`filepath.Glob("*")` does NOT match them). If the walker misses `.foo.meta.yaml`, that sidecar
    never enters the digest — so **executability silently stops being attested** while everything still
    looks signed and green. That is this project's signature failure shape. REQUIRED test: a
    dot-prefixed sidecar appears in `Components()` AND changes `Content()`.
  - Stem grouping must resolve `.foo.meta.yaml` to stem `foo`, not `.foo.meta` — strip the leading dot
    and the `.meta.yaml` suffix.
  - Hidden is not unreviewable: `ctxloom review` renders components explicitly, so `ls` was never the
    review surface.
  - REJECTED alternative `.meta/<name>.yaml` (one hidden dir instead of N hidden files): it separates
    metadata from its content file, so a rename must touch two directories and grouping must look in
    two places. Adjacent sidecar wins.

  **REQUIRED MECHANISM — candidate grouping.** The tree walker must group files into item candidates
  BEFORE asking a type, or it cannot know `foo.md` and `.foo.meta.yaml` are one item. Rule: **group by
  STEM within a kind directory** — every file sharing a stem (dot-prefix and `.meta.yaml` stripped),
  plus a same-named subdirectory, forms one candidate `Source`. `Detect`/`Decode` then see the whole
  group, and sidecars are STRUCTURALLY components rather than items without the walker needing to know
  what a sidecar is.
  - PROPERTY GAINED: an executable surface's content file stays PURE VENDOR CONFIG, directly consumable
    by the tool that owns its schema — nothing of ours in it.
  - Attestation stays uniform even though residency differs: front-matter is inside the file's hashed
    bytes, and a sidecar is its own hashed COMPONENT. Either way metadata is covered by the digest, so
    changing metadata changes `Content()` and invalidates the signature — correct and intended.
  - IMPLEMENTATION TRAP: sidecars live beside items, so `Refs()`/`Detect` must NOT enumerate
    `foo.meta.yaml` as an item named `foo.meta`. Sidecars are components of their item, never items.
  - `Writer.Delete` must remove BOTH the content file and its sidecar.
- kind=directory.
- ~~Authored raw in content-tree; distilled variant is DERIVED → cache-tree, not signed as authored.~~
  **RETRACTED 2026-07-29 — this was WRONG.** See "DISTILLED IS PUBLISHED AND SIGNED" below.

### DISTILLED IS PUBLISHED AND SIGNED — and the signable unit is (item, FORM)
**Decided 2026-07-29; corrects the retracted claim above.** VERIFIED: `Distilled`/`DistilledBy` are
PUBLISHED bundle fields (`internal/bundles/bundles.go:325-326`, `yaml:"distilled,omitempty"`) —
distilled content ships WITH the bundle, it is not a local cache derivative. And the existing trust
model already demands per-form attestation: `bundles.go:388-390` — "blessing the raw form can never
validate a distilled exposure, and vice-versa."

- Both forms live in the CONTENT tree, both hashed in the manifest, both separately signable.
- Layout: sibling files — `fragments/solid.md` (raw) and `fragments/solid.distilled.md`. The common
  single-form case stays a single file and the form is visible in the path.
- `distilled_by` is metadata on the distilled file; `no_distill` on the raw file.
- ADDRESSING UNCHANGED: `Ref` carries no form component (`bundle#fragments/solid` addresses the
  ITEM); the form selects WHICH FILE is exposed, exactly as approvals key on (ref, form) today.
  Because forms are separate files with separate hashes and signatures, "raw never validates
  distilled" falls out of the STORAGE MODEL instead of needing enforcement.
- Rejected: (a) deliver-raw-only; (c) trust-the-local-transform — (c) would also have tempted an
  implementer to make `content_hash` load-bearing for trust, which is forbidden.

**INTERFACE CONSEQUENCE (human-flagged): form must be first-class in L0.** `Item.Content()` cannot
answer "which form", so the per-form accessor becomes the signable/attestable unit:
```go
type Item interface {
    Ref() trust.Ref
    Surface(ctx context.Context) (Surface, error)
    Forms(ctx context.Context) ([]trust.ContentForm, error)   // raw | distilled
    Form(ctx context.Context, f trust.ContentForm) (Form, error)
}
type Form interface {
    ContentForm() trust.ContentForm
    Content(ctx context.Context) ([]byte, error)        // deterministic bytes for THIS form
    Components(ctx context.Context) ([]Component, error)
    Signatures(ctx context.Context) (SigSet, error)
}
```
Layer 2 signs a FORM, not an Item: `SignForm(ctx, w, f Form, signer, ns)` /
`VerifyForm(ctx, f Form, root, now) (Verdict, error)`; `Writer.Put`/`PutSignature` take the form too.
This matches the existing trust keying `(kind, ref, form, bytes)` exactly — the signable unit was
always (item, form), and the earlier Item-level sketch quietly lost `form`.
- MANIFEST vs PIN = Option B (LAYER): git SHA stays the FETCH COORDINATE; the signed manifest-of-hashes is the TRUST AUTHORITY (wins on conflict). Do NOT rip out fetchAtLockedSHA/U093-F01 now; design the manifest so full replacement of the pin is a later ADDITIVE step. Manifest hashes AUTHORED files.
- content_hash stays SEPARATE from the manifest (distilled staleness vs authored-file integrity) and MUST be documented in docs/trust-model.md as EXPLICIT BOOKKEEPING — change-detection only, never a trust input (closes the conflation misread twice; see memory bundle-hash-vs-sig-trust).

DESIGN CONVERGED 2026-07-29 — ready to split into bundle-as-tree.design.md + restructure task; signing seam rides on top.

## THE ACCESS SURFACE (settled 2026-07-29) — the actual foundation
Human ask: "an access surface/interface for ctxloom CONTENTS, on which we can layer things like
the signing implementation." Signing is ONE consumer, not the point. Three layers, strictly ordered.

### Layer 0 — content access (storage-agnostic)
```go
type Store interface {
    Bundles(ctx context.Context) ([]BundleID, error)
    Open(ctx context.Context, id BundleID) (Bundle, error)
}
type Bundle interface {
    ID() BundleID
    Refs(ctx context.Context, kinds ...trust.ItemKind) ([]trust.Ref, error)
    Item(ctx context.Context, ref trust.Ref) (Item, error)
}
type Item interface {
    Ref() trust.Ref
    Content(ctx context.Context) ([]byte, error)    // EXACT authored bytes = the trust payload
    Surface(ctx context.Context) (Surface, error)   // decoded, TYPED form (see registry below)
    Signatures(ctx context.Context) (SigSet, error) // sig BYTES only — meaning lives in layer 2
}
type Writer interface {                             // IN SCOPE (decided) — needed by mild-shock authoring
    Put(ctx context.Context, ref trust.Ref, s Surface) error
    Delete(ctx context.Context, ref trust.Ref) error
    PutSignature(ctx context.Context, ref trust.Ref, ns Namespace, sig []byte) error
}
```

> **[L0 AS BUILT — the block above is the pre-S1 sketch. Read `internal/content/content.go`.]**
> S1 nested `Store -> Bundle -> Item -> Form` (bytes and signatures hang off FORM, not Item, because
> the attestable unit is (item, form)). S2 then added the BUNDLE-LEVEL half the F1 ruling called for:
> ```go
> type Bundle interface {
>     ID() BundleID
>     Refs(ctx, kinds ...trust.ItemKind) ([]trust.Ref, error)   // now FAILS on unclaimed files (F2)
>     Item(ctx, ref trust.Ref) (Item, error)
>     Files(ctx) ([]string, error)                              // TOTAL: dotfiles, .sigs, the manifest
>     ReadFile(ctx, relPath string) ([]byte, error)             // ADDED beyond the ruling — see below
>     Manifest(ctx) (Manifest, error)
>     BundleSignatures(ctx) (SigSet, error)                     // ADDED beyond the ruling — see below
> }
> type Writer interface {
>     Put(ctx, ref, f, s) error; Delete(ctx, ref) error; PutSignature(ctx, ref, f, ns, sig) error
>     PutManifest(ctx, id BundleID, m Manifest) error
>     PutBundleSignature(ctx, id BundleID, ns Namespace, sig []byte) error
> }
> ```
> **TWO METHODS BEYOND THE RULING'S FOUR, added deliberately per its own instruction** ("if you find
> yourself wanting `os.ReadDir` or `filepath.Walk` beside the store, that is the signal this API is
> incomplete — extend it deliberately"). `Files` alone can NAME the files a manifest must cover but
> cannot HASH them, so `BuildManifest` would have had to reach around the store to a filesystem —
> the precise failure the ruling exists to prevent; `ReadFile` closes it. And F1 named only the WRITE
> side of the bundle signature; without `BundleSignatures` nothing could read one back, so
> `VerifyBundle` would have had to glob `.sigs/` itself. No `os.ReadDir` or `filepath.Walk` appears
> outside the tree backend.

### SURFACE-TYPE REGISTRY (human-directed 2026-07-29) — replaces a flat `Meta`
Surface types REGISTER themselves with the content layer, each supplying its own DETECTION code and
decoder; the store then returns the APPROPRIATE TYPE. There is no uniform `Meta` struct and no
per-kind union — each surface owns its own shape, and a new kind (future opencode kinds) plugs in by
registering rather than by editing the store.

### REGISTRY, COLLAPSED (revised 2026-07-29 after Phase-A review — polymorphism, not data tables)
Human direction: "don't duplicate the registry; registry entrants provide explanation AND method
implementations; use the polymorphism and limit enums where they're not needed."

```go
// SurfaceType is registered per kind. THE REGISTRATION IS THE VOCABULARY —
// there is NO parallel content.Kind enum, and no data table mirroring it.
type SurfaceType interface {
    Name() string                                    // "fragments", "mcp", ... — the kind's identity
    Dir() string                                     // where its files live
    Detect(src Source) bool                          // does this source hold one of mine?
    Decode(src Source) (Surface, error)
    Encode(s Surface) ([]Component, error)           // write-side symmetry: a surface may emit N files
    Forms(src Source) ([]signing.Form, error)        // raw|distilled for content kinds; exec for mcp/hook
    RefFor(bundle string, src Source) (trust.Ref, error) // path<->ref translation lives HERE (hooks need it)
}

// Source is the ONE access abstraction Detect/Decode take. It spans every backend:
// authored tree (afero), remote bytes-at-pinned-SHA (NOT an afero.Fs), archive, builtin.
type Source interface {
    List() ([]string, error)               // component paths for this item
    Open(relPath string) ([]byte, error)
}

// Trust participation is an OPTIONAL INTERFACE, not a nullable field:
// kinds the trust gate governs implement it; PROFILES SIMPLY DO NOT.
type TrustGated interface{ TrustKind() trust.ItemKind }

func Register(t SurfaceType)
func As[T Surface](ctx context.Context, f Form) (T, error)
```
Consequences of the collapse:
- **No `content.Kind` enum and no `TrustKind` field.** "Profiles are not trust-gated" becomes a
  structural fact (they don't implement `TrustGated`) instead of a nil check someone must remember.
- **`signing.Form` is REUSED, never re-invented.** `trust.ContentForm` DOES NOT EXIST — an earlier
  sketch here invented it. The real types are `bundles.ContentForm` (raw|distilled) and
  `signing.Form` (raw|distilled|**exec**|""). mcp/hook are `exec`; reporting `raw` for them would make
  Layer 2 rebuild the WRONG countersign preimage. This was a live correctness bug, not a naming nit.
- **`Detect` takes a `Source`, never an `afero.Fs`** — the earlier signature contradicted the whole
  rationale for a domain interface by excluding the bytes-only remote backend.
- **`Decode` takes a `Source`, never `raw []byte`** — a skill is a DIRECTORY and cannot be decoded
  from a single byte slice.
- **`ComponentRole` is COLLAPSED AWAY.** One `Mode` enum on `Component` (see below) serves both
  materialize and review emphasis; prompt-vs-asset can be explained by the surface type if ever
  needed. Two overlapping enums were one too many.

```go
// Surface is the decoded, typed form of one item. Each kind implements it.
type Surface interface{ Kind() trust.ItemKind }

// SurfaceType registers one kind: where its files live, how to RECOGNISE them,
// and how to decode/encode them.
type SurfaceType struct {
    Kind   trust.ItemKind
    Dir    string                                        // from trust.ItemKind.Dir()
    Ext    string                                        // ".md" content surfaces, ".yaml" executable
    Detect func(fsys afero.Fs, path string) bool         // the detection code
    Decode func(raw []byte) (Surface, error)
    Encode func(s Surface) (raw []byte, err error)
}
func Register(t SurfaceType)

// Generic typed accessor (Go: interfaces can't carry generic methods, so this is
// a package-level function over the Surface returned by Item.Surface).
func As[T Surface](ctx context.Context, it Item) (T, error)
```
Concrete surfaces carry their OWN metadata — e.g. `Fragment{Body, Tags, Notes, ContentHash}`,
`MCP{Command, Args, Env, ...}`, `Skill{Files map[string]SkillFileMeta, ...}`, `Hook{Event, Index, ...}`.
Usage: `frag, err := content.As[Fragment](ctx, item)`.

### MULTI-COMPONENT SURFACES: Content is a DETERMINISTIC byte string (decided 2026-07-29)
`Item.Content` is ALWAYS a deterministic byte string, and it is what gets signed, hashed, compared
and approved for every kind:
- **single-file kinds** → the file bytes verbatim (trivially deterministic);
- **multi-file kinds (skill, any future multi-component kind)** → a CANONICAL COMPONENT DIGEST:
  sorted path + sha256 + mode, one line per component, fixed field order, explicit version tag.

This is a deliberate PARTIAL walk-back of "no canonical form" — kept ONLY for multi-component
surfaces, and using the SAME primitive as the bundle manifest (a deterministic manifest-of-hashes)
rather than per-kind bespoke rendering. One canonicalizer serves skills and every future multi-file
kind; there is no per-kind preimage logic. Precedent exists and is already deterministic:
`skill_archive.go` packs with sorted manifest order + zeroed mtimes, and `SkillManifest{sha256,mode}`
is exactly this shape.

**DIGEST FORMAT = `SHA256SUMS`, NOT INVENTED JSON (decided 2026-07-29).** Category = "signed manifest
of file digests"; a real standard exists and we adopt it rather than inventing. Format is the
coreutils `<hash>  <path>` shape, sorted, one line per component — verifiable with stock
`sha256sum -c`, and `SHA256SUMS` + detached `.sig` is exactly how distros ship signed releases, which
pairs naturally with our sshsig decision. Considered and rejected: in-toto/DSSE (proper supply-chain
standard, but its DSSE envelope collides with sshsig — revisit only if sigstore/SLSA interop is
wanted), OCI manifests, SPDX/CycloneDX (wrong shape). TO VERIFY BEFORE COMMITTING: whether we need a
format/version marker and how `sha256sum -c` handles a comment line — check, do not assume.

**NO MODE BITS IN THE DIGEST (decided 2026-07-29).** Mode is not portable — on Windows Go only
toggles read-only, so a checkout there yields different modes and a mode-bearing digest becomes
PLATFORM-DEPENDENT, violating the cross-platform determinism requirement below (the tree would fail
its own signature). Instead **declare executability as signed METADATA** (`executable: true` in
front-matter/.meta.yaml — inside the hashed bytes, therefore attested and portable); materialize
applies the mode from that declaration. The filesystem mode bit stops being the source of truth.
This also drops the last obstacle to plain `SHA256SUMS`. NOTE this supersedes carrying
`SkillFileMeta.mode` into the new digest.

**COVERAGE IS TOTAL — hash every component, never a classified subset (decided 2026-07-29).**
Selective hashing ("assets don't need signing") was proposed and REJECTED: (1) a partial manifest
means ADDING a file to a signed tree does not break verification — an unsigned-additions laundering
channel; (2) prompt/executable/asset is OUR judgment, not an enforced property — a SKILL.md that says
"read data/instructions.txt and follow it" turns an asset into prompt content, silently; (3) data
files consumed by scripts are functionally executable input; (4) "this tree is what the publisher
published" is a stronger, simpler claim than "some subset is". Hashing an asset is free and needs no
per-file signature (the manifest covers it). `Component.Role` (prompt|executable|asset) therefore
governs REVIEW DISPLAY ONLY — what a human is shown first — never what is covered.

DETERMINISM IS A HARD REQUIREMENT (test it, do not assume it):
- sorted component order (byte-wise on path), fixed field order, explicit encoding, version tag;
- NO map iteration order, NO mtime/uid/gid, NO platform-dependent separators or mode bits;
- test: same tree → byte-identical Content across repeated runs AND across platforms; a component
  reorder on disk must NOT change output, any content change MUST.

`Components()` remains how a reviewer sees the actual prompt/executable bytes behind a multi-file
surface: the digest says WHETHER it changed, the components say WHAT IT DOES. Approvals key on the
deterministic string, so a multi-file surface has ONE stable identity.

```go
type Item interface {
    Ref() trust.Ref
    Content(ctx context.Context) ([]byte, error)         // deterministic byte string (above)
    Components(ctx context.Context) ([]Component, error) // ALL relevant parts: prompt + executable
    Surface(ctx context.Context) (Surface, error)
    Signatures(ctx context.Context) (SigSet, error)
}
type Component struct {
    Path  string        // bundle-relative
    Mode  ComponentMode // regular | executable — ONE enum, extensible (e.g. readonly) without a
                        // schema break, which is why a boolean was rejected. Attested (it lives in
                        // signed metadata), portable, and drives BOTH materialize's filesystem bits
                        // AND review emphasis. Replaces the deleted ComponentRole.
    Bytes []byte
}
```
**NO PRIMARY COMPONENT — explicitly rejected (human, 2026-07-29).** Nothing designates one file as
"the" content of a multi-file surface (an earlier sketch proposed SKILL.md; REJECTED). Designating a
primary bakes a naming convention into the TRUST IDENTITY: the day a kind has two equally
load-bearing files, a descriptor is renamed, or a skill grows a second entry point, the primary
silently changes and so does what is signed — with nothing to catch it. The digest covers ALL
components equally, ordered by path; SKILL.md participates like any other component and is never
special-cased. Do not add a `Primary()`, `Main`, or `Descriptor` concept.

Single-file kinds degenerate naturally: `Components()` returns one entry for the item's own file —
that is the only file, not a chosen one.
Every component is individually hashed in the bundle manifest and individually signable; the
aggregate is never a separate signed artifact beyond `Content` itself.

### SIGNATURES COVER FILE BYTES (decided 2026-07-29) — no canonical preimage
A per-file `.sig` covers the FILE BYTES exactly as authored. There is NO per-kind canonical preimage:
`PublisherPreimage`, `SkillManifest.Serialize()`-as-preimage, the mcp "executable surface" rendering,
and form-dependent `ContentPayload` all drop out of the signing path.
- `Surface` therefore needs NO preimage method — `Item.Content` IS the preimage.
- Manifest and per-file sig now cover THE SAME bytes (previously we would have hashed one thing and
  signed another).
- Multi-file surfaces (skills) are covered by the manifest, so `SkillManifest.Serialize()` stops being
  a special case — the manifest generalizes it.
- Accepted cost: front-matter metadata lives INSIDE the signed bytes, so editing a tag invalidates the
  file's signature. That is correct/honest and matches git + `ssh-keygen -Y` semantics.
- STILL OPEN (last design gap): DISTILLED content is delivered into sessions but, under authored-only
  signing, carries no direct signature. Options: (a) deliver raw only; (b) sign distilled artifacts too
  (odd — derived, regenerable, lives in the unsigned cache tree); (c) verify the authored sig and treat
  distillation as a trusted local transform. Leaning (c). TRAP: do NOT implement (c) by linking
  distilled→authored via `content_hash` — that would make bookkeeping load-bearing for trust, which we
  explicitly decided against. A separate linkage is required.

NOTE this is where `Recognizer` properly belongs — as surface-type DETECTION in the content layer.
Dissolving it out of the SIGNING seam was right; it was the wrong home, not a wrong idea.
`Item.Content` stays the exact authored bytes (the trust payload) and must never be a re-serialization
of the decoded form.
- Writer is SEGREGATED: a pinned-remote store simply does not implement it (read-only by construction).
- `Item.Signatures` returning BYTES is deliberate: layer 0 knows WHERE sig bytes live (they are files
  in the tree); layer 2 owns what they MEAN. That is what lets signing add no FS access of its own.
- Backends this must span (why a domain interface, not just afero.Fs): authored tree (afero),
  remote git-at-pinned-SHA (BundleReader is bytes-only, NOT an afero.Fs), archive (skill_archive
  codec), builtin (embedded resources).

### Layer 1 — manifest / integrity, layered OVER the store (decided: NOT a method on Bundle)
```go
func BuildManifest(ctx context.Context, b Bundle) (*Manifest, error)
func LoadManifest(ctx context.Context, b Bundle) (*Manifest, error)
func (m *Manifest) VerifyContents(ctx context.Context, b Bundle) error // every file hashes
```
Store stays purely about content; integrity is a layer above it.

> **[BUILT 2026-07-30 on `bundle/s2` — the sketch above is superseded by the shipped shapes.]**
> `LoadManifest` dissolved into `Bundle.Manifest(ctx)` per the human's F1 ruling (the manifest is a
> FIRST-CLASS BUNDLE-LEVEL OBJECT reachable through the store, never a reserved item). L1 lives in
> `internal/content/manifest.go` — same package as L0, because a manifest is integrity only and
> carries no trust semantics. `Manifest` is a VALUE, not a pointer. What shipped:
> ```go
> const ManifestPath  = "SHA256SUMS"   // bundle-relative; the Digest format, `sha256sum -c`-checkable
> const BundleSigKey  = ManifestPath   // fixed sig-store key for the BUNDLE-level signature
> type  ManifestEntry struct{ Path, SHA256 string }
> type  Manifest      struct{ /* opaque */ }
> func  NewManifest(entries []ManifestEntry) (Manifest, error)
> func  ParseManifest(raw []byte) (Manifest, error)          // STRICT: re-renders and requires byte equality
> func  (m Manifest) Bytes() []byte                          // the bytes a bundle signature covers
> func  (m Manifest) Entries() []ManifestEntry
> func  (m Manifest) Lookup(p string) (string, bool)
> func  (m Manifest) Len() int
> func  (m Manifest) IsZero() bool
> func  ManifestCovers(p string) bool                        // false for SHA256SUMS and .sigs/**
> func  BuildManifest(ctx context.Context, b Bundle) (Manifest, error)
> func  (m Manifest) VerifyContents(ctx context.Context, b Bundle) error
> type  ContentsError struct{ Bundle BundleID; Missing, Mismatched, Unclaimed []string }
> ```
> **F2 CLOSED, both halves.** `VerifyContents` checks BOTH directions — every manifest entry hashes,
> AND every covered-eligible file on disk appears in the manifest — so a hostile publisher's `evil/`
> is caught by the reverse direction that nothing else would ever look at. And `Bundle.Refs` now FAILS
> LOUD (`content.ErrUnclaimed`) on any file inside a kind directory that no `SurfaceType` claims,
> instead of `walk()` dropping it in silence; the unclaimed set is computed by SUBTRACTION (all files
> under the kind dir minus everything an accepted candidate claimed) so it is complete by construction
> rather than by remembering to report at each decline point.
> **F3(c) DECIDED: the manifest covers everything EXCEPT itself and `.sigs/**`.** Both exemptions are
> STRUCTURAL, not policy — a file recording its own hash has no fixed point, and signatures are
> written after the manifest is signed. The undetectability of adding/removing a sig is safe, and the
> argument is written at `ManifestCovers`: removal cannot downgrade (bytes must still match the signed
> manifest, so the item stays manifest-attested, or drops to `tampered` — strictly more suspicious),
> and addition cannot upgrade (an untrusted key is inert; a trusted key over manifest-matching bytes
> only changes which trusted principal is named; a trusted key over contradicted bytes yields
> `content-substituted`). A tree-writer cannot CHOOSE which attestation governs in their favour.

### Layer 2 — signing/trust, layered over both
```go
func SignItem(ctx context.Context, w Writer, it Item, signer ssh.Signer, ns Namespace) error
func SignBundle(ctx context.Context, w Writer, b Bundle, signer ssh.Signer) error // builds+signs manifest
func VerifyItem(ctx context.Context, it Item, root signing.TrustRoot, now time.Time) (Verdict, error)
func VerifyBundle(ctx context.Context, b Bundle, root signing.TrustRoot, now time.Time) (Verdict, error)
```

> **[BUILT 2026-07-30 on `bundle/s2` — package `internal/content/attest`, beside `content` so L0
> never imports a trust root.]** What shipped:
> ```go
> func SignBundle(ctx, w content.Writer, b content.Bundle, signer ssh.Signer) error
> func SignItem(ctx, w content.Writer, it content.Item, f signing.Form, signer ssh.Signer) error
> func VerifyBundle(ctx, b content.Bundle, root signing.TrustRoot, now time.Time) (BundleVerdict, error)
> func VerifyItem(ctx, b content.Bundle, ref trust.Ref, f signing.Form, root signing.TrustRoot, now time.Time) (Verdict, error)
> type Status string    // unattested | manifest-signed | item-signed | content-substituted | tampered
> type Authority string // "" | manifest | item
> type Verdict struct{ Status Status; Principal string; Authority Authority; Detail string }
> func (v Verdict) OK() bool          // ONLY manifest-signed and item-signed
> type ItemVerdict struct{ Ref trust.Ref; Form signing.Form; Verdict }
> type BundleVerdict struct{ Bundle content.BundleID; Verdict; Manifest content.Manifest; Contents error; Items []ItemVerdict }
> ```
> **F3(a) CLOSED STRUCTURALLY, not by a check.** `SignItem`/`SignBundle` take NO namespace parameter
> and verification reads only `NamespacePublish`; `signing.VerifyPublisher` in turn only verifies
> against keys the trust root authorizes for that namespace. Pinning is doubled: an approve-namespace
> signature is never looked at, and an approve-only key never verifies one. Making the namespace an
> argument is exactly what would let a caller turn a review record into a publication.
> **F3(b) CLOSED: the new distinct verdict is `StatusContentSubstituted`.** Most-specific-wins now
> applies ONLY where the two attestations AGREE about the bytes. When a trusted publisher's signed
> manifest contradicts the bytes on disk and those bytes carry a valid publish signature from another
> trusted key, that disagreement IS the finding: `content-substituted`, `OK() == false`, with a Detail
> naming both principals. Absence and conflict are no longer conflated.
> **F10 CLOSED AT BUNDLE LEVEL** (it stays open per-item): the bundle signature is filed at the FIXED
> `BundleSigKey` rather than a content-derived one, so rewriting the manifest to cover an added file
> leaves the old signature reachable, failing → `tampered`, instead of moving it out from under its own
> key → `unattested`. Attestation cannot be stripped by editing. The rename-cannot-orphan argument for
> content-keying does not apply: a bundle has exactly one manifest at exactly one path.
`Signable`/`Recognizer` DISSOLVE — `Item` already exposes content + signatures, so the separate
signing seam is redundant. One less abstraction than the earlier plan (stage-1 commit aa38b9fd is
superseded, not reworked). Other layer-2+ consumers: trust gate (Content→EffectiveTrust),
distillation, search_library composition (overt-silo), review enumeration, materialize,
directory-form remote fetch (engaged-chivalry).

### Reference addressing — UNCHANGED (verified)
`Ref.Key()` is already `Bundle + "#" + Kind.Dir() + "/" + Name` (internal/trust/trust.go:204-206) —
`Kind.Dir()` ALREADY returns a directory name, so addressing was already tree-shaped and only STORAGE
was a document. `code-quality#fragments/solid` → `<root>/code-quality/fragments/solid.md`. The `#`
keeps its natural URL meaning (bundle root vs path within). Ref grammar, profiles, and approval keys
are untouched; only RESOLUTION changes (map-lookup → path resolution). New rule needed: kind→extension
(.md content surfaces, .yaml executable surfaces) + hooks keep the ordinal `hooks/<event>/NN-name`.

### Countersign staling — ACCEPTED (decided)
Restructuring changes item bytes, so every existing countersigned approval goes stale and items revert
to pending review. Human decision: acceptable. S1 therefore carries NO byte-identical constraint;
re-approval happens as part of S4's migrate+re-sign pass.

## Folded-in requirements (from ticket triage 2026-07-29)
The restructure must satisfy these (each was an open ticket now parked against this work):
- **Directory-form remote fetch (engaged-chivalry):** resolver/fetcher detects `<name>/bundle.yaml`, fetches the WHOLE tree (skills/** + every per-item file), verifies per-file sigs + manifest. Today NO directory-form bundle (thus no skill) is distributable — headline payoff.
- **Atomic multi-file publish (pushy-tree):** all-or-nothing publish of N item files + `.sig` siblings + manifest; current two-call file+sig is non-atomic and scales badly.
- **Tree-aware move/transfer (tasty-disk):** treat a dir bundle as the whole directory — preserve dir name as identity, carry full payload + sigs + manifest, atomic, fail-loud on partial.
- **One unified path convention, any repo as source/target (jaded-rust):** identical layout for in-repo and dedicated bundle repos; bundle-presence discovery; trust keys on source ref (local when you stand in it, gated when consumed elsewhere).
- **Migrate + re-sign the two consumed repos (smoky-doze):** ctxloom-default & -personal → new tree + per-file `.sig` + manifest, publish allowed_signers, delete reprise.yaml, clean-machine E2E (pull→offline verify→zero prompts). The contract's proving ground. (cruel-flyer + high-rogue re-signs roll into THIS pass.)
- **Per-item authoring surface (mild-shock):** `ctxloom bundle <kind>` add/remove/list/show creates/deletes the per-item file + writes its manifest hash so it enters the trust gate.
- **Loud version/contract mismatch (homey-quack):** the new verifier rejects/warns on unknown payload/manifest version with a diagnostic, never silently "pending" (carry exec-preimage version-as-identity forward).
- **Acceptance coverage (outer-water sign-half, cheap-pug move-half):** new tree-aware `bundle move` (2nd remote fixture) + new `ctxloom sign` (hermetic key fixture) ship with scenarios. (session-watch + `signer list` halves stay independent.)
- **APPROVALS READ SURFACE (wise-slot — supersedes spare-penny ADR):** the rewritten per-file `.sig` approvals store EXPOSES a `ctxloom approvals list` + `review --diff` read/inspect surface. Reverses the "no approvals-list feature" ADR — a per-file sig store is inspectable and a read surface is cheap+useful. spare-penny ADR to be AMENDED to record the supersession.
- **Bundle-composition query on the FS-walk store (overt-silo):** the restructure BREAKS the current cache-YAML scrape (dir tree, not one doc) → search_library/init must answer composition from the new FS-walk store, not by scraping.
- **Builtin-ref enumerate/review (sworn-drab item 1):** the new trust/review surface can enumerate + review builtin refs.

## Staged build — REVISED for the pivot (supersedes the old 6-stage signing-only plan)
Each stage its own commit; TDD; coordinator reviews trust diffs + runs full suite + acceptance at each merge.

**S1 — Access surface + tree format (the foundation; only true bottleneck).**
Layer-0 interfaces (Store/Bundle/Item/Writer) + their TREE implementation. Promote fragment/command/
mcp/hook to files (skills already are); front-matter in .md, .meta.yaml elsewhere; kind=dir; hook
ordinal filenames; kind→extension resolution. FS-walk replaces ParseBundle/the whole-document parse.
Migration CONVERTER (a converter, not a dual-format reader — per no-backward-compat-shims).
Per-kind promotions parallelize within this stage.

**S2 — Manifest + signing over the tree. [DONE 2026-07-30, branch `bundle/s2`.]**
Layer 1 (BuildManifest/LoadManifest/VerifyContents) then layer 2 (SignItem/SignBundle/VerifyItem/
VerifyBundle) on internal sshsig. Per-file `.sig` + signed manifest-of-hashes; precedence
MOST-SPECIFIC-WINS (per-file overrides manifest); mixed provenance allowed.
CORRECTION FROM THE BUILD: most-specific-wins holds ONLY where the two attestations AGREE about the
bytes. Where a signed manifest contradicts the bytes on disk, a per-file signature does NOT override
it — that is `content-substituted`, a surfaced verdict, not a silent substitution. `LoadManifest`
became `Bundle.Manifest`. See the "BUILT 2026-07-30" banners on Layer 0/1/2.

**S3 — Distribution + trust surfaces.**
Directory-form remote fetch (engaged-chivalry); atomic multi-file publish (pushy-tree); tree-aware
move (tasty-disk); unified path convention any-repo (jaded-rust); manifest as trust authority LAYERED
over the git-SHA fetch pin; approvals list/diff read surface (wise-slot, supersedes spare-penny ADR);
composition query on the FS store (overt-silo); per-item authoring via Writer (mild-shock);
loud version/contract mismatch (homey-quack).

**S4 — Prove, migrate, document.**
Migrate + re-sign ctxloom-default & -personal incl. RE-APPROVAL after countersign staling (smoky-doze,
absorbing cruel-flyer + high-rogue); clean-machine E2E (pull→offline verify→zero prompts); rewrite
docs/trust-model.md (new model + content_hash documented as EXPLICIT BOOKKEEPING, never a trust input);
acceptance coverage for the new `sign` + tree-aware `move` (outer-water/cheap-pug halves).

SUPERSEDED: stage-1 commit aa38b9fd (Signable+SigPath) — `Signable`/`Recognizer` dissolve into `Item`,
so that commit is superseded rather than reworked.

## Decisions & corrections (2026-07-29, review — session vital-deaf-stunt)
> **[RETRACTED — do NOT build this. Superseded by a later decision in this same document; kept only because removing it would lose the reasoning. See the review findings section at the end.]**

- **DECIDED: DROP `Signable.Kind()` entirely.** (Supersedes an earlier same-day decision to
  promote `trust.ItemKind("bundle")` to a real `KindBundle` constant — REVERSED.) Verified:
  `Kind()` has ZERO consumers — `SignItem` uses only preimage + sig I/O, nothing switches on a
  returned enum. The concrete `Signable` type IS the identity. Dropping it deletes the
  fabricated `trust.ItemKind("bundle")` literal, the `KindBundle` question, and the
  `operations→trust` coupling on the sign side. Kind still lives on the VERIFY side only
  (`trust.Ref.Kind` + existing `computeItemPayloadPair` rebuild a found `.sig`'s preimage) —
  unchanged, and needs no `KindBundle` (bundles don't gate through EffectiveTrust).
- **DECIDED: unified I/O seam.** Fold sig read+write INTO `Signable` (`ReadSignature`/
  `WriteSignature`); drop `SigPath() string` and the external `afero.Fs` param on `SignItem` —
  the item owns its fs binding. `[]byte`, not `io.Reader` (preimages are small; matches
  `signing.Sign`). Seam also reads CONTENT (== preimage for bundle; != for skill, whose
  preimage is `manifest.Serialize()`).
- **DECIDED: pluggable disambiguator.** A `Recognizer` registry maps a file/directory path →
  `Signable`; first-match-wins; a new kind registers a recognizer instead of editing
  `ctxloom sign`. This is the "future opencode kinds plug in" extension point.
- **DECIDED: bundle.yaml `content_hash` is NOT superseded by sigs and is NOT touched by this
  work.** It is distillation change-detection ("an INDEX never an authority", bundles.go:409),
  never a trust input. Item trust = whole-bundle publisher sig over EXACT bytes + live re-hash
  + per-item countersign records. Per-item PUBLISHER sigs only fill the loose/imported-item gap
  (F26(b)); stored as sibling `.sig` ("on"), never embedded in bundle.yaml. `ctxloom sign
  <bundle>` emits NO recursive per-child sigs. Subcomponent extraction is VERIFY-side only.
- **STAGE 1 REWORK:** the committed seam (aa38b9fd) has `Kind()` + `SigPath()`; both change
  under the above — stage 1 gets reworked to `{PublisherPreimage, ReadSignature, WriteSignature}`
  + the recognizer registry, not extended.
- **Code-shape corrections (design shorthand vs real signatures):**
  - `VerifyPublisher(bundleBytes, armoredSig []byte, root TrustRoot, now time.Time) (string, error)`
    — takes `now`, RETURNS the verified principal. (design wrote it 3-arg, void.)
  - Publish namespace constant value is `NamespacePublish = "publish.v1.ctxloom.dev"` (not the
    `"publish.v1"` shorthand used above).
  - `Signable` / `PublisherPreimage` are entirely NEW — the existing countersign polymorphism
    is procedural (`computeItemPayloadPair`'s `switch tRef.Kind` + per-kind `ContentPayload`),
    not an existing interface. The seam wraps those pieces; it does not mirror an existing one.

## Open (decide as we build, not blockers)
- Exact `ctxloom sign` command home (unify top-level vs keep under a group) — settle in stage 2.
- Whether `prompt`/`mcp`/`hook`/`fragment` kinds get Signable now or later (skill + bundle first; others are the same pattern).

---

## MIGRATION IS NON-DESTRUCTIVE (human, 2026-07-29): "leave them alongside"

The converter writes the tree **alongside** the existing monolithic `bundle.yaml` and LEAVES
THE ORIGINAL IN PLACE. It does not replace, move or delete it. (My reading of the
instruction — if what was meant was instead "keep the converter code as a sibling of the
loader rather than folding dual-format support into it", that is also satisfied by this
shape, since the loader gains no second path. If a THIRD reading was intended, correct this
paragraph; everything below depends on it.)

**This partially SOLVES F11's silent-disappearance problem, which is a real gain.** F11: an
old client shown a tree-form bundle sees ZERO items with ZERO errors, because `ParseBundle`
is non-strict. If the monolith stays on disk, an old client keeps reading the monolith and
keeps working — the silent-zero-items case only arises for a bundle that never had one.
Non-destructive migration is therefore also the compatibility story the design was missing.

**But it creates a NEW hazard that must be specified before S2 ships — two sources of truth:**
1. **Precedence.** If both `bundle.yaml` and the tree exist, which wins? `Loader.Find` today
   resolves `<name>.yaml` BEFORE `<name>/bundle.yaml`, so a naive implementation would keep
   serving the MONOLITH after migration and the tree would be inert — the migration would
   appear to succeed and change nothing, which is this project's characteristic bug.
   The tree must win once present, and that inversion must be deliberate and tested.
2. **Drift.** A user who edits the leftover `bundle.yaml` after migrating gets silently
   ignored. That is the same silent-no-op in the other direction. Options: mark the monolith
   (a header comment plus a rename to something the loader never resolves, e.g.
   `bundle.yaml.migrated`), or warn loudly when both exist and the monolith is NEWER than the
   tree. The second is better — it catches the real mistake instead of relying on a comment
   nobody reads.
3. **Signing.** The leftover monolith must NOT be covered by the tree's manifest (it is not
   part of the published tree), and must not be enumerable as an item. This interacts directly
   with F2: enumeration must FAIL LOUD on unclaimed files, so the leftover needs an explicit
   exemption or F2's fix will reject every migrated bundle.

**RESOLVED 2026-07-29 (human): a config setting permits extraneous files, DEFAULTED ON, with a
task to clear it.** This settles the F2-vs-migration tension directly. Proposed shape:

```
bundles:
  allow_extraneous_files: true   # default; migration leftovers and unclaimed files are
                                 # permitted rather than rejected
```

**REFINEMENT — ACCEPTED BY THE HUMAN 2026-07-29. Permissive must not mean silent, because the alternative is this project's characteristic bug:
permissive must NOT mean SILENT.** With the setting on, enumeration does not FAIL — but it
must still REPORT every file it permitted, by path. Three reasons this is not optional:
1. A permissive default that says nothing is indistinguishable from no check at all, and F2's
   hazard stays live: a hostile publisher's extra directory rides along through `VerifyBundle`
   with every signal green.
2. The same failure mode as the CCN gate found red-and-ignored earlier today — a check nobody
   sees trains everyone to assume there is nothing to see.
3. **The warning list IS the worklist for clearing the setting.** Warn-now-fail-later gives the
   migration path for free: when someone flips the default, the accumulated warnings already
   say exactly what would break. Without the warnings, flipping it is a blind change.

So: `false` → refuse and name the unclaimed files; `true` (default) → permit and WARN naming
them. The mis-extensioned-hook case (§364-365's silently withheld `pre_tool` guardrail,
reachable by typo) is then caught by the warning even under the permissive default, which is
the single most valuable thing this setting buys.

**Consequence for the strict-mode design:** F2's "enumeration FAILS LOUD" becomes
"enumeration reports loudly, and fails when `allow_extraneous_files` is false". The
both-directions `VerifyContents` check from F2 is unaffected and still required — this setting
governs ENUMERATION of unclaimed files, not manifest coverage verification. Do not let an
implementer weaken the manifest check on the strength of this setting.

Item 3 is the one that will bite:

 F2's "fail loud on unclaimed files" and "leave the monolith
alongside" are in direct tension, and whichever is implemented second will look like a bug in
the first. Decide the exemption when F2 is designed, not afterwards.

## ADVERSARIAL REVIEW FINDINGS (Fable, 2026-07-29)

**Verdict: buildable WITH CONDITIONS.** S1 as scoped is genuinely buildable and the
`s1/content-access` branch proves it (full suite green at `43d08123`, 51 architectural gates).
**S2/S3 are NOT buildable as written** — F1, F3 and the encoding collision must be resolved first.
Citation audit: 41 checked, 4 substantive failures, 9 stale-line-only.

> **F1, F2 and F3 are CLOSED as of 2026-07-30 (branch `bundle/s2`).** F1 by the human's EXPLICIT
> MANIFEST API ruling (manifest-as-reserved-item considered and REJECTED — do not reintroduce it as a
> simplification); F2 by two-directional `VerifyContents` plus fail-loud enumeration; F3 by
> namespace pinning, the `content-substituted` verdict, and an explicit `.sigs`/manifest coverage
> decision. See the "BUILT 2026-07-30" banners on the Layer 0/1/2 sections for what shipped. The
> findings below are kept as the RATIONALE — they are why the code looks the way it does — not as
> open blockers.

### F1 — L1 and L2 are UNIMPLEMENTABLE over L0 as specified (blocks S2/S3). CONFIRMED.
`Bundle` exposes only `ID/Refs/Item` and `Writer` only `Put/Delete/PutSignature` — all
item+form-scoped. But L1 declares `LoadManifest(ctx, b Bundle)` with no way to READ a manifest
through `Bundle`, and L2's `SignBundle` "builds+signs manifest" with no way to WRITE the manifest
or a bundle-level signature through `Writer`. `VerifyContents`'s "every file hashes" cannot check
COMPLETENESS either, because `Bundle` cannot enumerate non-item files — it can only verify files
it already knows about.
FAILURE: the implementer reaches S2, finds the interfaces do not span the manifest, and invents
ad-hoc filesystem access beside the store — defeating this design's own "signing adds no FS access
of its own".
**Resolve before S2 starts, as a design decision rather than an implementation improvisation:**
add explicit bundle-level raw access (`Bundle.Manifest()` / `Writer.PutManifest`, or
manifest-as-reserved-item).

### F2 — "this tree is what the publisher published" is UNENFORCEABLE. CONFIRMED.
This design rejects selective hashing precisely because "ADDING a file to a signed tree does not
break verification — an unsigned-additions laundering channel". But coverage-is-total is
implemented PER-ITEM: at bundle level `tree.go`'s `walk()` SILENTLY DROPS any candidate group no
`SurfaceType` claims — no error, no diagnostic — and a manifest built from `Bundle` can only cover
enumerated item components.
FAILURE: a hostile publisher adds `evil/` or `fragments/payload.txt` to a signed bundle; nothing
enumerates it, the manifest never covered it, `VerifyBundle` PASSES, and the file rides along in
every pull, move and materialize. The same mechanism makes a MIS-EXTENSIONED HOOK (`guard.yml`)
silently vanish — this document's own nightmare of a silently withheld `pre_tool` guardrail,
reachable by typo.
**Resolve:** `VerifyContents` must check BOTH directions (every manifest entry hashes AND every
file on disk is manifest-covered or explicitly exempt), and enumeration must FAIL LOUD on
unclaimed files inside kind directories.

### F3 — MOST-SPECIFIC-WINS precedence is unsafe as written at three points. PLAUSIBLE (L2 unbuilt).
The headline attack does NOT work: an attacker's per-file sig from an untrusted key simply fails
verification. Downgrade-by-specificity is prevented by cryptography — but the precedence TEXT has
not caught up, and three gaps remain:
- **(a) namespace not pinned.** "Per-file sig overrides the manifest" never says the overriding sig
  must be `NamespacePublish` from a trust-rooted key; the store accepts any validated namespace and
  `readSignatures` returns all of them. A sloppy L2 lets an APPROVE record satisfy a PUBLISH slot —
  an assertion-strength downgrade of exactly the class the countersign work just closed.
- **(b) conflict vs absence conflated.** The fallback rule covers the ABSENCE case; the override
  rule says per-file wins "on conflict". So a file whose bytes MISMATCH the trusted publisher's
  manifest but carry a valid sig from any OTHER co-trusted key verifies SILENTLY. Any co-trusted
  key can substitute content inside another publisher's bundle with zero diagnostic. Mixed
  provenance is declared "FINE" without requiring it be VISIBLE.
- **(c) `.sigs/` and the manifest's own coverage undecided.** If the manifest covers `.sigs/`,
  adding a signature breaks the bundle signature (circularity). If it does not, adding OR REMOVING
  sig files is undetectable — and removing a per-file sig silently flips authority back to the
  manifest, letting a tree-writer choose which attestation governs.

### F4 — ENCODING VERDICT for the countersign collision: adopt the COMPOSITE closed enum.
The review's reasoned verdict, with the reasoning: the two proposals are **informationally
identical** — the composite is a canonical encoding of the (kind, form) pair, and BOTH preserve the
invariant this document actually defends (never kind-blind: a fragment keys `fragment/raw`, a hook
`exec/hook`, so the text→exec escalation is equally closed). **This document's "must not be
simplified further" objects to a strawman** — 4.D does not drop the kind distinction, it re-encodes
it. Closedness is moreover CORRECT for a preimage vocabulary: what verifies must not depend on
which plugins are loaded. And the trust vocabulary was already closed (`TrustGated.TrustKind()`
returns `trust.ItemKind` constants), so "no parallel enum" overstates — the parallel enum exists
today.
**FOUR MANDATORY CORRECTIONS if the composite proceeds — see task for the live-conflict details:**
1. Publish the explicit mapping {`trust.ItemKind`, legacy `signing.ItemKind`} → composite, and
   DECIDE what `agentskills` becomes. Three vocabularies are live: the composite says `command/*`,
   the real kind is `trust.KindPrompt` ("prompt", dir "prompts"), and the header's CURRENT
   vocabulary is `signing.ItemKind` = {fragments, skills, mcp, hooks, agentskills} where legacy
   "skills" meant COMMANDS. Ship without the mapping and migration mis-keys approvals.
2. **The composite CANNOT be `signing.Form` wholesale.** L0's file layout needs the plain
   raw/distilled axis — the filename suffix is `.distilled.md` and cannot carry
   `fragment/distilled`. TWO types are required (LAYOUT form vs ATTESTATION form) with ONE
   exhaustive derivation function. Neither document says this. **And `s1/content-access` threads
   `signing.Form` INCLUDING `FormExec`, which 4.D deletes — s1 and 4.D cannot both merge as-is.**
3. One commit, per plane-2's own atomicity requirement.
4. Rewrite this document's §250-254 to state the INVARIANT (kind-discriminated) and cite 4.D as
   the encoding, rather than specifying a competing one.
Migration cost is identical per proposal — but TWO contract bumps if they land separately, i.e.
two global re-review campaigns for one logical change.

### F5 — within-event hook ordering silently changes from AUTHORED order to LEXICOGRAPHIC. CONFIRMED.
Today `BundleHooks` stores per-event YAML ARRAYS appended in authored order, so array POSITION is
intra-bundle execution order. Under file-per-hook the tree enumerates sorted by NAME. The earlier
retraction of a per-hook `order` field proved only that no MERGE-TIME field is consulted; it missed
that the authored array position was the carrier.
FAILURE: a bundle whose event runs [guard, audit] migrates to files and thereafter runs
[audit, guard], with no diagnostic. **Resolve:** state it, and either accept name-order as the new
contract (documented in the migration) or make the converter REFUSE events whose behaviour is
order-sensitive.

### F6 — `EffectiveContentHash` is cited as the live exposure mechanism and has ZERO production callers. CONFIRMED.
The function exists but is called only from tests; the gate consumes payload BYTES directly. The
protective effect claimed is real (live bytes are what get judged) but the named mechanism is dead
code with a stale doc comment — the same "asserted to exist in production" class this review was
told to hunt. **Resolve:** reword to name the gate-over-payload-bytes path; fix the stale comments.

### F7 — the FINAL decision DELETES the approval-context audit trail rather than protecting it. CONFIRMED contradiction.
The keep-the-index paragraph ("the ONLY record of the context in which you approved something …
your own audit trail") is contradicted 50 lines later by "DELETE index.yaml; approved-manifest
snapshots replace it" — same day, no supersession marker on the earlier text. Under the final
state the per-item approve records ARE signed and DO pin bytes, but the CONTEXT is protected
nowhere: `ref` leaves the signed header, and `reviewed_at` was never in the signed data (verified —
no timestamp) and the document explicitly declines to add one. The manifest snapshot recovers the
diff base and is tamper-evident only TRANSITIVELY, via a chain this document implies but never
states as a requirement. **Needs an explicit human decision, not a leftover contradiction.**

### F8 — distillation bookkeeping stales the RAW form's signature and approval. CONFIRMED shape.
`mdMeta.ContentHash` lives in the raw file's front-matter, and front-matter is INSIDE the signed
bytes. Every distill run that updates `content_hash` rewrites the raw file → raw digest changes →
the raw publisher signature and the raw approval stale, even though the body a human reviewed did
not change. FAILURE: routine re-distillation silently re-pends every raw fragment approval.
**Resolve:** move distillation bookkeeping out of the raw file's hashed bytes — this document
already brands `content_hash` "explicit bookkeeping, never a trust input".

### F9 — skill archive round-trip unreconciled; mode has two sources of truth. PLAUSIBLE.
The skill sidecar sits BESIDE the package so the package stays a pure Agent Skill tree, but
`skill_archive.go` packs the package tree with its own FS-derived `mode`. If the archive EXCLUDES
the sidecar, the extracted item's component set differs → `Content()` differs → **every signature
stops resolving** (content-keyed lookup) and declared executability is lost. If it INCLUDES it, the
archive format changed and `SkillManifest.mode` now conflicts with the sidecar's `executable:` list
which this design says supersedes it — and nothing reconciles the codec. **Resolve:** define the
archive's component set as exactly `Form.Components()` and retire `SkillManifest.mode` or define it
as derived output.

### F10 — "tampered → reject" is UNREACHABLE under content-keyed signature storage. CONFIRMED by construction.
Signatures are looked up by the hash of the CURRENT content, so tampering moves the key and the old
signature becomes unreachable: the item presents as UNATTESTED/PENDING, not TAMPERED/REJECT. Only
the manifest layer can distinguish the two, so per-item tamper detection has migrated entirely into
L1 — and if L1's conflict handling is weak (F3b), "tampered" quietly becomes "pending".

### F11 — old readers and old companions. CONFIRMED code-side, unresolved.
`ParseBundle` is non-strict, so an old client shown a tree-form bundle sees ZERO items with ZERO
errors. Worse, old ltk/taskloom binaries emit the OLD single-document loadout with NAMELESS hooks
indefinitely, and the legacy-loadout→tree adapter — including hook-name synthesis stability and
ordering — is required and entirely undesigned.

### Minor
`Writer.Put` never removes components the new surface no longer encodes (a removed skill file or an
emptied sidecar stays on disk — stale bytes still grouped and hashed). `.sigs` grows monotonically
with no GC story: deliberate for rejects, unstated for publish/approve.

### Citation failures (4 substantive)
1. `EffectiveContentHash` as the exposure mechanism — zero non-test callers (F6).
2. The "an INDEX, never an authority" quote documents the `HashPayload` PRIMITIVE; the
   `content_hash`-field half lives elsewhere. Two comments fused into one cite.
3. plane-2 §4.D's "every kind is gated under FormRaw" is FALSE for fragments/commands, which pass
   the live form (raw or distilled); only hooks/mcp/skills hard-wire `FormRaw`.
4. plane-2's `skill_archive.go:661` FormExec cite is the wrong line (the only mention is `:769`).
