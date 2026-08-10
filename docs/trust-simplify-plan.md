# Trust-Simplify Implementation Plan

> ## SUPERSEDED — completed plan record (2026-07-13)
>
> This plan **landed**, and was then **superseded in part**. It is retained as a
> historical record (and because code comments cite it); it is **not** a description
> of current behavior.
>
> - Normative behavior: **[trust-model.md](trust-model.md)**.
> - Wire contracts and payload framing:
>   **[signature-envelope.spec.md](signature-envelope.spec.md)**.
>
> **What in here is now FALSE:** the trusted-sources mechanism — `trust_bundles: true`
> in `remotes.yaml`, `ctxloom remote trust|untrust`, `remoteTrusted()`,
> `Remote.TrustBundles` — is **deleted**, not deprecated. A trusted *location* has
> been replaced by a trusted *signing identity* in `allowed_signers`
> (signature-envelope spec §11). The `trust.yaml` acceptance ledger and hash denylist
> are likewise gone, replaced by the countersignature stores under
> `~/.ctxloom/approvals/` and `.ctxloom/approvals/`.
>
> The open item this plan left at its end — "non-interactive / CI story beyond
> committed `trust.yaml`" — is answered by signature-envelope spec §9.2.1.

Replace the seven-mechanism, two-layer trust model with a three-state review
model plus a first-party source exemption. Taskloom: `fair-slum`.

## Invariant

