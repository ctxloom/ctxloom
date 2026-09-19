# COMMON BRIEF — architecture audit grapher (read fully before starting)

You are a LEAF analyst in a seven-agent architecture audit of ctxloom (Go, ~250k production lines). You own ONE seam (named below). Your deliverable is a Markdown document with MERMAID call graphs and delegation/layer graphs for your seam, plus analysis. Nobody will answer questions; record uncertainties in the document and continue.

## Ground rules
- READ-ONLY in the repository. You are in the live checkout /home/babbitt/workspace/ctxloom/ctxloom/main; do NOT create, edit, or delete any file under it, do not run `git checkout`/`stash`/`commit`, do not build artifacts into it. A human and other agents are working in it.
- Your ONLY output location is the ABSOLUTE directory /home/babbitt/.ctxloom/sessions/vapid-fair-going/persist/arch-audit/ — write your document there (filename given below). Relative paths and any other location are black holes.
- WRITE INCREMENTALLY: create the file with a header in your first ten minutes, then append each graph and each finding as you produce it. If you die mid-run, what is on disk is the deliverable.
- References are BY SYMBOL AND FILE: `package.Func`, `package.Type.Method`, `internal/x/y.go`. NEVER line numbers.
- No slow commands: never `just test`, `just test-acceptance`, `go test ./...`, or anything over ~60s. Nothing here needs tests to run. Do not background commands; do not poll.

## How to build the graphs — tools you have
- `go list -deps ./internal/<pkg>` and `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/...` for the package import graph.
- `gopls` CLI is installed: `gopls references <file>:<line>:<col>`, `gopls implementation ...`, `gopls call_hierarchy <file>:<line>:<col>` (find the position with `grep -n` first). Prefer gopls over grep for symbols: grep misses aliased refs and matches comments.
- `git grep -n 'func (.*) Name\|func Name'` to find definitions; read signatures with `sed -n` / `head`.
- The STATED architecture: `GLOSSARY.md`, `docs/architecture/**` (agentcoord, cli, companions, core, engines, shared), and the enforced rules in `tests/arch/` (especially `layering_test.go`, `lean_binaries_arch_test.go`, `write_discipline_test.go`, `path_authority_test.go`). Read the ones relevant to your seam FIRST, then map the actual code, then DIFF stated vs actual.
- `taskloom list --tag-query area:<your area> --compact` and `taskloom show <harp>` for rows already recording known defects in your seam; do not re-derive what a row already says — cite the harp.

## What the document must contain (in this order)
1. **Scope and entry points**: the seam, the packages read, every ENTRY POINT you traced (CLI verb, MCP tool, gRPC handler, hook, goroutine), each as `package.Symbol` + file.
2. **Call graphs** (mermaid `flowchart LR`, one per entry point family). Nodes are `pkg.Symbol`; group nodes in `subgraph <package>` blocks; edges are calls. Keep each graph under ~60 nodes — split rather than cram. Node ids must be mermaid-safe (letters/digits/underscore); put the readable name in the label. Do not draw the standard library.
3. **Delegation / layer graph** (mermaid): the packages as nodes, edges = "delegates to / calls into", with the direction the STATED architecture expects marked distinct from edges that go AGAINST it or SKIP a layer (use a different arrow style or edge label). This is the graph the human will read first.
4. **Findings**, each in this shape and cited by symbol+file:
   - DUPLICATION: two or more implementations of one concept (same job done twice; two orchestrators for one path; two state machines; copy-pasted setup). Name both sites and say which is the more complete.
   - DIVERGENT PATHS: entry points that should share a pipeline and do not (e.g. one-shots that skip startup steps a normal run performs). Show the divergence as a mermaid graph with the shared trunk and the branches, and list the steps one branch skips.
   - LAYER BYPASS: a call that reaches past a layer that exists (cli reaching into a package that operations should mediate; coord reaching into cli; a handler doing what a service should).
   - MISSING LAYER: a concept that has no home, so its logic is smeared across callers. Say what the layer would be and which sites would collapse into it.
   - WORKAROUNDS: comments that explain WHY a workaround exists, arbitrary limits/sleeps/retries around deterministic things, thresholds tuned to silence gates, fallbacks masking failures. Each one is an unfiled bug: quote the comment, name the symbol.
   - STATED-VS-ACTUAL: every place the docs/glossary/arch tests say one thing and the code does another.
   Rank findings by blast radius. For each, one sentence on what would settle it (a test, a deletion, a move).
