# 0027 — Mirror established tools' naming when the concept is the same

**Date:** 2026-06-02.

## Status

Accepted.

## Context

ctxloom's remote/dependency lifecycle is a package manager in all but name: you
register a source, refresh it, install pinned dependencies, and advance those
pins to newer versions. Users arrive with mental models from tools that already
name these operations: apt, npm, cargo, git. When ctxloom invents its own verb
for a concept those tools already have a word for, the user pays a translation
cost for no benefit.

The `relock` command was a concrete instance. It re-pinned every reference at the
remote's current HEAD, which is exactly what apt calls `upgrade` (advance the
installed set to the latest available), paired with apt `update` (refresh the
index). ctxloom already had `remote update` for the refresh half, so the
dependency half was one well-known verb away from a familiar pair, but was named
after its mechanism (`relock`) instead.

## Decision

**When a ctxloom operation means the same thing as an operation in an
established, widely-known tool, use that tool's name for it.** Prefer the
familiar verb over a ctxloom-native coinage that the user has to learn.

apt is the touchstone for the remote/dependency lifecycle:

- `remote update` — refresh the local clones (apt `update`: refresh the index).
- `remote upgrade` — advance pinned dependencies to the remotes' current HEAD
  (apt `upgrade`: advance the installed set). Renamed from `relock` per this ADR.
- `remote lock` — record install-time SHAs (the pin step; no clean apt analogue,
  so it keeps a precise ctxloom name).

Two boundaries keep this from becoming cargo-culting:

1. **Only when the semantics genuinely match.** Borrow the verb because the
   concept lines up, not to decorate ctxloom with familiar words. Where there is
   no honest analogue (`lock`), a precise native term wins over a forced one.

2. **The borrowed name lives at the user-facing layer; the internal mechanism
   keeps its precise name.** `remote upgrade` is implemented by
   `operations.Relock` — "relock the lockfile" exactly names re-pinning, and the
   term "upgrade" is already taken internally for schema migration
   (`internal/upgrade`, `confirmUpgrade`). Renaming the operation would overload
   "upgrade" with two unrelated meanings in one package. A short comment on the
   command bridges verb to mechanism. The user sees the convention; the code keeps
   its precision.

This complements ADR [0019](0019-cli-pure-frontend.md): a single operation core
is reachable from every frontend, so aligning the *names* the user types is the
remaining surface to get right.

## Consequences

- Lower learning curve: a user who knows apt can guess `remote update` /
  `remote upgrade` without reading the help.
- A verb-vs-mechanism gap appears wherever the borrowed name differs from the
  internal symbol (e.g. `remote upgrade` → `operations.Relock`). This is paid for
  with a bridging comment, not a rename, to avoid term collisions.
- A risk of imperfect analogy: a borrowed verb can imply behavior the established
  tool has but ctxloom does not. Help text states the exact semantics so the
  analogy guides without misleading.

**Revive trigger:** if a borrowed term starts to mislead — the established tool's
semantics drift from ctxloom's, or the term collides with an existing ctxloom
concept the way "upgrade" already does internally — prefer a precise
ctxloom-native name over preserving the analogy. The goal is a shorter path to
understanding, not fidelity to another tool for its own sake.
