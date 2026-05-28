# 0008 — Skip task-status customization via env

**Date:** 2026-05-27.

## Status

Deferred.

## Context

flesler/mcp-tasks (the original tasks implementation we replaced) exposed configurable statuses via env vars: `STATUS_WIP`, `STATUSES`, `KEEP_DELETED`, etc. ctxloom's native task layer hardcodes four statuses: "In Progress", "To Do", "Done", "Archived".

The LLM-facing prompt that summarizes tasks for `assemble_context` is tuned to those four labels. Configurable statuses would either fragment the prompt (each user's labels mean different things to the model) or require runtime prompt synthesis (more moving parts).

## Decision

Don't expose status customization. Keep the four hardcoded statuses.

## Consequences

Teams with a workflow that needs different states (e.g., "Blocked", "In Review") can't model them as first-class statuses — they have to work around it with tags or task content.

**Revive trigger:** a user has a workflow that genuinely needs different statuses — and we judge the prompt-fragmentation cost worth it.
