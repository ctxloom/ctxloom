# ctxloom Trust Model

The canonical reference for how ctxloom decides what content reaches an agent.
Everything here is derived from the enforcement code; where behavior and older
doc-comments disagree, this document describes the behavior (open discrepancies
are listed under Known gaps).

## The invariant

**A human sees third-party content — including every update to it — before the
LLM does.** First-party content is exempt: material you authored in this project
(`ctxloom:local`), builtin bundles shipped inside the binary, and content from a
**trusted source** (`ctxloom-default` and your own personal/team remotes). Every
other remote item is born **pending** and is withheld from the agent until a
human reviews it.

There is one trust layer, not two. The lockfile is pure dependency pinning —
which commit of a bundle is installed — and grants no exposure. Whether an
individual item ever reaches the agent is decided per item, at exposure, by its
content hash.

## Item states

Every remote item — fragment, skill, MCP server, hook — is in exactly one of
three states:

- **pending** — never reviewed, or its content changed since a human accepted
  it. Withheld from the agent. Pending is the implicit state of any item with no
  entry in the store; it is never written to disk.
- **accepted** — a human reviewed this exact content. The acceptance binds to
  the item's `(raw, distilled)` content-hash pair as it stood at review; a
  change to either exposed form drops the acceptance and returns the item to
  pending.
- **rejected** — a human declined it. Withheld permanently, recorded as both a
  ref-level block and a content-hash denylist entry so a renamed or moved
  identical copy stays rejected. Rejection beats every allow, including the
  first-party exemption.

## The decision function

One resolver, `operations.EffectiveTrust`, owns every exposure decision. It
evaluates the item's current content hash against the store and the
trusted-sources set. First match wins; it is fail-closed:

1. **rejected** — the ref's recorded rejected state, or the item's content hash
   on the repo/ref-agnostic denylist → **DENY**.
2. **local** — the item was authored in this project (`ctxloom:local`), any kind
   including MCP servers and hooks → **ALLOW**.
3. **builtin** — the item is shipped inside the binary itself
   (`resources/builtin_bundles`) → **ALLOW**.
4. **trusted source** — the item's repo is in the trusted-sources set → **ALLOW**
   (updates included).
5. **accepted** — the item is accepted *and* the recorded hash for its current
   effective form matches the recomputed content hash → **ALLOW**.
6. **otherwise** — pending: **DENY**, withheld until reviewed, counted toward the
   startup notice.

Rejection is checked first so it beats every exemption: a user can reject an
item even from a trusted source or a **builtin**. This is enforced, not just
documented — builtin bundles are routed through the SAME decision function as
everything else (`trust.Ref{IsBuiltin: true}`, keyed under the synthetic
identity `builtin:ctxloom` so a builtin item can never collide with a
project-local bundle of the same name), and step 1's rejection check runs
before step 3's builtin exemption. An empty hash slot (a lazily-migrated v1
acceptance recorded only one form) or a form mismatch does not satisfy step 5 —
the exact materialization being exposed was never reviewed, so it stays
pending. An unreadable trust store or remote registry **denies everything**
(fail closed) and, in strict mode (the fail-loudly workstream), is a fatal
startup finding rather than a silent degrade to deny-all.

`builtin:ctxloom` is a plain identity string, not a cryptographic signature —
nothing about a builtin bundle is verified beyond "it shipped inside this
binary" (trusting the binary trusts what it ships, same as always). It exists
purely so builtin items are addressable and rejectable through the same
identity shape the store already uses for local (`ctxloom:local`) and remote
(canonical repo URL) items.

## First-party sources

Three source classes are exempt from review by default (but not from
rejection — see the decision function above):

- **Local** — items authored in this project, keyed to the `ctxloom:local`
  source. Locality is honest: a seeded or cloned bundle stamps its canonical
  remote ref, so a *copy* of remote content keys as remote and is **not**
  local-trusted. "You wrote it here, you trust it; a clone of it is not yours."
- **Builtin** — bundles compiled into the binary, keyed to the synthetic
  `builtin:ctxloom` identity. Allowed by default (step 3) with no review
  friction — but, unlike local content's step-2 placement, this is a distinct
  step specifically so a rejection (step 1) can still reach it.
- **Trusted sources** — remotes whose content is exempt, updates included.

### Trusted sources

The trusted-sources set is the remotes carrying `trust_bundles: true` in
`remotes.yaml`. Membership means: everything this source publishes — text *and*
executables, now and in every future update — reaches the agent without per-item
review.

- `ctxloom-default` ships trusted.
- `init --remote` adds your personal repos to the set.
- `ctxloom remote trust <name>` adds a remote (behind a confirmation that names
  the consequence); `ctxloom remote untrust <name>` removes it.

Third-party remotes default untrusted; their content is born pending. Trust a
source only when you would run anything it publishes. Trusting a source does not
touch per-item states: an item you rejected stays rejected even after its source
becomes trusted.

## The review ceremony

`ctxloom review` is the single porcelain. It walks every pending item, grouped by
bundle, and records a decision each:

- **New** items show their full content. **Updated** items (a ref you previously
  accepted whose content has since changed) show a unified diff against the
  snapshot of the accepted version — falling back to full content when no
  snapshot exists (e.g. a migrated v1 acceptance).
