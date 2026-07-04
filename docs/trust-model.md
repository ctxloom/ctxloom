# ctxloom Trust Model

The canonical reference for how ctxloom decides what content reaches an agent.
Everything here is derived from the enforcement code; where behavior and older
doc-comments disagree, this document describes the behavior (the stale comments
are listed under Known gaps).

## The two layers

ctxloom enforces trust in two independent layers. Conflating them is the most
common way to misunderstand the system:

- **Layer A — lockfile review** (`lock.yaml` vs `lock.pending.yaml`): controls
  *which SHA of a bundle is resolvable at all*. Governed by remote trust and the
  human review flow (`bundle review` / `approve` / `decline` / `hold`).
- **Layer B — per-item exposure** (`trust.yaml` + `remotes.yaml`): controls
  *whether an individual fragment, skill, MCP server, or hook is exposed to the
  agent*, keyed by content hash. Governed by the trust cascade
  (`operations.EffectiveTrust`).

**Approving a bundle (Layer A) does not trust its items (Layer B).** An
approved bundle from an untrusted remote still default-denies at the item
cascade until covered by a grant, bundle posture, remote trust, or the
baseline. `CTXLOOM_AUTO_APPROVE_BUNDLES=1` likewise only merges the lockfile;
it exposes nothing.

## Mechanisms

### Per-item grant — `ctxloom trust <ref>`

Blesses one item (fragment, skill, MCP server, hook) at its current content.
The grant binds to the **effective-content hash** — the distilled or raw form,
per `config.use_distilled` — recomputed from resolved content at every
exposure; the author-supplied `content_hash` field is never trusted. Any
content change drops the grant and the item re-gates. An `@commit` suffix on
the ref is recorded as provenance (`sha_at_grant`), not compared.

### Blacklist — `ctxloom blacklist <ref>`

Withholds an item everywhere, always. Writes two companion entries: a **sticky
ref-level block** (survives content changes and version bumps) and the item's
current **content hash on the denylist** (repo- and ref-agnostic, so a renamed
or moved identical copy stays blocked). The denylist entry is best-effort (only
when the item resolves); the ref block is always written. Deny always beats
every allow.

### Bundle posture — `ctxloom bundle trust|untrust <name>`

A SHA-agnostic posture toward a bundle as a source. Cascades to every item in
the bundle that has no explicit grant or blacklist. Not pinned to any content
hash: it is "I trust whatever this bundle ships," revocable with `untrust`. An
unknown posture value fails closed to deny.

### Remote trust — `ctxloom remote trust|untrust <name>`

The **widest single trust primitive**. `trust_bundles: true` on a remote does
double duty:

- **Layer A**: the remote's bundle SHA changes apply without review
  (`remote upgrade` writes them straight to the active lockfile; they never
  appear in `bundle review`).
- **Layer B**: every item the remote ships — text *and* executables — is
  auto-allowed at the cascade's remote tier.

`init` marks personal repos added via `--remote` as trusted, and the bundled
`ctxloom-default` remote ships trusted by default. Trust a remote only when you
would run anything it publishes.

### Local auto-trust (honest source keying)

Items **authored in this project** (`ctxloom:local` source) are auto-allowed —
all kinds, including MCP servers and hooks. The rule is "you wrote it here,
you trust it; a *clone* of it is not yours": items are keyed by their true
source ref (a seeded/cloned bundle stamps its canonical remote ref; only a
genuine project-local bundle keys as local), so copying a remote bundle into
the cache does not manufacture local trust. The same keying gates cloned
**text** like executables — a fragment is instructions to an LLM, and treated
accordingly.

### Baseline (one-time migration snapshot)

When per-item enforcement first rolled out (TR6), a one-shot **baseline** minted
grants for every then-resolvable remote item so existing setups didn't lose
content. It runs exactly once per store (marker-guarded in `trust.yaml`),
excludes builtins and local items, and never repeats: content that changes
after the baseline re-gates normally. Consequence worth knowing: the first
ctxloom run on a pre-existing project blesses the remote content present at
that moment, sight-unseen.

### Blind mode

Non-interactive pulls (startup sync, automation) run **blind**: the security
review display and confirmation prompt are skipped, with a stderr notice.
Compensating control: a *first install from an untrusted remote* is staged into
the pending lockfile instead of activating (`StageUntrustedNew`), so
never-reviewed hooks/MCP/context stay inert until a human approves.

### Environment overrides

`CTXLOOM_AUTO_APPROVE_BUNDLES=1` — CI/cron escape hatch that auto-merges
pending lockfile changes at MCP startup (Layer A only). No environment variable
bypasses the per-item cascade.

## Storage

| File | Contents |
|------|----------|
| `.ctxloom/trust.yaml` | `grants[]` `{repo_url, ref, content_hash, form?, sha_at_grant?}`; `blacklist[]` `{repo_url, ref}`; `denylist[]` (bare content hashes); `bundles[]` `{bundle, decision}`; baseline marker |
| `.ctxloom/remotes.yaml` | remotes with `trust_bundles` flags (and custom forges) |
| `.ctxloom/lock.yaml` | active pins: `bundles: map[canonicalRef]{sha, url, requested_version, kind, pinned, ...}` |
| `.ctxloom/lock.pending.yaml` | staged pins awaiting review |

## Enforcement points

