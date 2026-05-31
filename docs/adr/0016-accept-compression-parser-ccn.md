# 0016 — Accept >10 cyclomatic complexity in compression parsers/classifiers

**Date:** 2026-05-31.

## Status

Accepted.

## Context

The Phase 2 complexity-remediation batch drove production functions to CCN ≤ 10.
Nine functions in `internal/compression` resist that target without harm:

| Function | CCN | File | Shape |
|----------|----:|------|-------|
| `DetectContentType` | 17 | compressor.go | switch over file extension + content sniff |
| `extractPython` | 16 | code_python.go | tree-sitter AST visitor (switch per node kind) |
| `extractJS` | 15 | code_javascript.go | tree-sitter AST visitor |
| `isIdentifier` | 14 | json.go | string-shape classifier (UUID / URL / path / alnum ratio) |
| `extractRust` | 12 | code_rust.go | tree-sitter AST visitor |
| `detectLanguage` | 12 | code_treesitter.go | content-heuristic language classifier |
| `extractGo` | 11 | code_go.go | tree-sitter AST visitor |
| `extractRustFunc` | 11 | code_rust.go | tree-sitter AST visitor |
| `extractJSMethodSig` | 11 | code_javascript.go | tree-sitter AST visitor |

Every one is **one branch per token / node kind / language / pattern**. The
branch count *is* the cyclomatic count, and the branch count is dictated by the
external grammar (the tree-sitter node taxonomy, the set of supported
languages, the shapes a JSON string can take) — not by tangled control flow.

The available CCN-reducing moves all make these *worse*:

- A handler **map** (`map[nodeKind]func(...)`) trades a flat, readable switch
  for scattered closures plus a dispatch table — same number of cases, lower
  locality, and the per-branch work (recursing into children, appending to
  shared `preserved`/`compressed` builders) doesn't factor cleanly into uniform
  handler signatures.
- Splitting a visitor by node kind fragments tightly-coupled parsing logic
  across multiple functions that are only ever called from the one switch.
- The classifiers (`DetectContentType`, `detectLanguage`, `isIdentifier`) are
  short, linear cascades of independent predicates — collapsing them into a
  predicate table buys nothing; the cascade *is* the specification.

Six of the nine are behind `//go:build treesitter` and only compile in the
devcontainer, so they aren't exercised by the offline test path either.

## Decision

Accept these nine functions above CCN 10. Each carries a one-line load-bearing
comment stating the invariant (one branch per node kind / language / pattern)
and pointing here, so a future reader doesn't mistake the high CCN for neglect.
Do **not** refactor them into handler maps or per-kind helper functions.

## Consequences

`just complexity -C 10` continues to flag these nine. That's expected — the
load-bearing comments and this ADR are the record that the flag is acknowledged
and intentional, not a backlog item. Phase 2's "production code ≤ 10" claim is
read as "≤ 10 except the parsers documented in ADR 0016."

**Revive trigger:** any of these functions exceeds **CCN 25**, OR a branch is
added that is *not* a per-token/per-node/per-language case (i.e. real control-
flow complexity creeps in alongside the grammar dispatch). Either signals that
the function has outgrown "flat switch over a grammar" and warrants a genuine
restructure.
