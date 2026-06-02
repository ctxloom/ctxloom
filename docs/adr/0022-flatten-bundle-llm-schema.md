# 0022 — Flatten the bundle item LLM-export schema (`plugins.llm.*` → `llm.*`)

**Date:** 2026-06-01.

## Status

Accepted.

## Context

A fragment/prompt in a bundle can carry per-LLM export settings (how it surfaces
as a slash command in each backend). The schema nested them two deep:

```yaml
plugins:
  llm:
    claude-code: { description: ..., argument_hint: ..., allowed_tools: [...] }
    gemini:      { description: ... }
```

The outer `plugins` namespace had exactly one member, `llm` — a vestige of an
imagined "plugin types" axis that never grew a second member. Under the
plugin→llm rename, a literal key rename would produce the stutter `llms.llm`
(rejected for the config in ADR 0021 for the same reason).

## Decision

Flatten to a single `llm` key, dropping the empty `plugins` namespace:

```yaml
llm:
  claude-code: { description: ..., argument_hint: ..., allowed_tools: [...] }
  gemini:      { description: ... }
```

Go: `bundles.PluginsConfig` + `bundles.LMPluginConfig` collapse into one
`bundles.LLMExports{ ClaudeCode ClaudeCodeConfig; Gemini GeminiConfig }`;
`LoadedContent.Plugins` / bundle item `Plugins` → `LLM` (yaml `llm`).
`ClaudeCodeConfig` / `GeminiConfig` are unchanged (backend-specific, not
"plugin"). `resources/schema/fragment-schema.json` flattened to match.

This is a **breaking** bundle-schema change with **no migration shim** (per the
owner directive against dual names). Every bundle YAML carrying the old
`plugins.llm.*` block must be migrated — including the `ctxloom-default` remote
repo. An unmigrated `plugins:` block is silently ignored (yaml no longer maps
it), so the item simply loses its custom slash-command export settings and falls
back to defaults.

## Consequences

One less nesting level; the bundle item's LLM-export config reads as `llm.<backend>`,
consistent with the config's `llm` section (ADR 0021). The cost is migrating
existing bundles; the in-repo builtins carried no such block, so the live blast
radius is the `ctxloom-default` remote (migrated alongside) plus any third-party
bundles in the wild.

**Revive trigger:** if a genuinely non-LLM export axis appears (e.g. editor or
shell integrations needing their own per-tool block), reintroduce a namespacing
level deliberately — don't reflexively re-nest under a revived `plugins`.
