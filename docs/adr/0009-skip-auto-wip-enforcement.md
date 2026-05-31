# 0009 — Skip AUTO_WIP enforcement

**Date:** 2026-05-27.

## Status

Deferred.

## Context

The original tasks plan included "AUTO_WIP": cap the number of simultaneously-in-progress tasks to a configured N, refusing to start a new one if the cap is hit. This patterned on Kanban WIP limits.

ctxloom's task surface today places no cap. User discipline (and the LLM's own judgment when it manages tasks) is the only constraint.

## Decision

Don't enforce a WIP cap.

## Consequences

Nothing prevents a user (or the LLM) from accumulating arbitrarily many in-progress tasks. The cost is visibility: a long in-progress list dilutes the LLM's `assemble_context` summary.

**Revive trigger:** a user reports they (or the LLM) routinely accumulate too many in-progress tasks and want hard enforcement.
