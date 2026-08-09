# 0033 — Version-constraint dependency model: manifest constrains, lock resolves, hold freezes

**Date:** 2026-06-08.

## Status

Accepted.

**Partially superseded** by the trust-simplify work (Slice 3, commit `192d4ef`).
The review-gated *staging* half of `upgrade` described below — untrusted bundle
changes staging to a pending lockfile for `bundle review`/`approve` — was
removed along with the `bundle review/approve/decline/show-pending` family.
`upgrade` now moves the **active** lock unconditionally (holds still frozen, hash
conflicts still abort); whether any changed content ever reaches the agent is
decided per item at exposure by the content-hash trust gate and reviewed with the
single `ctxloom review` porcelain (see [trust-model.md](../trust-model.md)). The
three-layer constraint / lock / hold model, the "lock is the sole home of the
resolved SHA" invariant, and everything else in this ADR still stand — only the
lockfile's role as a *security* review surface was retired.

Updates [0028](0028-reference-pinned-discovery-head.md) (referenced content is now read at the **lock's** resolved SHA, derived from a constraint, rather than a SHA carried in the manifest ref). Builds on [0032](0032-single-canonical-reference-no-short-form.md) (the `@version` slot of the canonical reference now holds a *constraint*, not just a tag/SHA), [0027](0027-mirror-established-tool-naming.md) (apt `update`/`upgrade` naming), and the `Pinned`-flag hold from the already-superseded [0003](0003-skip-contentversion-pinning.md).

## Context

"Locks" and "pins" had drifted into three tangled mechanisms with two storage homes:

1. A resolved commit SHA baked into the **profile ref** (`…@bundles/x@<sha>`), rewritten by `remote upgrade` on every bump.
2. The same SHA recorded in the **lockfile** (`LockEntry.SHA`).
3. A `Pinned` bool on the lock entry (the hold).

The SHA therefore lived in two places that had to be kept in sync, and `upgrade` mutated user-authored profile YAML. Reasoning about "what version is this, and will it move?" required reconciling all three. A "starting-point ref that the lock overrides" model was prototyped and rejected — it produced manifest/lock divergence, a YAML that lied about the installed version, vestigial SHAs, and weakened conflict detection.

Every mature dependency manager (apt, npm, uv, Cargo, Bundler, Nix flakes) instead separates three concerns and never lets the third rewrite the first.

## Decision

Adopt the **familiar package-manager model**, three layers with one invariant:

- **Constraint** (manifest / profile ref) — human-authored intent in the `@version` slot: a semver range (`@^1.2`), a branch/channel (`@main`), an exact tag or SHA (`@v1.2.3`, `@<sha>`), or empty (track the default branch).
- **Resolution** (lockfile) — the exact commit that satisfies the constraint, plus the tag it chose. The lock is the *sole* home of the resolved SHA.
- **Hold** (policy) — the `Pinned` flag on the lock entry: "don't let `upgrade` advance this," without editing the manifest.

**Invariant:** the lock always satisfies the manifest; it never contradicts or overrides it.

Mechanics:

- **Version space** = the source repo's git tags, ordered by semver (`Masterminds/semver/v3`); branches are channels; an exact tag/SHA is the tightest constraint. Repos that don't tag still work via branch/exact/default constraints.
- **Resolution precedence** (`internal/operations/constraint_resolve.go`): held entry → carried forward; unchanged constraint in lock mode → carried forward (stability); a bare commit → recorded verbatim with no clone; otherwise resolve the constraint against the repo; on failure, fall back to the last locked SHA, else skip with a warning (never an empty pin).
- **`sync`** installs exactly what the lock pins. **`lock`** resolves constraints (carry-forward stable). **`update`** reports the newest commit *within each constraint*. **`upgrade`** re-resolves within constraints and moves the **lock only** — trusted remotes apply to the active lock, untrusted stage to pending for `bundle review`/`approve`. **`bundle hold`/`unhold`** toggle the hold.
- The manifest ref is **never** rewritten by `upgrade`/`approve`/`install` — only by an explicit author edit or `profile update --add-bundle x@^2`.

## Consequences

- **No migration.** An existing `@<sha>` ref is simply the tightest constraint (an exact pin); it resolves to itself, recorded verbatim, no clone. Decision is back-compatible by construction.
- **The YAML never lies.** A loose ref looks loose; an exact ref is exact. `update`/`upgrade` change only the lock, so profile diffs stay clean.
- **Transitive freeze falls out for free.** A held parent profile resolves to its old commit; `ResolveShortRefs` makes its short child refs inherit that commit as an exact pin — so holding a profile freezes its whole subtree.
- **One source of truth** for the resolved SHA (the lock) eliminates the ref-vs-lock drift bug class that motivated this work.
- **Hold inherits 0003's gitignore caveat.** The hold lives on the (typically gitignored) lock entry, so it does not travel across `git clone`. Same revive trigger as 0003: if a user wants a hold to survive clone, it must move into committed config.
- `bundle pin`/`unpin` were kept as aliases of `bundle hold`/`unhold` so the rename was non-breaking; **superseded** — the aliases were deleted outright (no-backward-compat-shims policy) because they re-taught the exact confusion the `hold` rename exists to remove. MCP tool names `pin_bundle`/`unpin_bundle` are a separate surface and are unchanged.
- Naming: "hold" (not "pin") for the freeze, so it never collides with the idea of an exact version pin in the manifest.
