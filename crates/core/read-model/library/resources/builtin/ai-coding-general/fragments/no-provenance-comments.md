---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - comments
  - documentation
  - style
content_hash: sha256:2598d86fbbdc712913f84f88c968579dcc02d38ade0b9481e467608ace3c5533
---
# No Provenance in Comments

Comments state invariants. Git records timeline.

## The Rule

Do not put git-provenance metadata in code comments or in prose about the codebase. This includes:

- Commit hashes (`see commit abc123`)
- Milestone tags (`wired in P0.2`)
- PR numbers (`added in #1234`)
- Release versions used as historical markers (`introduced in 1.4.0`)
- Author and date stamps (`added by Alice 2024-01-15`)
- TODO references to sprints or iterations (`TODO from sprint 47`)

## Why

- Commit hashes break after rebase or squash
- Milestone tags become ambient noise once the project moves past them
- Author and date stamps age into tombstones that survive the author leaving and the line being rewritten
- Git already records all of this in `git blame` and `git log`; duplication guarantees divergence

## What to Write Instead

State the load-bearing invariant. If the comment would still be true after a git history rewrite, keep it. If not, you wrote a timeline entry in the wrong file.

Before:
```go
// reflection.Register: wired in P0.2 (commit abc123). Canonical.
```

After:
```go
// reflection.Register: canonical "what does this server speak"
// mechanism. Do not add a parallel descriptor service.
```

## Where Provenance Actually Lives

- `git blame` and `git log` on the line itself
- Commit message body and PR description
- `CHANGELOG.md` or release notes
- ADRs (architectural decisions log)
- Issue tracker history

## Exceptions (Not Provenance)

- External stable identifiers: RFC numbers, CVE IDs, language-spec versions. Stable outside your repo.
- Public-API `@since` annotations: the version is part of the API contract for consumers.
- "We tried X and it failed" comments: invariant content (the rejected approach); the dated artifact lives in the rationale, not the comment.
