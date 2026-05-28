# 0007 — Skip cross-project task search

**Date:** 2026-05-27.

## Status

Deferred.

## Context

Tasks live as per-project files under `.ctxloom/tasks/`. `ctxloom tasks list` only sees the current project. The original tasks plan proposed a `~/.ctxloom/tasks-index.yaml` aggregating per-project task files, and a `--all-projects` flag on `tasks list`.

The aggregation index requires write coordination across project boundaries (every task mutation needs to update the global index), and surfacing cross-project results requires UI/CLI design we haven't done. No user has asked.

## Decision

Don't implement cross-project task search.

## Consequences

`tasks list` continues to operate strictly within the current project. Users with tasks across multiple projects have to switch directories to see them.

**Revive trigger:** a user wants `ctxloom tasks list --all-projects` (or any cross-project task surface).
