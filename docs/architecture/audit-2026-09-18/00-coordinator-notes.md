# Coordinator notes — architecture audit 2026-09-18

## Verified empirically (not from the graphs)

### Seam 1 F1 — delegated children run WITHOUT ctxloom surfaces. CONFIRMED.
A live `agent_run` child (developer-host, harp wordy-sick-dude, spawned by this
coordinator) runs its claude engine with:
  HOME=/home/babbitt                      (the REAL home; no relocated engine home)
  no CLAUDE_CONFIG_DIR, no --settings, no --append-system-prompt-file
  --mcp-config /tmp/ctxloom-claude-chat-mcp-<rand>/.mcp.json   (os.MkdirTemp)
  CTXLOOM_SESSION_HARP=<harp>
and ~/.ctxloom/sessions/<harp>/ contains only persist/. No engine home, no
settings.json, no hooks, no commands/skills dir, no CLAUDE.md delivery.
Consequence: no managed hook fires for any child (ltk, tool-reflect, hud,
next-step, skill-mates); the agent's composed profiles reach it only through the
first-turn prompt, if at all; the child reads the human's real ~/.claude.
This is the path every child of the last two nights took. It contradicts the
"delivery never degrades; private EngineHome is the root" rule and the memory
"child agents honor composed profiles". Join question for seams 1+3+4.

### Container reuse — ABSENT (checked before the audit landed)
Shutdown exists (--rm; bounded remove/kill in isolation/attach.go;
isolation.ReapOrphanedContainers from operations startup). Reuse does not:
isolation.containerName mints a random token per start, coord.resumeChild
enqueues a fresh run, and under ResumeModeOneShot a new run_id per turn means a
container child pays a container start PER TURN. Candidate design: container
lifetime = session harp, engine recycled inside, idle-container reaper.

### Seam 2 F10 — the forward shim never forwards resource templates, so
ctxloom://fragments/{name} is unreachable behind it. Not yet re-verified here;
consistent with the premise-catalog instruction failing silently for shim users.

### Seam 7 F1 — keep marker swept into persist/ — CONFIRMED and FIXED (a9b61bfae)
HarpTopLevelArtifacts now excludes paths.SessionKeepMarkerFileName and
paths.NextStepFileName; the test names both, and the mutant (omitting them)
reports both as "authored artifacts". Package, build, lint and test-arch green.
Synthesis should treat 7.F1 as closed; the pattern (a hand-listed member
classification with no single predicate, seam 7 F9) stands.

### Design A + adversarial review (21-adversarial-review.md): 25 refuted / 33 held
Shape held (hexagon, composite package, one launch value, one verbs layer). Broke
at the PROCESS BOUNDARY: R1 *Package cannot cross the wire (Plan must be the wire
value, PlanFor in the originator); R2 the MCP endpoint is an output of
Dynamic.Serve per runner process, not a per-harp typed launch input; R3 no single
config owner / invalidation contract, hot reload deleted with nothing replacing it
(proposed composite.Library{Current; Invalidate}); R5 container children cannot
receive mail (runner Inbox sweeps an unmounted spool; RunnerLink has no push verb);
R6 "exactly two depths" contradicts delegation.depth and womanless-quarterly; R7
Resolve mints unconditionally — no resume arm; R8 host facts (Binary(), host home)
handed to a container as if universal; R9 the routing rule is a substitution arm
under a new name; R10 protobuf in core/bus and afero in core/engine violate the
design's own R1/R9 on day one; R11 no reach-back-from-container test in any slice.
These are the divergence axes to weigh B against: wire value, endpoint identity,
config ownership, mail push to containers, depth, resume, container carriers,
routing, core purity, reach-back proof.

