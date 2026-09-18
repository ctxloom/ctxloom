# DATA-FLOW REVIEW BRIEF — the Fable pass over the assembled architecture

You are the data-flow reviewer for a completed architecture audit of ctxloom. Seven seam documents and a synthesis exist; your job is a DIFFERENT lens on the same material, applied to the code: how data actually moves through calls, and what its movement reveals about layers that are guessing. The human's own words for the lens:
  - "passed data that's not used"
  - "data that should be passed — what's lacking indicates that subsequent, deeper items are probably making things up or assuming"
  - "unnecessary coupling"
  - "lack of data transformations — passing internal things out"

## Inputs
Directory /home/babbitt/.ctxloom/sessions/vapid-fair-going/persist/arch-audit/
- 10-synthesis.md — read FIRST: Part A4 (harp + resolved-launch data-flow), A2 (launch divergence), A3 (MCP/delegation), and B1 (missing layers ML-A..ML-H). The synthesis is your map; the code is your evidence.
- 01..07-*.md — each has a "data-flow smells" list and a signatures section annotated INPUT/OUTPUT/HIDDEN. Use them as leads, not as findings: verify each in code before you keep it.
- 00-coordinator-notes.md — coordinator-VERIFIED facts.
Repository: /home/babbitt/workspace/ctxloom/ctxloom/main — READ-ONLY (never edit, checkout, stash, commit). `gopls references/implementation/call_hierarchy` is available; use it over grep for symbols. No slow commands (no `just test*`, no `go test ./...`).

## What to produce — /home/babbitt/.ctxloom/sessions/vapid-fair-going/persist/arch-audit/11-dataflow-review.md (absolute; write incrementally)
Work the boundaries the synthesis names (launch, delivery, MCP/delegation, bus, trust, cli↔operations, sessions). For each boundary signature and each hop on the A2/A3/A4 graphs, answer four questions IN CODE and record the answer by symbol+file (never line numbers):

1. UNUSED INPUTS. Parameters (or struct fields on a parameter) the callee never reads — including fields threaded through N layers and read by none of them. Prove it with gopls references on the field/param, not by eye. Table: symbol, parameter/field, who passes it, evidence, what its presence made callers believe.

2. MISSING INPUTS — THE GUESSING TELL. Where a callee needs a fact its caller HAD but did not pass, so the callee re-derives it (config reloaded, path recomputed, harp/cwd/project re-read from env or disk), DEFAULTS it, or ASSUMES it. For each: the fact, where it was known, where it was re-derived/assumed, and whether the two can disagree (that is the defect). Rank by "can disagree in a real run". Include booleans/enums that select behaviour deep in a chain but are decided far away with no carrier.

3. UNNECESSARY COUPLING. A callee that receives a whole *config.Config / *Coordinator / runState / ManagedConfig and reads two fields; interfaces satisfied by one type; packages imported for one constant; callers that reach into a struct's internals instead of asking; env vars and files used as an in-process parameter channel. For each: what the narrow input would be, and what it would let the caller stop knowing.

4. MISSING TRANSFORMATIONS — INTERNALS PASSED OUT. Places where an internal representation crosses a boundary unchanged: a config struct or pb message handed to the CLI renderer or the MCP response, a raw Go struct marshalled as JSON output (row threefold-chump names one), a filesystem path or env-var name that escapes as an API value, a proto type used as a domain type, a bundle-internal type reaching the engine writer. For each: the boundary, the type that crossed, what a DTO/domain type would hide, and which consumers currently depend on the leak.

Then:
5. THE GUESSING MAP (mermaid flowchart): for the resolved-launch value and the session identity, every hop from where a fact is decided to where it is consumed, with each hop coloured/labelled as PASSED (typed param), CARRIED (env/file/global), RE-DERIVED, or ASSUMED. This is the picture the human asked for.
6. RANKED FINDINGS: each with symbol+file, the four-question category, blast radius, and what settles it (usually: pass the value; delete the re-derivation; introduce the narrow type). Say which synthesis missing-layer (ML-A..H) each finding feeds. Prefer findings that let code be deleted.
7. Disagreements with the seams/synthesis and how you resolved them in code; your uncertainties last.

## FINAL report
File `agent_report` scope FINAL with the document path, counts per category (unused / missing / coupling / transformations), the top five findings in one line each, and what you deferred ("nothing deferred" if true).
