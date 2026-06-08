# 0006 — Skip `task_link_session` tool

**Date:** 2026-05-27.

## Status

Deferred.

## Context

The ctxloom-tasks plan proposed a `task_link_session <harp-id> <harp-session-name>` MCP tool for explicit task ↔ session association. After plan-stamping shipped (the PostFileEdit hook that records the active harp on `plans.md` changes), the link is created implicitly: a task created during session X has X's harp stamped on it via the bundle hook.

## Decision

Don't implement explicit `task_link_session`. Plan-stamping covers the implicit link sufficiently.

## Consequences

Tasks created outside the plan-stamping path (e.g., direct CLI `tasks add` with no `plans.md` edit) carry no harp linkage. The current task UI doesn't surface that linkage anyway, so the gap is invisible today.

**Revive trigger:** a user wants to link a task to a session *other* than the one stamping happened in — e.g., revisiting an old session's task from a fresh session.
