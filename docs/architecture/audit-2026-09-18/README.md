# Architecture audit — 2026-09-18

A seven-seam read-only audit of the codebase at release/0.7 `d42cc4229`, produced
by delegated analysts and assembled by a synthesis pass. Each seam document carries
mermaid call graphs, a delegation/layer graph, data-flow graphs, findings ranked by
blast radius, and the boundary signatures annotated input/output/hidden-input.
References are by symbol and file, never line numbers.

- `00-coordinator-notes.md` — facts the coordinator verified empirically (not from graphs)
- `01`–`07` — the seams: launch paths, MCP paths, delivery and surfaces, coordination
  bus, trust/signing/remote, cli↔operations and the binaries, sessions/transcripts
- `10-synthesis.md` — the assembled whole-system graphs (Part A) and the analysis:
  missing layers, duplication/workaround/divergent-path ledgers, arch-gate gaps, and a
  ranked refactor program with the human rulings each slice needs (Part B)
- `11-dataflow-review.md` — the data-flow lens (unused inputs, guessed inputs,
  coupling, internals passed out), added when it lands
- `briefs/` — what each analyst was asked, for scope

These are a snapshot: they describe the tree they were read from. Findings that land
are recorded in the task log; the documents are not maintained forward.