5. **Signatures that matter**: the exported signatures at your seam's boundaries (copy them verbatim), so the human can review the contract without opening files.
6. **Uncertainties**: what you could not determine and why.
7. **Handoff**: which OTHER seams your findings touch (name the seam number) so the synthesis pass can join them.

## Depth
Be thorough. Read the signatures, not just names. Follow every entry point to the process boundary (exec, gRPC, file write, socket). Where two paths look the same, diff them step by step and say what differs. Expect this to take real time; do not summarise early.

## FINAL report
When the document is complete, file `agent_report` with scope FINAL containing: the absolute path of your document, its section list with the number of graphs and findings, your top three findings in one line each, and anything you deferred or could not cover — "nothing deferred" if true. The document is the deliverable; the report tells the coordinator it exists.

# YOUR SEAM — 1. LAUNCH PATHS. Output file: 01-launch-paths.md

Every way an engine process gets started, traced to the exec/spawn. Known symptom from the human: "one-shots not going through the same startup procedures". Entry points to trace (find more):
- `ctxloom run` (internal/adapters/cli/run.go, run_owned.go; cli.launchEngineWithPrompt), `ctxloom init`'s launch (internal/adapters/cli/init_launch.go), oneshot (internal/adapters/operations/oneshot.go, operations.RunOneshot), distill/compact one-shots (internal/adapters/cli/distiller.go, cli.newLLMDistiller), task triage / init auth probe if they launch anything.
- `ctxloom llm host <backend> --label` (internal/adapters/cli/llm_host.go, llm_runner_common.go) and who spawns it: internal/adapters/isolation/none.go, direct_runner.go; the container runners in internal/adapters/isolation.
- The coordinator's spawn: internal/core/coord/spawner.go (StartRun, Options.Starter), enginehost.go, owner_run.go; coordtest runners.
- Engine backends: internal/lm/backends (managed.go, LoadSkillExports), internal/engines/claude/chat_run.go, mockengine.
- LaunchForm and the "resolved once host-side, carried to the plugin" principle (grep LaunchForm) — is it honoured by every path?
- Harp minting: which paths mint a session harp and which do not (rows scant-undoing, boned-monoxide describe the intended invariant "every run mints a harp").
For each path produce the startup step list (config load, profile resolution, harp mint, session dir, engine home, surface delivery, hooks install, MCP config, env, exec) and DIFF the lists. The divergence graph (shared trunk vs branches) is the centrepiece. Read rows: scant-undoing, concerned-levitator, dimmed-epidural, boned-monoxide, tranquil-mutiny, broken-jailbreak.

## DATA FLOW — required on every graph (added by the human)
Track what CROSSES each call edge, not just that the call happens:
- On every call-graph edge, label the ARGUMENTS that carry state (the types, and for structs the fields that matter downstream) and the RETURNS. Mermaid edge labels: `A -- "cfg *config.Config, harp string / (Paths, error)" --> B`. Omit trivial plumbing (ctx, loggers) unless the logger IS the finding.
- For each seam, produce at least one DATA-FLOW graph (mermaid `flowchart`) for its central value(s): where a value is CREATED, every hand that TRANSFORMS it, and where it is CONSUMED (written to disk, sent on the wire, exec'd). Candidates: the launch form / resolved paths (seam 1), an MCP tool request and its session identity (seam 2), a bundle item and its bytes (seam 3), a mail item and a run record (seam 4), a preimage and an approval (seam 5), a config value from flag/env/file to use site (seam 6), a session harp and a transcript path (seam 7).
- Call out DATA-FLOW SMELLS explicitly, each cited by symbol+file: a value re-derived at several sites instead of passed (e.g. config loaded again downstream, a path recomputed from parts, a harp re-read from env deep in a call chain); "God" parameters/structs that carry everything so callers cannot tell what a callee reads; values that travel through globals, env vars, or the filesystem between two functions in the same process (a hidden parameter); return values that are ignored at a call site; the same value with two names or two types along one path; a boolean/enum flag threaded through many layers that each branch on it (feature-flag layering).
- In the SIGNATURES section, for each boundary signature say which parameters are INPUT state, which are OUTPUT, and which are hidden inputs (env, globals, files read inside).
These data-flow views are the primary input to the architectural analysis; make them precise.
