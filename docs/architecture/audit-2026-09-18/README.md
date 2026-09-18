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
- `20-target-architecture.md` — the revised architecture designed from the review under
  the human's rulings (hexagonal core/ports/adapters; engines as polymorphic plugin
  packages; one composite package from repos, project and companions; static and
  dynamic delivery; session home as the default root; one owner per harp over the
  runner socket), as package boundaries, exported signatures, the target data flow,
  a deletion ledger, the rulings it needs, and an ordered migration with three
  design-by-test bodies
- `21-adversarial-review.md` — the attack on design A: 25 refuted / 33 held, with the smallest
  design change that closes each refutation
- `22-target-architecture-b.md` — design B, made blind to A from the same evidence, with
  the config-lifecycle and pass-once emphases
- `23-comparison-a-vs-b.md` — where A and B agree (the provisional skeleton) and the
  divergence table with the review's bearing on each
- `24-adversarial-review-b.md` — the attack on design B against the rulings made after
  the comparison, added when it lands
- `briefs/` — what each analyst was asked, for scope

These are a snapshot: they describe the tree they were read from. Findings that land
are recorded in the task log; the documents are not maintained forward.
