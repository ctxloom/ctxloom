# 0020 — The operations/LLM boundary: Distiller interface + backends polymorphic package

**Date:** 2026-06-01.

## Status

Accepted; amended 2026-09-19 (see "Amendment: per-engine exports are opaque
blocks" below — the third clause of the Decision no longer holds).

## Context

Task `bats-moody-rake` asked whether `internal/adapters/operations` delegates all LLM
specifics to the polymorphic, configured backend rather than hardcoding them.
The stated bar: no plugin names, model IDs, prompts, or backend-specific
behavior baked into operations; operations depends only on the provider-agnostic
seam.

An audit of every `.go` file under `internal/adapters/operations/` (2026-06-01) found:

- **Distillation** goes entirely through the injected `Distiller` interface
  (`bundles.go:54`). Every caller — `CreateBundle`, `UpdateBundle`,
  `DistillBundleFile`, the `items.go` CRUD, `DistillBundleItem` — receives the
  distiller via its request struct. Frontends construct it (`newLLMDistiller`
  from the configured/selected plugin) and inject it. Zero model IDs, zero
  prompt strings, zero backend-name string literals.
- **Hooks / context / command-files** (`hooks.go`) delegate to the
  `internal/lm/backends` package's *polymorphic* functions —
  `BackendsWithSettings()`, `AssembleManagedHooks()`, `WriteSettings()`,
  `WriteCommandFilesFor()`, `WriteContextFile()`. That package iterates over all
  registered backends; it IS the polymorphic seam, so calling it is correct
  delegation, not a leak.
- The only place operations names backends is `getBuiltinPrompts()`
  (`hooks.go:254-262`), which populates `bundles.ClaudeCodeConfig{}` and
  `bundles.GeminiConfig{}` with each built-in command's description.

The question this ADR settles is whether that last item is a leak to remediate.

It is not LLM behavior. `LLMExports` (`internal/core/bundles/loader_content.go`; was
`LMPluginConfig` before the plugin→llm rename / bundle-schema flatten in ADR 0022)
is a *typed, per-backend* config schema: `ClaudeCode` and `Gemini` are named
struct fields carrying **different** data (Claude has `ArgumentHint`,
`AllowedTools`, `Model`; Gemini has only `Description`). Each backend's
command-file writer reads only its own field, with no generic fallback
(`commandfiles.go:130`, `gemini_capabilities.go:214`). Populating both fields is
therefore load-bearing — and it is populating a bundle YAML schema, the same way
any code that builds a `LoadedContent` must, not encoding model/prompt knowledge.

The alternative — add a provider-neutral `Description` to `LoadedContent` and a
per-backend fallback in both writers — spreads a parallel "neutral description"
concept across the bundles data model and every backend writer to remove two
struct-field names from one operations function. That is churn, and it fights the
deliberately-typed schema (see [[0019]] on the CLI/operations boundary; the
plugin schema is typed on purpose because backends genuinely differ).

## Decision

Define the operations/LLM boundary as exactly two seams, and treat operations as
compliant with `bats-moody-rake` against that definition:

1. The `Distiller` interface for anything that calls an LLM.
2. The `internal/lm/backends` polymorphic package for hooks, settings, context,
   and command-file emission.

Operations may reference the typed `bundles` plugin-config structs
(`ClaudeCodeConfig`, `GeminiConfig`) when populating the bundle plugin schema.
That is config-schema population, not an LLM-specifics leak, and it stays.

## Consequences

`bats-moody-rake` closes: operations carries no model IDs, no prompts, no
backend-name string literals, and no backend-identity branching. Any new
LLM-touching operation must take the same shape — an injected interface, not a
constructed backend.

The cost is that adding a third LM backend means adding a field to
`LLMExports` and updating `getBuiltinPrompts()` to set its description. That
edit is in the bundles schema's blast radius regardless; this ADR just declines
to pretend otherwise behind a neutral-description indirection.

**Revive trigger:** ANY of —
- A model ID, system prompt, or prompt template appears as a literal in
  `internal/adapters/operations` (that IS a leak — route it through `Distiller` or a new
  injected seam).
- Operations branches on backend/plugin *identity* to change behavior (not just
  populate a config field).
- A third LM backend lands AND the per-backend description fan-out in
  `getBuiltinPrompts()` becomes a maintenance burden — at that point reconsider a
  neutral `LoadedContent.Description` with per-backend fallback.

## Amendment (2026-09-19): per-engine exports are opaque blocks

The typed per-backend export structs this ADR let operations populate are
gone. A bundle item carries `exports: {<engine name>: <block>}` and the
bundle package models it as `bundles.EngineBlocks` — one opaque JSON block
per engine name. The boundary is now:

- **Core never reads inside a block.** `core/bundles` carries blocks as
  bytes; `core/composite` assembles a `Package` whose commands and skills
  carry their blocks per engine name and hands an engine ITS block and
  nothing of another's (`composite.Package.EngineItems`); `operations`
  resolves the config and calls `composite.Assemble` once. None of them
  spells an engine's field names.
- **The engine decodes its own block** against its declared schema
  (`engine.Definition.ExportSchema`, published per engine by `gen-schemas`
  as `engine-exports-<name>`), through `engine.Engine.Exports(items)`. A
  block the schema refuses is an error naming the engine and the item — the
  engine exports nothing on a guess.
- **A command's help text is neutral.** The authored `description:` rides
  the item; an engine's block may override it. ctxloom's own embedded
  commands carry their frontmatter description the same way, so the
  per-backend fan-out this ADR once kept in operations does not exist.
- **One frozen exception, in the bundle package.** The exec trust preimage
  (`signing.CommandPreimageContract`) fixed its bytes as a canonicalisation
  of the claude-code block's fields; `bundles.CommandSurface.ExportsPayload`
  still reads that one block to produce them, so every existing
  countersignature verifies. A block for another engine rides outside that
  preimage. Widening it is a contract bump that re-signs every bundle, and
  a human's call.

The rejected alternative above — a neutral description with per-engine
fallback — is what landed, because the typed schema it was weighed against
no longer exists. The revive trigger about a third backend is superseded: a
new engine needs no change to the bundle package, to composite, or to
operations; it declares its schema and decodes its block.
