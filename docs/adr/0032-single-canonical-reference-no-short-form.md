# 0032 — One canonical, scheme-qualified reference; no short `repo/path` form

**Date:** 2026-06-07.

## Status

Accepted. Implementation phased (see
`/home/babbitt/.ctxloom/sessions/brief-fond-flap/eliminate-short-form.plan.md`;
ctxloom task `warm-grant`). Phase 1 (selector parse) landed; remaining phases pending.

## Context

Content (bundles, profiles) has carried **two identities** for the same item:

- **Canonical**: a self-contained URL — `https://github.com/owner/repo@bundles/name[@ver]`
  (`remote.ParseReference`, `CanonicalString`). Used in profile `parents:` and
  ctxloom-default source `bundles:`.
- **Short**: `repo/path` produced by `NormalizeBundleName` / `Reference.ToLocalName`.
  Used as lockfile keys, seed keys, loader index keys, and cache dir names.

The dual identity is a recurring source of bugs and friction:

- **Ambiguity**: `sync.isRemoteReference` can't distinguish a local bundle `a/b` from a
  remote `repo/bundle` — both are scheme-less with a slash.
- **Inconsistent normalization**: canonical→short happens on some paths
  (`bundle_refs.go`, `pull.go`) but not others, so resolution depends on which path a
  ref arrived through.
- **Parser bug**: `expandBundleRef` split refs with `strings.IndexAny(ref, ":#")`, which
  fires on the `https:` scheme colon — so URL-form cherry-pick (`…#fragments/x`) was
  silently dropped and only the short form resolved.
- **Collisions**: two remotes whose repos share a name collide in short form.

Having two names for one thing means every layer must know both and convert between
them; the conversions drift and the ambiguity leaks.

## Decision

**There is ONE reference identity: a scheme-qualified canonical reference. The short
`repo/path` form is removed from the operational codebase entirely.**

- **One grammar**, remote and local alike:
  `SOURCE @ kind/name [#section/item] [@version]` — `kind` ∈ {bundles, profiles},
  `section` ∈ {fragments, prompts, mcp}. The tail (`@kind/name#section/item@version`) is
  parsed and formatted identically regardless of source.
- **The SOURCE (front) is the sole resolver-dispatch key.** It selects the fetcher:
  `https://` / `git@` / `file://` → remote fetcher; `ctxloom:local` → local fetcher.
- **DRY resolution.** The uniform tail (item lookup, `#section/item` cherry-pick,
  version pinning) lives once in a shared base resolver; each scheme implements only
  source-specific raw retrieval. No per-scheme duplication of selector/version logic.
- **Local content is first-class and scheme-qualified** via a `ctxloom:local` source,
  stored in a committed (not git-excluded) `.ctxloom/local/` dir, referenced as
  `ctxloom:local@bundles/foo#fragments/bar@<hash>`. There are no bare local names.
- **No back-compat / no dual naming.** Short refs are not accepted as input — a short
  ref is a hard error that names the canonical form to use. `NormalizeBundleName`,
  `Reference.ToLocalName`, and `parseSimpleReference` are removed, not deprecated.
  Lockfile/seed/loader index re-key to canonical (one-shot lockfile rewrite from each
  existing `LockEntry.URL`, preserving pinned SHAs).
- **Version policy** (source-aware): `@version` is optional to parse but always pinned
  when resolvable, for reproducibility. A missing version warns for remote (it floats
  to latest) but is a tolerated edge case for local (non-git-stored / transient /
  unhashable content).

## Consequences

- One identity per item: no `repo/path` collisions, no ambiguity, no canonical↔short
  conversion to drift. `isRemoteReference` becomes a scheme check.
- Resolution is polymorphic by source with a single shared tail — local-vs-remote
  differences are contained to a small fetcher surface (ADR
  [0026](0026-ports-and-adapters.md) ports-and-adapters spirit).
- **Breaking on-disk change** (no back-compat): existing lockfiles re-key on load;
  profile content (ctxloom-default included) migrates to canonical / `ctxloom:local`;
  short input errors loudly. The git-clone cache (`cache/repos/<host>/<owner>/<repo>`)
  is already URL-derived, so it needs no migration; gitignored extracted copies rebuild
  on `remote sync`.
- Removal must be **thorough**: no vestigial short parsing, dual-keying, helpers, or
  tests — `rg` for `NormalizeBundleName`/`ToLocalName`/`parseSimpleReference` and
  `repo/path` assumptions must come back clean in non-test code.
- Aligns with ADR [0027](0027-mirror-established-tool-naming.md): a single explicit,
  self-contained reference rather than a tool-specific shorthand.

**Revive trigger:** if a second identity for the same content reappears — a short
alias, a `normalize-to-X` helper, a lockfile/seed/cache keyed by anything but the
canonical ref — that is the signal the dual-identity problem is regrowing. Collapse it
back to the single canonical reference rather than teaching another layer to convert.
