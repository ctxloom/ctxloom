# Architecture Decision Records

This directory holds ADRs in the [Michael Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions). One decision per file. Numbered sequentially. These are project-owner decisions captured for future reference (and future-self), not team-consensus records — the format is the same, the bar for reopening is whatever the owner wants it to be.

## Format

Each ADR file has these sections, in this order:

```markdown
# NNNN — short title

## Status

One of: Proposed | Accepted | Deferred | Rejected | Superseded.

## Context

What problem or question we're deciding on. The forces in play.
What was true at the time of the decision.

## Decision

What we're doing (or not doing). Stated in the active voice.

## Consequences

What follows from this decision. For Deferred, include the named
**revive trigger** — the specific, concrete condition that would
flip the status to Accepted (or back to Proposed for re-discussion).
For Superseded, include **Superseded by:** with a link to the
replacement ADR.
```

## Status taxonomy

- **Proposed** — under discussion, no decision yet.
- **Accepted** — the decision is active and in effect. We did the thing (or adopted the convention) and it's part of how the codebase works.
- **Deferred** — a conscious "not now" with a named revive trigger. Not closed forever. When the trigger fires, the item moves back onto the plan. Most "speculative feature, skip until needed" decisions land here.
- **Rejected** — actively decided against. Revival requires re-litigating the decision; a passing thought ("maybe we should…") does not reopen it. Use this when the option has a real cost we've decided is too high, not just "we don't need it yet."
- **Superseded** — replaced by a later decision. The replacement's ADR number goes in **Superseded by:** so future readers can follow the chain.

The difference between Deferred and Rejected is the bar for reopening: Deferred reopens automatically on a concrete trigger; Rejected requires a new conversation that overturns the previous one.

## File naming

`NNNN-short-slug.md`, zero-padded, ascending. The number is permanent: even if an ADR is later Superseded, its file keeps its number and slug.

## Existing ADRs

| # | Title | Status |
|---|---|---|
| [0001](0001-skip-bundlereader-cache.md) | Skip in-memory `BundleReader` read cache | Deferred |
| [0002](0002-skip-ctxloom-gc.md) | Skip `ctxloom gc` command | Deferred |
| [0003](0003-skip-contentversion-pinning.md) | Skip `Reference.ContentVersion` pin promotion | Superseded |
| [0004](0004-skip-review-each-ux.md) | Skip "Review each one at a time" UX loop | Deferred |
| [0005](0005-skip-fetcher-fixture-tests.md) | Skip fetcher-fixture end-to-end content tests | Deferred |
| [0006](0006-skip-task-link-session.md) | Skip `task_link_session` tool | Deferred |
| [0007](0007-skip-cross-project-task-search.md) | Skip cross-project task search | Deferred |
| [0008](0008-skip-status-customization.md) | Skip task-status customization via env | Deferred |
| [0009](0009-skip-auto-wip-enforcement.md) | Skip AUTO_WIP enforcement | Deferred |
| [0010](0010-skip-reminders-section.md) | Skip flesler-style Reminders status | Deferred |
| [0011](0011-skip-transcript-symlink.md) | Skip transcript symlink in harp directory | Deferred |
| [0012](0012-skip-harp-go-extraction.md) | Skip `harp-go` extraction | Deferred |
| [0013](0013-keep-tasks-bundle-embedded.md) | Keep `ctxloom-default-tasks` embedded | Deferred |
| [0014](0014-remove-transcript-scan-binding.md) | Remove transcript-scan / marker session binding | Accepted |
| [0015](0015-local-git-test-remote.md) | Local bare-git `file://` remote for integration tests | Accepted |
| [0016](0016-accept-compression-parser-ccn.md) | Accept >10 cyclomatic complexity in compression parsers/classifiers | Accepted |
| [0017](0017-harp-self-id-marker.md) | Deterministic harp self-id marker for read-time recovery; defer LLM-echo fallback | Accepted/Deferred |
| [0018](0018-bundle-edit-keeps-add-only-semantics.md) | `bundle edit` keeps add-only semantics; guards in place vs routing through UpdateBundle | Superseded |
| [0019](0019-cli-pure-frontend.md) | The CLI (and every frontend) is a pure frontend over internal/operations | Accepted |
| [0020](0020-operations-llm-boundary.md) | The operations/LLM boundary: Distiller interface + backends polymorphic package | Accepted |