- MCP servers and hooks display as **what they run** — command, args, env,
  matcher, install — the exact executable surface the acceptance hash covers.
- Per item: **[a]ccept**, **[r]eject**, **[s]kip**; per bundle: **[A]** accept
  all remaining. Accepting records the `(raw, distilled)` hash pair and snapshots
  the accepted bytes; rejecting records the ref block plus the content-hash
  denylist entry. Viewing never mutates — only an explicit letter acts.
- Off a TTY, or with `--list`, it prints the pending table (bundle, ref, kind,
  new|update) and exits, so scripts and agents can see what a human still owes a
  look.
- `init`'s interview ends with a review session when anything is pending.

`ctxloom trust <ref>` and `ctxloom blacklist <ref>` are the scriptable plumbing
beneath the porcelain — they write the same accepted / rejected states through
the same mutation path, so the porcelain and the plumbing produce identical
on-disk results.

## Content-hash gating

An acceptance binds to the item's `(raw, distilled)` content-hash pair, always
recomputed from resolved content at acceptance — the author-supplied
`content_hash` field is never trusted. At every exposure the gate re-hashes the
exact pre-substitution bytes it is about to expose and compares against the
recorded hash for the current effective form (distilled vs raw, per
`config.use_distilled`). Any edit to the exposed form drops the match and
re-gates the item to pending. Profile-variable templating cannot smuggle content
past the gate, because the hash covers the bytes before substitution.

## Storage

| File | Contents |
|------|----------|
| `.ctxloom/trust.yaml` | `version: 2`; `items[]` `{repo_url, ref, state: accepted\|rejected, raw_hash?, distilled_hash?, reviewed_at}`; `denylist[]` (bare content hashes of rejected items). Pending items have no entry. |
| `.ctxloom/remotes.yaml` | remotes with `trust_bundles` flags (trusted-sources membership) and custom forges |
| `.ctxloom/lock.yaml` | dependency pins only: `map[canonicalRef]{sha, url, requested_version, kind, pinned, ...}` |
| `cache/trust/objects/` | content-addressed snapshots of accepted bytes, keyed by hash — the diff base for update review. Pure cache: deleting it only degrades update review to a full-content display. |

`trust.yaml` is committable: a team or CI inherits review decisions a human
already made. A version-1 store (grants / blacklist / bundle postures / baseline
marker) migrates in memory on load — grants become accepted items, blacklist
entries become rejected, and postures and the baseline are dropped (the content
they covered lands pending) — and persists in v2 form on the first mutation.

## Enforcement points

The decision is enforced at distinct chokes. A DENY is **fail-closed and silent
to the agent**: the item is simply absent. The human gets one aggregate,
content-free stderr advisory — `N item(s) awaiting review — run 'ctxloom
review'` — and nothing about withheld content is ever injected into agent
context.

| Choke | Covers | On deny |
|-------|--------|---------|
| Content gate | fragments, skills (text) — including builtin fragments | absent from assembled context |
| Executable gate — MCP | bundle MCP servers — including builtin servers | omitted from backend settings |
| Executable gate — hooks | bundle hooks — including builtin hooks | omitted from backend settings |
| Executable gate — command export | skill slash-commands | not exported |
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
match. A moved or renamed item keeps neither its accepted state (new ref →
re-gates to pending, safe) nor its ref-level rejection — the content-hash
denylist compensates when the content form matches. Hook identity is positional
(`{event}/{index}`), so reordering a bundle's hooks shifts later hooks'
identities (acceptances re-gate: safe, but see Known gaps).

## Threat model

Addressed:

- **Malicious bundle update** — changed remote content re-hashes to pending and
  is withheld; review shows the diff against what was accepted before it is
  exposed.
- **Prompt injection via shared text** — cloned fragments and skills gate exactly
  like executables (a fragment is instructions to an LLM); pre-substitution
  hashing.
- **Arbitrary execution via MCP/hooks** — per-item gating at the exec chokes,
  hashed over the full executable surface (command, args, env, matcher, type).
- **URL-variant / typosquat escape of a rejection** — canonical repo URLs on both
  comparison sides; the content-hash denylist is repo- and ref-agnostic.
- **curl-pipe-sh via tooling declarations** — tooling collection is trust-gated;
  nothing is applied without per-item human acceptance.
- **Content-form-flip escape** — closed by binding acceptance to the full
  `(raw, distilled)` hash pair.

## Known gaps and accepted risks

1. **Content-form-specific denylist.** A rejection denylists the raw and
   distilled hashes present at rejection. If a moved copy is later exposed in a
   *different* form than was denylisted (and its ref differs, so the sticky block
   does not apply), the copy can escape the content denylist in that form. The
   ref-level rejection still catches the same ref.
2. **Positional hook identity.** `{event}/{index}` keying means inserting or
   reordering hooks shifts identities; a sticky ref block can land on a different
   hook than the one rejected. The content denylist still catches identical
   content. Content-derived hook IDs would be more robust.
3. **Trusted sources are broad.** One flag grants unreviewed SHA advance *and*
   blanket exposure of text + executables, updates included. `ctxloom-default`
   ships with it on. Splitting the two halves (auto-apply vs auto-expose) is a
   possible future refinement.
4. **`$PAGER` during review** is user-controlled code execution at review time;
   acknowledged in code as an accepted, OS-conventional risk.
</content>
</invoke>
