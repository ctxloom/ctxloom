# Designs A and B — where they agree, where they diverge

Two architects, same evidence (the seven seams, the synthesis, the data-flow review,
the same rulings brief), B blind to A. A was adversarially reviewed
(21-adversarial-review.md: 25 refuted / 33 held); B has not been reviewed yet.
The human's prior: where they agree is probably right; the divergences are where
the decision lives.

## 1. Where they AGREE (treat as the provisional skeleton)
- Hexagonal: a core that imports only core; engines, delivery, CLI, MCP, gRPC, fs,
  git, companions as adapters; archlint/layering rules as the enforcement.
- ONE composite package: sources (remote, project, companions, builtin) →
  verification → composition → an immutable `Package` value; trust decided inside
  the composite step, no `AdmitAll` reachable from an ungoverned constructor.
- ONE launch constructor producing ONE typed launch value that every tail consumes
  (A: `launch.Resolve → *Resolved`; B: `launch.Resolve → Launch`); the six
  `pb.RunStart` literals and the carrier structs collapse into it; permission
  floored once; both isolation axes typed; harp required.
- Delivery as a PLAN with no fallback arm: a routing function over the package and
  the engine's declaration (A `PlanFor`, B `Route`), then two ports — `Static`
  (files under a target root with one ownership record) and `Dynamic` (MCP served
  in the runner). Materialize = the same static delivery with the project root as
  target. Uninstall derives from the plan.
- The runner is the unit: `ctxloom runner` receives one launch, delivers, hosts
  the session's MCP endpoint, drives the engine, records the transcript; the
  container mode is the same runner as the container's foreground process (A
  asks it as ruling 10; B states it).
- Engine as a port implemented per package; mock conforms first, claude second,
  codex/opencode as the polymorphism proof; engine extraction after mock conformance.
- Bus: one verbs layer, one coordinator, one inbox (`spoolInbox`); the MCP server
  is an adapter inside the runner; the stdio shim/forward/marker tier deleted.
- Session dir: member classification as a type; lifetime axis persist/ephemeral.
- Identity minted once, carried typed, env decoded at the composition roots only.
- Migration: ~16 slices; the first four are pure deletions / type introductions /
  small correctness fixes an unattended run may take; launch unification after
  the type exists; every slice carries the test-determinism rule.
- The same rulings needed: earthly-city reopened and affirmed; reroot → refuse;
  exec gate fail-closed (+ local invalid signature withholds + one policy); one
  signature per bundle; the reaper clock and --include-persist.

## 2. Where they DIVERGE
| axis | A (20-target) | B (22-target-b) | review of A says | weight |
|---|---|---|---|---|
| Package layout | 28 packages, RENAMED into `core/*`, `adapters/*`, `engines/*` | keeps today's names (bundles, profiles, trust, config, paths, sessions, coord, spool) as core; ADDS composite, launch, delivery, runner, companions; RETIRES 10 (lm/backends, lm/grpc, vpio/goplugin, vpio/dockerexec, shared/ledger, lm/engine, mockengine, claude, agentcoord/discover, shared/companionloadout) | — | B moves less code to say the same thing; A's rename is a migration cost with no behaviour |
| What crosses the wire | `Package` implied to reach the runner ("holding a *Package IS the proof") | the whole `launch.Launch` in `StartRun.launch`, incl. `Loadout` (the planned bytes) AND `Package`; B flags Package size as its own uncertainty #3 | R1 REFUTED A: Package cannot cross; `Plan` must be the wire value, `PlanFor` in the originator | B is half-right (Loadout crosses typed); shipping `Package` too repeats A's R1 in weaker form. Resolve: Route in the originator, Plan/Loadout crosses, Package never leaves |
| MCP endpoint identity | output of `Dynamic.Serve`, minted per runner process | `Endpoint` "minted by whoever hosts it" (the runner), `Dynamic.Serve(ctx, plan, ep)` takes it as input; `Launch.ReachBack` carries the coordinator endpoint typed | R2 REFUTED A: must be per-harp, minted in Resolve, a typed launch input; a one-shot child would otherwise mint a listener per turn | B's reach-back is right; B's MCP endpoint has the same per-run minting weakness unless the runner outlives one-shot turns — UNRESOLVED in both |
| Config lifecycle | none (memo + reloads survive) | `config.Owner` per process → immutable `Snapshot{Config, Catalog, Generation}`; `Update` (write-through) and `Reload` (post-pull, post-scaffold, once per spawn) are the whole contract; no LoadFresh, no bundles.Loader | R3 REFUTED A; proposes `composite.Library{Current; Invalidate}` | B answers the human's race concern directly; the review's proposal and B's differ only in where the snapshot lives (composite vs config) |
| Resume | `Resolve` mints unconditionally; no resume arm | `Launch.Resume ResumeRef` + `Facts.Resume Declared[ResumeSpec]` pulled from the engine | R7 REFUTED A | B |
| Delegation depth | "exactly two depths" | `Depth int` set by the runtime coordinator | R6 REFUTED A (contradicts delegation.depth and womanless-quarterly) | B |
| Mail to a containerized child | runner inbox sweeps a spool the container cannot see | the session dir (scoped) is MOUNTED into the container, so the spool is visible; "no shared filesystem for the session dir" is a stated non-goal | R5 REFUTED A; review wants a push verb on RunnerLink | B's answer is coherent but rests on the mount; the review's push verb would remove the dependency. A design decision, not a defect |
| Engine port width | 6 methods: Describe, Surfaces, Command, Turn, Chat, History | 12: Facts, Surfaces, CLI, Exports, ExportSchema, Home, Container, Transcripts, Version, Exec, Drive, Hooks — optional capabilities as `Declared[T]` | — | B pulls more engine-specific knowledge behind the port (the ruling); A leaves home/container/transcript knowledge in core. B closer to the ruling; wider interface to conform |
| go-plugin | KEPT as `adapters/pluginwire` (llm.proto for the interactive turn) | DELETED: one arm; StartRun carries the launch; the runner is spawned directly; `lm/grpc`, `vpio/goplugin` retired (B's new ruling 5) | R10: protobuf inside A's core violates its own rule on day one | the largest single divergence; a dependency/wire deletion the human must rule |
| operations' ring | a "use-case ring" between core and adapters (rule R2) | an adapter: application services per ADR 0019/0026; cli forbidden from every domain package (`cli-through-operations`) | — | same intent, different ring; B's is the existing ADR |
| Reach-back from containers | no home for endpoint selection; no test per slice | `Launch.ReachBack sessions.Endpoint` set by the spawner; each slice names the j002300/j002200/container scenarios it keeps green | R11 REFUTED A | B |
| Ownership record | one sidecar `Ownership` per target | one record via confpatch | R4 REFUTED A (two owners on one target) | B, provisionally |
| Rulings added | ten | eight, incl. NEW: delete the go-plugin arm; per-engine export blocks (ADR 0020 amendment); keep reload-per-spawn at one site | — | the go-plugin question is the one that changes the plan |

## 3. Reading
The skeleton is consistent and matches the review's "the shape is right". B already
holds on seven of the review's eleven refutations of A (config owner, resume,
depth, reach-back, ownership, wire value in part, endpoint as input), because it
had the config-lifecycle and data-passing emphases A did not. What B has NOT been
tested against: Package on the wire (its own uncertainty #3), the MCP endpoint's
lifetime across one-shot turns, the mount dependency for container mail, core
purity (does `agentcoord/coord` in B's core import the proto?), and the go-plugin
deletion's blast radius on the interactive turn.
