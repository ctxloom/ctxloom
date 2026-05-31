# 0010 — Skip flesler-style Reminders status

**Date:** 2026-05-27.

## Status

Deferred.

## Context

flesler/mcp-tasks has a "Reminders" status — tasks the LLM is told about on every interaction. The intent is to surface evergreen instructions ("always use semantic commit messages", "never push to main") that should ride along with every prompt.

ctxloom already surfaces in-progress tasks on every `assemble_context`. A separate Reminders status would duplicate that mechanism with a different label and a different prompt slot.

## Decision

Don't implement Reminders.

## Consequences

Users who want evergreen reminders have to encode them as in-progress tasks, prompt fragments, or bundle content. None is a perfect fit; the alternatives work.

**Revive trigger:** empirical observation that the LLM ignores in-progress tasks but would attend to a separate "Reminders" surface — i.e., the duplication has a real payoff in attention.