The cascade verdict is enforced at distinct chokes. A DENY is **fail-closed and
silent to the agent**: the item is simply absent, and the human gets one
aggregate, content-free stderr advisory ("N item(s) withheld — review with
...`"). Nothing about withheld content is ever injected into agent context.

| Choke | Covers | On deny |
|-------|--------|---------|
| Content loader gate (`gateContent`) | fragments, skills (text) | absent from assembled context |
| MCP exec choke (`extractMCPFromBundle`) | bundle MCP servers | omitted from backend settings |
| Hook exec choke (`extractHooksFromBundle`) | bundle hooks | omitted from backend settings |
| Command-file export (`LoadSkillExports`) | skill slash-commands | not exported |
| Tooling collection (`CollectTooling`) | `tooling` declarations | withheld from Containerfile proposals |
| Listing stamp (`TrustStamper`) | JSON listings | stamped `trusted: false` + source |

The gate hashes the exact pre-substitution bytes, so profile-variable
templating cannot smuggle content past it.

**Ungated by design:**

- **Profiles** — a profile definition is orchestration, never gated; its
  constituent items still gate at their own chokes. Profile changes ride
  Layer A review.
- **Builtin bundles** — shipped inside the binary; trusting the binary trusts
  them. Every builtin resolver bypasses the gate, and the baseline excludes
  them.

## Precedence

One resolver (`operations.EffectiveTrust`), first match wins, fail-closed:

1. **denylist** — content hash denied → DENY
2. **blacklist** — sticky ref block → DENY
3. **explicit grant** — `{repo, ref, hash}` grant → ALLOW
4. **bundle posture** — trusted/untrusted → its decision
5. **local** — project-authored → ALLOW (all kinds)
6. **remote trust** — trusted remote → ALLOW
7. **default** — DENY

An unreadable trust store or remote registry denies everything. Repo URLs are
canonicalized (scheme, `.git`, `git@`, host case; path case only on case-folding
forges) on both sides of every comparison, so a URL-spelling variant cannot
escape a blacklist or manufacture a match.

## Lifecycle

- **Steady-state sync** installs exactly the pinned set; it stages nothing.
  SHA movement is `remote upgrade`'s job.
- **Interactive pull**: fetch → security review (boxed warning + full content,
  paged) → explicit `[y/N]` (default No) → lock.
- **Startup/auto sync**: blind pull; new untrusted installs staged inert;
  loud stderr advisory with the staged count.
- **`remote upgrade`**: re-resolves within constraints. Trusted-remote changes
  apply directly; untrusted changes stage in `lock.pending.yaml`; held
  (`pinned`) entries never advance; a hash conflict aborts with nothing
  applied.
- **Review**: `bundle review` (lists staged NEW/MODIFIED with an injection
  warning) → `bundle show-pending <name>` (raw YAML + structural diff; a hook
  command swap is flagged) → `approve` (merge all pending → active, re-apply
  hooks) or `decline [name]` or `hold <name>`.
- **Grant invalidation**: the gate re-hashes effective bytes at every exposure;
  any content edit re-gates the item. Trust mutations (`trust`, `blacklist`,
  `bundle trust`) immediately re-apply managed artifacts so a withheld MCP/hook
  is scrubbed without waiting for the next run.

## Identity

Items key as `{canonical repo URL} + {bundle}#{kind}/{name}` with no version;
hashes carry the version dimension. A moved or renamed item keeps neither its
grant (new ref → re-gates, safe) nor its sticky ref-block (the content-hash
denylist compensates, when the content form matches). Hook identity is
positional — `{event}/{index}` — so reordering a bundle's hooks shifts later
hooks' identities (grants re-gate: safe, but see Known gaps).

## Threat model

Addressed:

- **Malicious bundle update** — SHA pins + staged review; structural diff
  highlights hook/MCP command swaps.
- **Prompt injection via shared text** — cloned fragments/skills gate exactly
  like executables; pre-substitution hashing.
- **Arbitrary execution via MCP/hooks** — per-item gating at the exec chokes,
  hashed over the full executable surface (command, args, env, installation /
  matcher, type, command).
- **URL-variant/typosquat escape of a blacklist** — canonical repo URLs on both
  comparison sides.
- **curl-pipe-sh via tooling declarations** — tooling collection is
  trust-gated; nothing is applied without per-change human approval.
- **Silent activation of never-reviewed content** — blind pulls stage untrusted
  first installs inert.

## Known gaps and accepted risks

1. **Review ≠ trust is a UX trap.** Approving a bundle exposes nothing by
   itself; users will approve and then wonder where their content is. The
   advisory points at the right commands, but the concept needs to be
   understood (hence this document).
2. **Content-form-specific hashes.** Grants and denylist entries hash the
   *effective* form (distilled vs raw per `config.use_distilled`). Flipping
   that setting, or adding/removing a distilled form, silently drops grants
   (safe: re-gate) and can let a moved copy escape the content denylist in the
   other form (the sticky ref block does not follow moves). Blacklisting in
   both forms would close this.
3. **Positional hook identity.** `{event}/{index}` keying means inserting or
   reordering hooks shifts identities; a sticky ref block can land on a
   different hook than the one blacklisted. The content denylist still catches
   identical content. Content-derived hook IDs would be more robust.
4. **Trusted remotes are very broad.** One flag grants unreviewed SHA advance
   *and* blanket exposure of text + executables. `ctxloom-default` ships with
   it on. Splitting the two halves (auto-apply vs auto-expose) is a possible
   future refinement.
5. **First-run baseline blesses unseen content.** The one-time baseline grants
   whatever remote content is resolvable at first run — the boundary is "state
   at first run," not "reviewed."
6. **`$PAGER` during review** is user-controlled code execution at review time;
   acknowledged in code as an accepted, OS-conventional risk.
7. **Stale doc-comments** (code fixes queued): several comments still claim
   local MCP/hooks are "never auto-trusted" — the cascade's local tier allows
   all local kinds; and the `remote trust` help says pending changes "are
   approved" — it only flips the flag and filters review; pending entries
   linger un-applied until the next upgrade/approve.