A human sees third-party content — including every update to it — before the
LLM does. First-party sources are exempt: local content (authored here),
builtin bundles (shipped in the binary), and the trusted-sources set
(ctxloom-default and the user's personal remotes).

## Design

### Item states

Every remote item (fragment, skill, MCP server, hook, tooling declaration) is
in exactly one state:

- **pending** — never reviewed, or content changed since acceptance. Withheld.
- **accepted** — a human reviewed this exact content. Bound to the
  (raw, distilled) hash pair present at review; a change to either form
  returns the item to pending.
- **rejected** — a human declined it. Withheld permanently; recorded as both a
  ref block and a content-hash denylist entry so a renamed identical copy
  stays rejected.

### Decision function

Evaluated at every exposure choke, first match wins:

1. rejected → deny
2. local | builtin | trusted source → allow
3. accepted at current hash pair → allow
4. otherwise → pending: withhold, count toward the pending notice

Rejection beats the first-party exemption: a user can reject an item even from
a trusted source or a builtin. An unreadable trust store denies everything
(fail closed), and in strict mode (fail-loudly workstream) is fatal.

### Trusted sources

A set of source repos whose content is exempt from review, updates included.

- Ships with `ctxloom-default`.
- `init --remote` adds personal repos to it; `ctxloom remote trust <name>`
  adds/removes with a confirmation that names the consequence ("everything
  this source ever publishes reaches the agent unreviewed").
- Third-party remotes default untrusted; their content is born pending.

### The review ceremony

`ctxloom review` is the single porcelain:

- Walks pending items grouped by bundle; shows full content for new items and
  a **diff against the previously-accepted version** for updates.
- Executables (MCP, hooks) display as what they run: command, args, env.
- Actions per item: accept / reject / skip; per bundle: accept all remaining.
- Acceptance records the hash pair; rejection records ref block + content hash.
- `init`'s interview ends with a review session when pending items exist.

`ctxloom bundle trust <ref>` and `ctxloom blacklist <ref>` remain as scriptable
plumbing that write the same accepted/rejected states.

### Deletions

- **Bundle posture** (`bundle trust/untrust`) — replaced by accept-bundle
  inside review (hash-bound to what was seen).
- **First-run baseline** (TR6) — a fresh store starts all-pending; one review
  session replaces the silent blessing.
- **Pending/active lockfile split as security** — the lockfile becomes pure
  dependency pinning. `remote pull`/`upgrade` move pins freely; exposure gates
  at the content layer. `bundle review`, `show-pending`, `approve`, `decline`
  fold into `ctxloom review`; `hold`/`unhold` survive as dependency management.
- **Blind mode's security significance** — sync always pulls without ceremony;
  safety is that unseen content is pending, announced by one loud line.
- **Form-flip escape** — closed by hash-pair acceptance.

## Storage

`trust.yaml` v2:

```yaml
version: 2
sources:                    # trusted-sources set (beyond builtin/local)
  - https://github.com/ctxloom/ctxloom-default
items:
  - ref: <canonical-repo>@bundles/<bundle>#<kind>/<name>
    state: accepted | rejected
    raw_hash: <sha>         # accepted only
    distilled_hash: <sha>   # accepted only, when a distilled form exists
    reviewed_at: <ts>
denylist:                   # content hashes of rejected items
  - <sha>
```

Committable: a team or CI inherits review decisions a human already made.
`remotes.yaml` keeps `trust_bundles` as the sources-set membership flag
(renamed semantics, same field) or migrates to the sources list — decide in
slice 1 (recommendation: keep the flag, derive the list, one less migration).

## Migration

No grandfathering (pre-1.0, no-compat policy):

- Existing explicit grants translate to `accepted` where the stored hash still
  matches current effective content (record the pair by rehashing both forms).
- Existing blacklist/denylist entries translate to `rejected`.
- Bundle postures and the baseline marker are dropped; content they covered
  lands pending.
- Existing `trust_bundles: true` remotes join the trusted-sources set —
  they were already exempt, so this preserves user intent.
- One notice on first run after upgrade: "N items await review."

## Slices

1. **State store + resolver.** New `trust.yaml` schema + migration; the
   four-line decision function behind the existing `EffectiveTrust` seam so the
   six chokes (content gate, MCP/hook extractors, command export, tooling,
   list stamper) don't move; trusted-sources set wiring; `remote trust`
   confirm. Acceptance: all chokes enforce the new states; migration
   round-trips a populated v1 store.
2. **Review porcelain.** `ctxloom review` interactive flow (content, diffs,
   accept/reject/skip/accept-bundle); `trust`/`blacklist` rewritten onto the
   new states; init-interview wiring; pending-count notice at startup.
   Acceptance: a third-party bundle's items are withheld until reviewed, then
   exposed; an upstream edit re-gates exactly the changed items and review
   shows the diff.
3. **Demolition. — DONE (breaking sweep).** Removed the pending-lockfile
   "security layer" (`lock.pending.yaml` + `WithPendingLockfile`/
   `PendingCounterpart`/`StageUntrustedNew`/`Staged`, the active↔pending diff
   `DiffLockfiles`/`BundleChangeSet`/`PendingBundleChanges`, and the
   `bundle review/approve/decline/show-pending` family) and blind mode (the
   `Blind` pull option + the pull-time `securityReview` gate and its
   `SecureContent`/`SecurityWarning` model + `remote update --blind`).
   `remote pull`/`upgrade` now move pins straight to the active lock;
   `StageUpgrade`/`ApproveUpgrade` collapsed into `UpgradeDependencies`
   (advance-to-active, holds preserved). `bundle hold`/`unhold` survive as
   dependency management. Invariant preserved: changed remote content re-hashes
   to `pending` and is withheld by the content-hash trust gate at every
   exposure choke (content, MCP/hook exec, prompt export) until accepted via
   `ctxloom review`. Acceptance suite updated. NOTE: bundle posture
   (`bundle trust/untrust`) and the first-run baseline were retired in slices
   1–2, not here.
4. **Docs + messaging. — DONE.** Source help text and the MCP server
   instructions were retargeted at `ctxloom review`, and generated CLI docs +
   man pages regenerate (dropping the deleted commands/flag). The conceptual
   prose is now rewritten to the three-state model: `docs/trust-model.md`
   (full rewrite — three states, the five-step decision function, first-party
   sources, the single `ctxloom review` ceremony, content-hash gating, updated
   storage/chokes/lifecycle) and the website `concepts/review-and-trust.md`
   (full rewrite). `docs/adr/0033` keeps its historical decision text and gains
   a "partially superseded by trust-simplify (Slice 3, commit 192d4ef)" note
   retiring only the lockfile-as-review-surface half. The startup pending-count
   line needed no fold: `warnPendingTally`
   (`internal/operations/trust_gate.go`) already emits
   "N item(s) awaiting review — run 'ctxloom review'" with no reference to the
   removed pending-lockfile/`bundle approve` flow. Release-notes entry skipped:
   the repo keeps no CHANGELOG/release-notes file. Remaining stale references
   are OUT of this docs slice's scope (code help text + adjacent doc pages) —
   see the deferred list at the bottom of this section.

   Deferred (stale references to removed commands/flow found outside the three
   rewritten docs, not fixed here):
   - `internal/cli/remote.go` `remote trust` Long help still says untrusted
     "staged bundle changes likewise stay pending until approved" — stale
     (staging + `bundle approve` are gone); feeds generated CLI docs, so a fix
     needs `just gen-docs`.
   - `website/src/content/docs/reference/environment.md` still documents
     `CTXLOOM_AUTO_APPROVE_BUNDLES` (removed from code).
   - `website/src/content/docs/guides/mcp-server.md` references
     `ctxloom bundle trust <name>` (removed command).
   - `website/src/content/docs/concepts/remotes.md` says untrusted-remote
     changes are "staged for review" (lock-staging is gone; content is per-item
     pending).
   - `internal/cli/bundle_list.go` / `internal/cli/trust_interactive.go`:
     `bundle show -i` help says "offer to mark the bundle trusted"; the
     interactive flow now shows per-item effective trust + per-hook
     trust/blacklist (no bundle posture) — help text is stale.
   - `docs/adr/0024-minimize-mcp-surface.md` names the removed
     `bundle review/approve/decline/show-pending` commands — historical ADR,
     leave as-is (a superseding note could be added if desired).

## Interactions

- **fail-loudly (fiery-slick)**: pending items are *not* failures — the strict
  default reports them prominently but starts; a corrupt trust store is fatal.
- **ref-grammar (mere-puppy)**: its posture-key canonicalization slice is
  mooted (postures deleted); item refs in `trust.yaml` use the canonical
  grammar that work standardizes.
- **docs generation (soapy-chess)**: the `ctxloom review` command text becomes
  part of the generated reference automatically.
- Supersedes icky-drum items 3 (form-flip) and 4 (hook identity is still
  positional — consider content-derived IDs inside slice 1 while the schema is
  open); items 1–2 (stale comments, `remote trust` help) are addressed by the
  code this plan touches anyway.

## Resolved decisions (2026-07-02)

- First-party exemption includes ctxloom-default and personal remotes.
- `trust`/`blacklist` survive as plumbing under the review porcelain.
- No grandfathering beyond hash-matching grant translation.
- Rejection beats trusted sources and builtins.

## Open items

- Slice-1 call: keep `trust_bundles` flag as sources membership vs a
  `sources:` list in trust.yaml (recommendation: keep flag).
- Hook identity: adopt content-derived IDs during the schema change or defer.
- Non-interactive/CI story beyond committed `trust.yaml` (env override to
  treat pending as fatal vs withheld — defaults to withheld).
