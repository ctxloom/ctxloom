---
distilled_by: claude-3-opus
---
# no-provenance-comments

# No Provenance in Comments

Comments state invariants. Git records timeline.

## The Rule

No git-provenance metadata in code comments or codebase prose:

- Commit hashes (`see commit abc123`)
- Milestone tags (`wired in P0.2`)
- PR numbers (`added in #1234`)
- Release versions as historical markers (`introduced in 1.4.0`)
- Author/date stamps (`added by Alice 2024-01-15`)
- TODO sprint/iteration references

## Why

- Commit hashes break after rebase/squash
- Milestone tags become noise post-project
- Author/date stamps age into tombstones surviving author departure and line rewrites
- `git blame`/`git log` already record this; duplication guarantees divergence

## What to Write Instead

State the load-bearing invariant. If the comment survives a history rewrite, keep it. Else it's a timeline entry in the wrong file.

Before:
```go
// reflection.Register: wired in P0.2 (commit abc123). Canonical.
```

After:
```go
// reflection.Register: canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service.
```

## Where Provenance Lives

- `git blame`/`git log`
- Commit message body, PR description
- `CHANGELOG.md`/release notes
- ADRs
- Issue tracker

## Exceptions (Not Provenance)

- External stable IDs: RFC numbers, CVE IDs, language-spec versions
- Public-API `@since` annotations: version is part of API contract
- "We tried X and it failed": invariant is the rejected approach; dated artifact lives in rationale