### Human rulings after the A/B comparison (2026-09-18, evening)
- go-plugin: DELETE the arm (B's proposal stands). One arm: the runner is spawned
  directly and StartRun carries the launch.
- Engine port: B's shape. "Engines should be largely declarative, reusable parts;
  constructor feeds in session specifics. Pretty much all operations should go
  through engine declarations polymorphically."
- Package on the wire: "the package should serialize and be available for the local
  launcher; that should reduce duplication" — the Package is a serializable value;
  the runner and the local launcher consume the SAME serialized form. (Overrides
  the review's "Package never leaves the originator"; the review's size concern
  still needs an answer.)
- MCP endpoint: "does still need to be minted per SESSION, as it carries the
  dynamic context assembly and needs that session's packages." Per session harp,
  not per run; stable across one-shot turns.
- Package layout (A's rename vs B keeping names): "probably better" — side not
  named; OPEN. Cost both.

## RULINGS ON DESIGN C (2026-09-18 evening, one at a time)
1. SKELETON RATIFIED, WITH THE RENAME: the hexagonal rings and C's topological order
   stand; packages MOVE into core/ and adapters/ (and engines/) directories after
   all — the ~108 extra importing sites are accepted for the clearer tree. Core
   purity as a shrinking-allowlist ratchet from day one.
2. ENGINE PORT — REFINED, not C's shape verbatim: "an engine is a largely
   declarative STRUCT, with the ability to carry derivative values and methods that
   may take data and engine-specific logic." So: one struct type per engine package
   whose declarative fields are the Definition (surfaces, approaches, capabilities,
   home/container/transcript specs, export schema), plus derived values computed
   from them, plus methods holding the engine-specific logic (exec composition,
   drive, exports decoding) — the constructor still fed session specifics. C's
   Definition/Instance split maps onto the struct's declarative part and its
   methods; the port the core consumes is the struct (or a small interface the
   struct satisfies), not "Definition() as data". Zero-allowlist for mock still
   holds. Operations branch on the struct's declarations, never on a name.
2. ENGINE PORT — AGREED IN CONVERSATION (supersedes the note above):
   - Idiomatic Go: core owns `engine.Definition` (a declarative STRUCT: Name,
     Surfaces, Resume, OneShot, Home, Container, Transcripts, ExportSchema) with
     engine-agnostic derived METHODS (Static(), Carries(kind)); each engine package
     defines its OWN struct type (Claude, Mock, Codex) embedding Definition, built
     ONCE at the composition root and immutable — it IS the engine kind — with
     methods carrying the engine-specific logic. Core consumes it via
     `Engine{Def(); Instance(Session); Exports(Items)}`.
   - Definition.Surfaces = today's approach vocabulary per surface kind
     (Declaration / SurfaceKind / Approach); delivery.Route reads it and nothing
     else about the engine.
   - An engine kind is instantiated MANY times: `Instance(Session)` is per agent
     per launch; llm.configs labels (claude-code/claude-fast/claude-sonnet) are
     LabelConfig on the Session, not engine identity.
   - Instance LIFETIME = SESSION (matches endpoint-per-session and runner
     lifetime = session): one-shot turns are frames driven on the live instance;
     resume re-attaches. Instance{Exec(presented) (Exec, error); Drive()
     Declared[StructuredDriver]}.
   - Session (engine-facing projection): Identity, LabelConfig, Permission
     (floored), Roots (private home, project root, cell workdir), MCP endpoint +
     bearer, Prompt, Env additions. NOT in Session: the package (already
     Presentations), the trust gate, the isolation axes, the coordinator
     credential; container-aware behaviour comes from Definition.Container, never
     from an axis on the session.
   - `Declared[T]{Present bool; Value T}` with Declare/None: an explicitly stated
     optional capability; absent = the engine declares it does not have it;
     reading one without branching on Present is a lint/arch violation; a
     missing declaration at a point of use is ErrCapabilityUndeclared (fail loud,
     never fallback). Used on Definition.{Resume,OneShot,Home,Container,
     Transcripts} and Instance.Drive().
3. COMPOSITE PACKAGE — RATIFIED WITHOUT THE SEAL: no MAC; if security is needed it
   layers on top as auth. One immutable composite.Package, no ungated constructor,
   the trust gate a value the assembler holds. Transport is POLYMORPHIC with a
   claim-check adapter (mounted session dir / content-addressed store; typed claim
   on StartRun); NO BySize type — Resolve builds the package, measures it, and
   takes the claim-check path when it must (a conditional, not a third adapter).
   Both launchers redeem through the same codec; MaxRecvMsgSize set explicitly.
2b. ENGINE PORT, CORRECTION: Definition.Surfaces is NOT declared separately — that
   duplicates the approach implementations (today's claude/surfaces.go carries
   both and they can drift). The engine registers its []present.Approach (each
   with Kind, Name, Traits, Deliver) in its constructor; Definition.Surfaces() is
   DERIVED from that list. Home/Container/Transcripts stay declared (not derivable).
2c. ENGINE PORT, CORRECTION 2 — NO Declared[T]: it leaks capability inspection into
   core. Null Object instead: every engine implements every port method; an
   engine lacking a capability provides a NO-OP implementation that REFUSES
   LOUDLY when invoked (ErrUnsupported{Engine, Capability}) — never a silent
   success — so "native surfaces or fail loud" holds at the point of use, by the
   engine's own method. Home()/Container()/Transcripts()/Resume() are methods on
   the engine (mock: empty home spec, vendorless container spec, no readers,
   ErrUnsupported on resume). Core never branches on Present.
   Refinement: refuse loudly ONLY where the capability is required for the real
   operation being performed (Resume when a resume is requested; Drive when
   structured chat is demanded); where absence is harmless the no-op genuinely
   does nothing (Transcripts() empty; Home() nothing to seed).
4. LAUNCH — RATIFIED as C draws it: one launch.Resolve, one Launch, one
   runner.Execute; refuses (never substitutes) without harp/home/plan/mode or on
   ownership mismatch; identity enters Resolve; the six pb.RunStart literals and
   seven carriers collapse; container mode = the same runner as the container's
   foreground process via coord/spawn.StartRunner, mounts/env as carriers; the
   go-plugin arm deleted with its eleven importers replaced; reach-back typed on
   Launch.ReachBack, set by the spawner.
5. CONFIG LIFECYCLE + ENDPOINT — RATIFIED: config.Open once per process at the
   composition root; Owner.Current() immutable Snapshot{Config, Catalog, Trust,
   Generation}; Update write-through; Reload the only re-read at three sites
   (post-pull, post-scaffold, once per spawn); memoized config.Load, LoadFresh and
   the shared bundles.Loader deleted. MCP endpoint minted once per session in
   Resolve, bound by the runner; runner lifetime = session; one-shot turns are
   frames to the live runner; idle reaper with delegation.idle_timeout (default
   still open — C's ruling 9).
6. BUS — RATIFIED as C draws it: coord.Verbs over the proto request types with
   Validate(); one runtime coordinator; one spoolInbox per side; MCP inside the
   runner on an authenticated loopback TCP endpoint; shim/forward/marker tier
   deleted; Verbs.Host arms so host-relayed tools are served from the runner's
   config owner against the caller's project; container mail via the MOUNTED
   session dir (push verb rejected); the reach-back gate must prove reach-back
   THROUGH the runner.
2d. Declared[T] SURVIVES NARROWLY: nullable/non-existent capabilities ONLY where
   the system continues to operate without them (a structured driver, vendor
   transcript readers). Never for anything required for real operation — those
   get a real implementation or a loud refusal (2c stands).
7. DELIVERY + MATERIALIZE — RATIFIED; reroot = REFUSE (ErrUnrootable with the
   remedy, never substitute), with the acknowledgement that the ROOT itself may
   legitimately be the SHARED (unsafe) location when the binding selects it —
   unsafe is a selectable root, not a fallback. Writer-tagged single ownership
   record; materialize = the same static writer with the project root as target,
   the one sanctioned human-invoked project-root writer; uninstall = empty plan.
   (Closes C's open ruling 1.)
8. EXEC TRUST GATE — (a) YES: default flips to WITHHOLD; listing/review surfaces
   opt into an explicit Ungated() by name. (c) YES: one verification policy for
   ingest and exposure. (b) NO: a project-local bundle whose signature is INVALID
   stays admitted-as-unsigned (treated as absent, not as a refusal). Behaviour
   change for 0.7.0: unapproved executables are withheld until reviewed.
   (Closes C's open ruling 2.)
9. SIGNATURES — ONE per bundle: the .sigs/ manifest signature; the sibling
   bundle.yaml.sig is deleted; ONE verifier for every reader; re-signing replaces
   the manifest entry; re-init/re-sign is the upgrade path. (Closes C's ruling 3;
   settles unsigned-marine's direction.)
8b. RATIONALE TO DOCUMENT for ruling 8(b) (invalid LOCAL signature stays
   admitted-as-unsigned): a project-local bundle lives in a location the human
   already controls — source control or their home — so it is the LOCAL
   PROTOTYPING path: an author editing a bundle in place breaks its signature on
   every keystroke, and refusing it would make local iteration impossible.
   Locality is the trust boundary there; the signature is for what travels.
   Write this into docs/trust-model.md and the exec-gate ADR, not only here.
10. EXPORTS — AMEND ADR 0020: bundle items carry per-engine exports as OPAQUE
   blocks keyed by engine name, decoded by Engine.Exports(items) against the
   engine's own ExportSchema; core never reads inside; bundles.LLMExports' typed
   engine fields migrate once. (Closes C's ruling 4.)
11. SMALL RULINGS — RATIFIED: (5) one explicit Reload per spawn; edits take effect
   on the NEXT spawn. (6) reaper clock = whole-session newest mtime excluding the
   harp dir's own mtime and symlink mtimes; --include-persist TAKES transcripts.
   (9) delegation.idle_timeout default 15 minutes. (Closes C's rulings 5, 6, 9;
   settles boned-monoxide's clock and include-persist questions.)
8c. SCOPE NOTE on 8(b): "local" = where a local bundle exists today (the project,
   under source control). Do NOT extend to "home" unless home-local bundles are
   actually a thing — verify before the doc slice writes it.
12. SECURITY PAIR — CONFIRMED: (7) deceased-yoga Transport B: a WRITTEN RULING for
   0.7.0 that same-host bridge traffic (loopback or docker bridge, same user) is
   bearer-authenticated and unencrypted; the threat model is recorded on the
   row; mTLS is a later slice. (8) lunar-boat item 1: the launch CONTEXT scopes
   PREPARATION AND ATTACH ONLY; ownership transfers to the run record at attach.
   Cancel before attach -> abort the prepare and remove what it created (no
   orphan). Cancel after attach -> ignored; the ctx is never the teardown handle
   (TestStartDirectRunner_ContextIsNotTheTeardownHandle stays the contract).
   Teardown has ONE door: agent_stop / terminateRun / the idle reaper / the
   runner's own exit. A single cancelled call never tears down a running
   container. C's StartRunner(ctx) keeps ctx with this narrowed meaning; the
   design-by-test body pins both halves.
   -> All nine of C's open rulings are now closed; the skeleton, engine port,
   package/transport, launch, config+endpoint, bus and delivery are ratified.
2e. FINAL on the engine port — Declared[T] IS ELIMINATED ENTIRELY; DRY by
   construction:
   - The engine registers []present.Approach (Kind, Name, Traits, Deliver) in its
     constructor — the ONE source. Definition.Surfaces() and Carries(kind) are
     DERIVED views over it; nothing is stored twice.
   - delivery.Route picks, per item kind, an approach from Surfaces()[kind] that
     matches the preference and can root in the cell. No approach for a kind the
     run NEEDS -> ErrUncarried{Engine, Kind} (loud); an optional item -> routed to
     nothing and listed in Plan.Losses.
   - The other formerly-optional capabilities are SLICES, empty = none:
     Drivers() []StructuredDriver, Transcripts() []TranscriptReader; the caller
     that requires one refuses loudly (ErrUnsupported). Resume() is a real
     implementation or ErrUnsupported. No capability flag anywhere in core.
   - Mock registers observable no-op approaches for the same kinds (records what
     it would have delivered — the conformance loop checks it).
2f. NOTE, NOT A RULING (human, 2026-09-19 early): "I suspect we may want to use
   the builder pattern for instancing an engine / declaring" — i.e. an engine
   package's constructor may be a builder that accumulates approaches, specs and
   session specifics and validates on Build() (one place to refuse an incoherent
   declaration). Do not implement now; the engine slice's design-by-test body
   should try it against the plain constructor and keep whichever reads better.
2g. DECIDED: engine.Definition has ONE TYPED FIELD PER SURFACE KIND (Settings,
   Hooks, Commands, Skills, Context, MCP — each its own approach interface type),
   NOT a []present.Approach slice: a kind that does not exist cannot be declared
   and a kind cannot be declared twice — compile-time, no runtime engine.Validate
   for kinds. Surfaces() is derived by walking the typed fields; nil = not
   carried. Requiredness (e.g. Context on an engine that must carry a system
   prompt) is checked LOUDLY at Instance(), not at compile time. The typestate
   builder (compile-time requiredness) is REJECTED as non-obvious machinery;
   recorded only as the fallback if requiredness must ever move to compile time
   (the spike-typestate worktree is prior art to read before that).
2h. present.Approach is a MARKER interface (possibly carrying a few shared
   parameters — Name(), Traits()); each per-kind approach interface extends it.
   The typed field per kind permits EXACTLY ONE approach of each kind; for the
   REQUIRED kinds that means one is present (nil is the refusal at Instance()).
2i. Required kinds: exactly one approach each. OPTIONAL kinds exist (dynamic/MCP
   among them) and may be nil. A constructed engine may DELINEATE how it
   compounds static and dynamic delivery — e.g. static delivery for the
   non-pretext items and dynamic (MCP-served) delivery for the pretext (the
   assembled context) — and the dynamic half is OPTIONAL: an engine with no MCP
   approach receives everything statically. delivery.Route reads that
   delineation from the definition; it is not a core policy.
2j. ENGINE ROOT TYPE: a base type every engine EMBEDS (engine.Base / the root)
   owns the COMMON decisioning so it is written once. Example ruled: fragments
   with a PREFACE (a premise; the conditional fragments) are WITHHELD from static
   delivery when the engine has a dynamic approach, because dynamic delivery of
   fragments exists; the root's delivery step DELEGATES — non-preface items to
   the static approach types, preface items to the engine's provided dynamic
   approach. With no dynamic approach, everything goes static. Per-engine
   structs override nothing of this; they supply approaches.
