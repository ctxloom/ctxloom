# CLI Restructure Plan — SUPERSEDED

This plan is obsolete. The CLI shipped a different, content-centric design —
fragment/skill/profile as the primary verbs; `install`+`sync` folded into the
declarative `deps pull`; per-content `edit`; `config`/`mcp` under `manage`;
`bundle` hidden — documented in [docs/cli-reference.md](cli-reference.md).
Phases 4–9 of the original plan (install auto-detect, `edit` metadata,
`config mcp/plugin`, command promotion, help text, back-compat aliases) were not
executed by design.
