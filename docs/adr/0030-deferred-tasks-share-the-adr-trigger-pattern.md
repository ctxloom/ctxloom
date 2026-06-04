# 0030 — Deferred tasks reuse the ADR revive-trigger pattern

**Date:** 2026-06-03.

## Status

Accepted.

## Context

ADRs already have a "revive trigger" convention (see `docs/adr/README.md`): a
Deferred ADR records a named, concrete condition that flips it back to Accepted
when the condition fires. Triggers are free text and a human (or the LLM) judges
whether one has fired — there is no machine evaluation.

The task store had no equivalent. Statuses were a flat four-value set
(`To Do` / `In Progress` / `Done` / `Archived`, ADR [0025](0025-per-project-task-log.md)),
so a task that should wait on a future condition had to either sit in the active
list as noise or be lost to `Archived`. The same shape the ADRs already use for
exactly this problem was sitting one subsystem away, unshared.

## Decision

**Tasks gain a `Deferred` status with a required `trigger`, modeled on the ADR
revive trigger.**

- A new `StatusDeferred` sits between `To Do` and `Done` in the canonical order.
- `Task` carries a free-text `Trigger`. The invariant `a Deferred task must have
  a non-empty trigger` is enforced once, in the tasks store
  (`ValidateStatusTrigger`), so both store backends (markdown and the JSONL
  log) and both frontends (CLI and MCP) share the rule.
- The trigger is preserved across status changes: leaving `Deferred` never drops
  it, so a task can cycle `Deferred → To Do → Deferred` without re-typing the
  condition; a new trigger overrides the stored one.
- Deferred tasks are hidden from the default active list (like `Done`/`Archived`),
  surfaced only by an explicit `--status Deferred` filter, `--all`, or the skill.
- Evaluation is the same as for ADRs: **the LLM judges**, the human confirms. The
  built-in `check-triggers` skill lists Deferred tasks, decides which triggers
  have fired with reasoning, and asks before moving any back to `To Do`. There is
  no automatic transition.

## Consequences

- Tasks and ADRs now express "wait on a named condition" the same way; someone
  who understands one understands the other.
- The trigger stays declarative free text. We accept that there is no machine
  check — that is the point, and it matches the ADR precedent.
- The pending half (defer with a trigger) and the review half (the skill) are
  separate, mirroring how an ADR is deferred in one place and revived in another.

Revive trigger for this decision: if user-defined statuses or multiple
named triggers per task become a real need, revisit whether `Trigger` should
generalize to a typed/structured condition rather than a single free-text field.
