# SYNTHESIS BRIEF — assemble the whole-system graphs and analyse them

You are the final analyst in an architecture audit of ctxloom. Seven seam analysts have each produced a document with mermaid call graphs, delegation/layer graphs, data-flow graphs, and findings. Your job is to ASSEMBLE those into whole-system graphs and then analyse from the assembled graphs — duplication, workarounds, layer bypass, missing layers, divergent paths — and to produce a ranked program of refactors the human can act on. Nobody will answer questions; record uncertainties and continue.

## Inputs (read ALL of them, in full, first)
Directory: /home/babbitt/.ctxloom/sessions/vapid-fair-going/persist/arch-audit/
- 00-coordinator-notes.md    — findings the coordinator VERIFIED empirically (treat as confirmed)
- 01-launch-paths.md         — seam 1 (14 entry points, 9 graphs, 10 findings)
- 02-mcp-paths.md            — seam 2 (10 graphs, 15 findings)
- 03-delivery-surfaces.md    — seam 3 (10 graphs, 21 findings)
- 04-coordination-bus.md     — seam 4 (13 graphs, 12 findings)
- 05-trust-signing-remote.md — seam 5 (6 graphs, 52 findings)
- 06-cli-operations.md       — seam 6
- 07-sessions-transcripts.md — seam 7
- briefs/                    — what each analyst was asked (for scope)
Also read these already-filed rows before ranking, so you cite rather than re-derive: `taskloom show nifty-rival` (duplicated one-shot LLM plumbing), `easeful-chump` (six path-confinement implementations), `unskilled-state` (two copies of every arch rule), `careless-nanny` (one composition root), `scant-undoing`, `earthly-city` (closed as superseded — seam 1 says wrongly), `tacky-padding`, `blissful-blah`, `deceased-yoga`, `boned-monoxide`, `tranquil-mutiny`.

## Ground rules
- READ-ONLY in the repository (/home/babbitt/workspace/ctxloom/ctxloom/main). Verify a seam claim against the code when two seams disagree or when you are about to rank it in the top ten; otherwise trust the seam documents. Never edit, checkout, stash or commit. No slow commands (no `just test*`, no `go test ./...`).
- Output ONLY to /home/babbitt/.ctxloom/sessions/vapid-fair-going/persist/arch-audit/10-synthesis.md (absolute). Write incrementally: header first, then each assembled graph as you finish it, then the analysis.
- References by SYMBOL and FILE (`package.Func`, `internal/x/y.go`). NEVER line numbers.

## Part A — ASSEMBLED GRAPHS (the human's primary deliverable)
Build these as mermaid, each in its own section, each under ~80 nodes (split by concern if larger; never cram):
A1. WHOLE-SYSTEM LAYER GRAPH: every package that appears in any seam's delegation graph, grouped into the layers the STATED architecture names (cli / operations / shared+delivery / engines+lm / agentcoord / trust+remote / sessions+transcript / paths+config), with edges = actual dependencies. Mark three edge classes distinctly: conforms; AGAINST the stated direction; SKIPS a layer. Use the seams' layer graphs as sources and reconcile their vocabulary.
A2. UNIFIED LAUNCH GRAPH: every entry point that starts an engine (seam 1) joined to the delivery pipeline it does or does not run (seam 3), the bus arm it takes (seam 4), the MCP config it receives (seam 2), and the trust gate it does or does not attach (seam 5). One graph, the shared trunk in the middle, each branch labelled with what it SKIPS. This is the divergence map for "one-shots and children do not go through the same startup".
A3. UNIFIED MCP/DELEGATION GRAPH: one tool call (agent_run, agent_send, agent_recv) from an engine's stdio to the spool file and back, across every physical path (stdio-local, forwarded, runner-hosted, in-process), joined to seam 4's routing and seam 7's session identity. Mark every place a second orchestrator exists.
A4. UNIFIED DATA-FLOW: the session harp and the resolved launch value from creation to every consumer, across seams 1, 2, 3, 7 — where it is minted, every hand that re-derives it from env/files instead of receiving it, and every consumer. Label each edge with the carrier (typed param / env var / file / global).
A5. TRUST CHOKE GRAPH: bytes fetched from a remote to bytes an engine executes, every verification point, every path that reaches delivery without one (seam 5 joined to seam 3's writers and seam 1's exec sites).
A6. STATED-VS-ACTUAL MAP: a table (not a graph) of every doc/glossary/arch-test statement the seams found false, grouped by document, with the symbol that contradicts it.
Keep the seams' node names; where two seams name one thing differently, pick one and list the aliases in a legend under the graph.

## Part B — ANALYSIS (from the assembled graphs, not from the seams' prose)
B1. THE MISSING LAYERS: name each layer the assembled graphs show should exist (the seams propose several: a "resolved launch" type, a delegation-verb layer, one spoolInbox, a gate-holder, a single context-assembly, a session-identity carrier). For each: what it is, which existing sites collapse into it, which findings across seams it settles, and what net code DELETION it enables. Prefer proposals that delete more than they add.
B2. DUPLICATION LEDGER: every duplicated concept across all seams as one table — concept, sites (symbol+file), which is the more complete, seams citing it, and the collapse target from B1.
B3. WORKAROUND LEDGER: every quoted workaround comment/limit/sleep/fallback across seams — symbol, the quoted why, the underlying bug it hides, and whether a row already tracks it (harp) or not.
B4. DIVERGENT-PATH LEDGER: from A2/A3 — each branch, what it skips, the user-visible consequence (e.g. "delegated children run with no hooks and the human's real ~/.claude"), and the convergence target.
B5. ARCH-GATE GAPS: which of the findings would have been caught by an existing tests/arch gate if it were aimed correctly, and which need a NEW gate (name the gate's rule in one sentence). Include the seams' proposal of a gate that fails on unresolvable symbols in prose.
B6. RANKED PROGRAM: an ordered list of refactor slices, each: title, findings settled (seam.F#), files/symbols touched, net LOC direction (delete/add), risk (which stop conditions it trips: wire, on-disk, trust, prompt), prerequisite slices, what gate settles it. Order by blast radius ÷ risk. The first three slices should be the ones a human would approve today. Say explicitly which slices contradict a standing human ruling (cite the row) so the human can re-rule rather than be surprised.
B7. DISAGREEMENTS between seams, and how you resolved each (by reading code). Your own uncertainties last.

## FINAL report
File `agent_report` scope FINAL with: the document path, the count of assembled graphs and ledger rows, the top three program slices in one line each, seam disagreements you resolved, and what you deferred ("nothing deferred" if true).
