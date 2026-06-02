# 0021 — Consolidate all LLM config under `llm:`; breaking schema change

**Date:** 2026-06-01.

## Status

Accepted.

## Context

The `plugin`→`llm` rename surfaced a pre-existing organization problem in the
config. LLM settings were split across two sections: definitions under `llm`
(`llm.plugins`, `llm.plugin_paths`) but selection/tuning under `defaults`
(`defaults.llm_plugin`, `defaults.llm_model`, `defaults.compaction_*`). Reading
"what LLM, configured how" meant looking in two places, and a literal rename
produced the stutter `llm.llms`.

A config-key audit (2026-06-01) also found dead/half-wired knobs:
- `llm.llm_paths` + the external-LLM discovery cluster (`findExternalPlugins`,
  `llm list`/`default` scanning, `llm extract`): `ctxloom run` gates on the
  built-in backend registry (`backends.Exists`) with no dynamic registration, so
  a binary discovered via `llm_paths` can never be launched by name. Stranded.
- `sync.lock` / `sync.apply_hooks`: accessors `ShouldLock`/`ShouldApplyHooks`
  had zero callers.
- The generated config's empty `claude-code: {}` entry: indistinguishable from
  omitting it.

## Decision

Consolidate every LLM setting under `llm:` and drop the dead knobs. New shape:

```yaml
llm:
  default: claude-code     # was defaults.llm_plugin
  model: opus              # was defaults.llm_model (optional)
  configs:                 # was llm.plugins — per-LLM overrides, optional
    claude-code: { binary_path, model, args, env }
  compaction:              # was defaults.compaction_*
    llm: claude-code
    model: haiku
    chunks: 8000
defaults:
  profiles: [...]
  use_distilled: true
```

Removed: `llm.llm_paths`, `defaults.llm_plugin/llm_model/compaction_*`,
`sync.lock`, `sync.apply_hooks`, the external-LLM discovery cluster, and the
empty generated-config placeholder. Go: `LMConfig` gains `Default`/`Model`/
`Configs`/`Compaction` (+ new `CompactionConfig`); `Defaults` keeps only
`Profiles`/`UseDistilled`; `SyncConfig` keeps only `AutoSync`.

This is a **breaking** config change with **no migration shim** (per the
project-owner directive to avoid carrying two names / dual-read paths). Existing
`config.yaml` files using the old keys must be hand-migrated; unmigrated keys are
silently ignored (mapstructure) and fall back to defaults — fault-tolerant, but
the old setting stops taking effect.

## Consequences

`llm` is the single home for LLM configuration; `defaults` is now genuinely
"misc defaults." External LLM binaries are still runnable by overriding a
built-in's `configs.<name>.binary_path`; only the (non-functional) discovery
surface is gone.

Migration cost falls on existing users. Because there is no shim, a stale
`defaults.llm_plugin` will be ignored and the project will fall back to
`claude-code` — visible as "my default LLM reverted." Documented in the
changelog/release notes for this version.

**Revive trigger:** if first-class third-party LLM binaries are wanted, re-add a
discovery path — but wire it into the run registry (dynamic `backends.Register`)
so discovered binaries are actually launchable, not just listed.
