# 21 — Adversarial review of 20-target-architecture.md

Reviewer brief: `briefs/brief-adversarial-review.md` (A1–A10, including the three late sharpenings: re-derivation at every hop, the config lifecycle, raw-vs-container composition and reach-back, one MCP endpoint per session). Repository read-only at `release/0.7`; every counterexample below was checked in the tree with `git grep` and by reading the cited declarations. Citations are `package.Symbol` and file; the design is quoted verbatim where refuted.

## Attack list and outcomes

| Line | Attack | Outcome |
|---|---|---|
| A1 | ruling compliance through the signatures | REFUTED ×5 (R6, R7, R9, R17, R10); the rest HELD |
| A2 | data-flow holes, re-derivation, config lifecycle | REFUTED ×5 (R1, R2, R3, R8, R12) |
| A3 | duplication under new names | REFUTED ×3 (R20, R21, R22) |
| A4 | the eight hard cases | (a) (c) (d) (e) fall through; (b) (f) (g) (h) carried with notes |
| A5 | migration order + the three test bodies | REFUTED ×3 (R13, R16, R23) |
| A6 | enforcement R1–R9 | REFUTED ×2 (R10, R11's rule half); gaps listed under R24 |
| A7 | cost and risk vs the release blockers | one gap (R25) |
| A8 | the ten rulings asked | four re-statements needed (section 7) |
| A9 | raw vs in-container; reach-back per slice | composition HELD with one defect (R18); reach-back REFUTED (R11) |
| A10 | one MCP endpoint per session as a typed launch input | REFUTED (R2) |

---

## 1. VERDICT

The hexagon, the composite package, the one launch value and the one verbs layer are the right shape and most rulings are honoured by construction. The design does not hold as written at exactly the boundary it exists to fix, the process boundary: the `Package` it calls "proof by type" cannot cross the wire, the MCP endpoint is minted after launch inside each runner instead of being a per-session launch input, config has no single owner with an invalidation contract, and a containerized child can neither receive mail nor be told which coordinator address to dial. Before it is built: (1) move `PlanFor` to the originator and make `delivery.Plan` the wire value, `Package` never leaving the originator; (2) mint `MCPEndpoint` per harp in `Resolve` and carry it in `Resolved`; (3) replace `Deps.Settings`+`Deps.Catalog` with one `Library` owner with `Current`/`Invalidate`; (4) add `RunnerLink.Deliver` and a typed `launch.Reach` per runtime axis; (5) drop the "exactly two depths" type claim and give `Resolve` a resume arm; (6) fix S1's live-caller deletions and name a container reach-back journey as the gate of S8–S11.

---

## 2. REFUTED (ranked by how much of the design each invalidates)

### R1 — The `Package` cannot cross the wire; "proof by type" ends at the process boundary (A2, A1 verified-before-delivery, A4-a/b)

**Claim.** 1.3: "holding a `*Package` IS the proof that trust was decided — there is no flag to forget". 1.5: "It is consumed by every tail … and the runner on the far side of either wire." 2.1 draws `RSLV -->|PASSED| PKG --> RSRV` while `Resolved` itself goes `-->|WIRE| W1 --> RSRV`. 2.2 trunk: "RN->>DLV: PlanFor(Resolved.Package, …)". Test 3: `launchtest.RequireEqualResolved(t, r, back2)` after `runnerlink.DecodeStartRun`, and `require.NotNil(t, sr.GetHarness().GetManagedConfig(), "StartRun carries the loadout")`.

**Counterexample.** The runner is a separate process on both tails (`ctxloom runner` as a host subprocess, or as the container's main process). A `*composite.Package` "has unexported fields and exactly one constructor (`Catalog.Assemble`)", which takes a non-nil `*TrustContext`. So on the runner side one of two things is true: either `pluginwire.Decode`/`runnerlink.DecodeStartRun` reconstruct a `*Package` from bytes, which is a second constructor living in two adapters, after which "holding a `*Package`" proves only that a codec ran and `Resolved.Trust() composite.Proof` is a flag on a proto (the brief forbade "a flag on `*config.Config`"; this is the same flag one hop later); or no `Package` exists in the runner, and `PlanFor(Resolved.Package, …)` cannot run where 2.2 places it. Today's wire confirms which one the design actually means: `llm.proto` `RunStart.managed_config` is a `ManagedConfig` (commands, hooks, `bundle_mcp`, skills, deny_tools), a loadout, and the design's own 1.5 says `StartRun` "carries the loadout" as `managed_config`. The same hole swallows `Dynamic.Serve(ctx, plan)`: the runner-hosted MCP server needs the fragments' exact bytes and the premise catalog, which are `composite.Item`s with an unexported `payload`.

**Why it breaks.** The design settles F1/F4/F10 by "there is one `Catalog` and one `TrustContext` per originator". That is true only if decisions never leave the originator. The moment the runner re-plans from a decoded `Package`, the runner is a second decider with no `TrustContext`, and the design's guarantee "a `*Catalog` has no route to delivery" has a route through the codec.

**Smallest change.** Run `delivery.PlanFor` in the ORIGINATOR (it is a pure function of `Package`, `Declaration`, `prefs`, `Target`, all originator-side) and make `delivery.Plan` the wire value: `Resolved.Plan() *delivery.Plan` replaces `Resolved.Package()`; `Plan.Static[i]` carries the per-kind `Inputs` (exact decided bytes), not a `Built engine.Approach`, and the runner constructs approaches through `Declaration.Construct(name, inputs)`; `Plan.Dynamic` carries `[]delivery.Served{Ref, Bytes, Premise, Links}` rather than `[]composite.Item`. `Package` never leaves the originator. Add rule R10: `composite.Package`, `composite.Item` and `composite.Catalog` are referenced by no package under `internal/adapters/pluginwire` or `internal/adapters/runnerlink` (a proto may not name them). `Resolved.Trust()` goes; the `Proof` is rendered by the originator.

### R2 — The MCP endpoint is an OUTPUT of `Dynamic.Serve`, minted per runner process, not a per-session typed launch input; `PlanFor` cannot write it (A10, A2 ordering, A4-a)

**Claim.** 1.4: "`Served.Endpoint() MCPEndpoint // URL + bearer the engine's .mcp.json names`"; "`PlanFor(pkg, eng, decl, prefs, target)`" with no endpoint. 2.2: step 22 `Static.Deliver → … .mcp.json into the SESSION HOME`, step 23 `Dynamic.Serve → mcpserve on authenticated loopback TCP`, step 24 "endpoint written into session-home .mcp.json". 2.3: "`MCPInputs{Servers: pkg.MCPServers() + the runner's own endpoint entry}`". 1.7: "the MCP server is an adapter INSIDE the runner (`mcpserve.Serve(runner.Session, plan, verbs)`)".

**Counterexample.** (i) Ordering: `.mcp.json` is a static item constructed at `PlanFor` time from `MCPInputs`; the endpoint does not exist until `Dynamic.Serve` returns, which 2.2 runs AFTER `Static.Deliver`. Step 24 is therefore a SECOND write of `.mcp.json` outside the plan, outside `Approach.Deliver`, and outside the ownership record, which is the "three ownership mechanisms" defect the design deletes. (ii) Scope: the endpoint lives in the runner process. A one-shot child (`ResumeModeOneShot`: "a new run_id per turn", coordinator notes) gets a NEW runner per turn, so a new listener, a new bearer and a rewritten `.mcp.json` per turn. The human's ruling is "one endpoint per session". Today's shape is the same (`coord.EnvMCPSocket` is exported per runner in `cli/llm_runner_common.go`), so the design carries the defect forward under a new transport. (iii) A container child: the endpoint is minted and written INSIDE the container by the runner, so it reaches the engine by the runner re-deriving it in its own process, not as a typed input that crossed the boundary.

**Smallest change.** Mint the endpoint with the identity: `identity.MCPEndpoint{URL string; Bearer string}` allocated ONCE per harp in `launch.Resolve` (persisted on the `sessiondir.Record` so a resumed run and every one-shot turn reuse it), carried as `Resolved.MCP()` on both codecs; `PlanFor(pkg, eng, decl, prefs, target, ep identity.MCPEndpoint)` builds the `.mcp.json` entry from it; `Dynamic.Serve(ctx, plan, ep) (Served, error)` BINDS the pre-minted port on the runner's own loopback (in a container that is the container's loopback, shared with the engine, so a fixed port is safe) and refuses if it cannot. One endpoint per session, typed on the wire, written once by the static half, and step 24 disappears.

### R3 — Config has no single owner and no invalidation contract; the design deletes hot reload and replaces it with nothing (A2 sharpening 2, A4-c)

**Claim.** 1.5: "`Settings` is the typed project configuration a launch reads. projectfs parses config files into it once per process; nothing reloads." 1.3: "there is one `Catalog` per originator process, refreshed explicitly by `Sync`, never reloaded behind a call." 1.7: "`Spawner` … resolve an agent binding into a `launch.Resolved` (through `launch.Resolve` …)" over the originator's `Deps`. 2.1: `SET["projectfs: config files + overrides → launch.Settings ONCE (originator root)"]`.

**Counterexamples, each a place two readers observe different states or a write races a reader.**
1. `ctxloom init` (hard case c): `operations.Init` scaffolds `config.yaml`, creates the `distiller`/`triage` agents, then launches the discovery session and the auth probe IN THE SAME PROCESS through `launch.Resolve(Source{Label, Mode})` against `Deps.Settings`. With "once per process; nothing reloads", the discovery launch resolves against the pre-scaffold `Settings` (no labels, no agents) and fails with "no engine". Today the same process survives only because `config.Load` re-stats `config.yaml` on every call, and `internal/adapters/cli/init.go`'s own comment records that this stat check "has only pre-write state on a filesystem coarse enough" — the race the sharpening names, already documented in the tree.
2. `agent_run` of a binding created mid-session: `ctxloom agent set` runs as a SEPARATE process (the agent's shell, a second terminal). The originator's `bus.Coordinator` holds the `Spawner` built over the `Deps` captured at startup. `config.Load`'s doc names "hot reload: agent_run re-loads on every spawn so edited agent definitions take effect mid-session" as a requirement; the design deletes the reload (F4 "settled") and offers no replacement, so a new binding is invisible until the originator restarts, and a bad binding name "silently degrades to host+none" per the isolation fragment.
3. `deps pull` mid-session (the `unrevised-backspace` shape): `Index` returns a NEW `*Catalog`; `Deps.Catalog` is a pointer copied into the coordinator's `Spawner` at construction and into every CLI verb's `Deps`. After `Sync`, the CLI verb that ran it sees the new catalog and the coordinator's children keep resolving against the old one. Nothing in 1.3 says `Sync` rebuilds the `Source`s; the defect this morning was a loader that kept pre-pull readers across `Invalidate`, and `Catalog` "refreshed by Sync" is the same memo with a new name unless the contract says what is dropped.
4. Hook verbs: `ctxloom hook *` load config in their own process (N12 allows `root.go`). A `SessionStart` hook fired while a `deps pull` in another process rewrites the lockfile reads a half-written file. No fingerprint, no lock, no contract.
5. The runner "loads no config" but `search_library` needs the remotes' clones (R12): the design's answer will be a relay to the originator, which then serves it from the originator's `Catalog` generation, so a child's `search_library` and its parent's differ by whichever `Sync` ran between.

**Why it breaks.** "Once per process" is the wrong unit: a process is long-lived and config is written under it by the agent itself (`agent set`, `deps pull`, `config set`, `init`). The correct unit is once per DECISION (one `Resolve` reads one snapshot) with an explicit invalidation for in-process writers and a fingerprint for out-of-process writers. The design has neither, so it trades today's re-derivation for tomorrow's staleness.

**Smallest change (the signature the sharpening asks for).**
```go
// internal/core/composite/library.go — the ONE owner of loaded project state per originator
type Generation uint64
type Snapshot struct {
    Settings launch.Settings
    Catalog  *Catalog
    Trust    *TrustContext
    Gen      Generation
}
type Library interface {
    // Current returns the snapshot every reader in ONE decision shares. The adapter
    // fingerprints config.yaml, the lockfile and the bundle roots (stat mtime+size,
    // then content hash on a tie); a changed fingerprint rebuilds Sources, Catalog and
    // TrustContext under one lock and bumps Gen. Cheap when unchanged.
    Current(ctx context.Context) (Snapshot, error)
    // Invalidate is called by every in-process writer after its fsync+rename (Save,
    // Sync, agent set, config set, init's scaffold). Out-of-process writers are caught
    // by the fingerprint at the next Current.
    Invalidate(reason string)
}
```
`launch.Deps` takes `Library`, not `Settings`+`Catalog`+`Trust`; `Resolve` calls `Current` exactly once and threads the `Snapshot` value (two reads inside one `Resolve` become impossible because there is one value); `projectfs.Save` calls `Invalidate`; rule: `projectfs.Load` is referenced only from the `Library` implementation. The hook verbs and the runner never hold a `Library`.

### R4 — One `Ownership` per target sidecar cannot hold two owners (A4-e, A1 materialize)

**Claim.** 1.4: "`Ownership struct { Owner OwnerKind // OwnerSession(harp) | OwnerAtRest; Entries map[string][]string }`"; "the static adapter persists it as ONE sidecar per target (`<target>/.ctxloom-managed`)"; "reconcile-to-declared (install = uninstall = the same call with an empty plan)".

**Counterexample.** A project root that carries session X's `unsafe-file` `.mcp.json` entries (the explicit `RootProjectRoot` selection the design permits) and then a human runs `materialize`. Both plans target `AtRestTarget(projectRoot)`/`SessionTarget` roots that resolve to the same file and the same sidecar; the record has ONE `Owner`. The second writer's reconcile-to-declared removes every entry not in ITS plan, so it deletes X's live entries mid-session, or it overwrites the sidecar and X's `Delivered.Cleanup` later removes the human's. Two concurrent sessions both selecting `unsafe-file` into one project root hit the same collision with no human involved.

**Smallest change.** Per-entry ownership: `Entries map[string][]Entry` with `Entry{Key string; Owner OwnerRef}` where `OwnerRef{Kind OwnerKind; Harp identity.Harp}`; reconcile removes only entries whose `Owner` equals the plan's; `Delivered.Cleanup` likewise. (Test 2 already reads `rec.Owner.Kind`, which only compiles against this struct, see R16.)

### R5 — The runner's `Inbox` sweeps a spool a container cannot see, and `RunnerLink` has no push verb (A2, A4-a, A9-2)

**Claim.** 1.7: "`Inbox` is the ONE parked long-poll … instantiated once by the Coordinator for the owner and once by the runner for its run"; "`NewInbox(spool Spool, harp identity.Harp, sweep time.Duration)`"; runner's `agent_recv` → "`runner.Session.inbox.Recv`". 1.7 `RunnerLink` has exactly `StartRun`, `StopRun`, `Events()`. 1.8 `Members`: "`spool … Mount: MountNone`".

**Counterexample.** Today a container child receives mail by PUSH: `coord.Coordinator` builds an `agentcoordpb.PeerMessage` in `coord/runchannel.go` and the runner's `coord.Home.deliverNotice` completes the parked recv; the runner's own sweep (`Home.sweepSpoolIn`) reads `spool.NewHomeMapper()`, i.e. the container's fresh HOME, which is empty. `isolation/statemounts.go` binds only `persist/` and `persist/transcripts` into the container; the design's `Members` table keeps `spool` unmounted. So in the target a rootless-container child's `agent_recv` parks forever, and hard case (a), a child steering a grandchild in a container, has no channel for the grandchild's replies.

**Smallest change.** `RunnerLink.Deliver(ctx context.Context, run identity.RunID, m *Mail) error` (originator → runner push, the `PeerMessage` frame that exists today) and a runner-side `Inbox` fed by the link (`Inbox.Push(*Mail)`), with the file spool the DURABLE substrate on the originator side only. The `Spool` port stays; the runner never holds one.

### R6 — "Exactly two depths" is a comment on an `int`, contradicts a configurable cap and a pinned test, and the design's own `IsLeaf` admits more (A1, A4-a)

**Claim.** 1.6: "`Depth int // 0 = the originator's own session; 1 = a spawned child. There are exactly two.`" 3.3 honoured: "the four-level flat topology; two agent depths … `identity.Session.Depth ∈ {0, 1}`". 4.3 #8: "the target's `Session.Depth ∈ {0,1}` makes it a type."

**Counterexample.** `config.DefaultDelegationDepth = 1` is the DEFAULT of `delegation.depth`; `coord.Coordinator` (children.go) refuses at `caller.Depth >= depthCap` with the remedy "raise `delegation.depth` in config.yaml if a deeper tree is actually wanted"; `TestAgentRun_GrandchildAllowed` (`coord/conformance_test.go`) pins that depth 2 lets a child spawn a grandchild through the same path; the release-blocker row `womanless-quarterly` records "A depth-1 child steering its grandchild is tested end to end." The design's own signature `Session.IsLeaf(limits DelegationLimits) bool // OneShot || Depth >= limits.Depth` and `Settings.Delegation DelegationLimits{Depth, Concurrency}` admit any depth. Nothing is a type here: `Depth` is an `int`.

**Smallest change.** Delete the "exactly two" sentence, the 3.3 row's `∈ {0,1}` and 4.3 #8; state the ruling as "the DEFAULT cap is 1; `IsLeaf(limits)` is the one rule". If the human actually wants two depths as a type, `delegation.depth` and `TestAgentRun_GrandchildAllowed` must be deleted in the same slice and the release-blocker row re-scoped; the design must ask, not assert.

### R7 — `Resolve` mints unconditionally: no constructor path for a resumed run, and `Resolve` has uncompensated side effects (A1 every-run-mints, A4-d)

**Claim.** 1.5: "Resolve is the ONE constructor: Source → Settings ladder → identity mint → …"; `Source` has `Resume engine.ResumeRef` and no harp; `Minter.Mint(ctx, project, engine) (Harp, error)`. 1.6: "a resumed harp gets a new RunID, the same Harp." 1.7: `armResume(harp, delay)`; `Spawner.Resolve(ctx, caller, agent, src)`.

**Counterexample.** After a coordinator restart, `armResume(harp)` must relaunch the child for the EXISTING harp with `HarnessSpec.resume_session_id` (the `StartRunResult` doc: "feeds it back … when the same harp's next run attempt starts (a fresh run_id; the harp is what stays continuous)"). Through `Spawner.Resolve → launch.Resolve` the only identity arm is `Minter.Mint`, so the resumed run gets a NEW harp: a new session dir, a new spool, a new transcript lineage, and the parent's mail addressed to the old harp is orphaned. The originator's own `ctxloom run --resume` has the same problem. Separately, `Resolve` performs `Minter.Mint` (mkdir), `Workspace.Prepare` (worktree materialization) and `Runtime.Prepare` (container preparation) and then may still fail on "a permission the mode cannot honour" or `ErrUnsafeUnselected`; nothing releases what was prepared, and `Prepared.Start(ctx, r *Resolved)` needs a value that does not exist until `Prepare` returned.

**Smallest change.** `Source.Resume ResumeRef{Harp identity.Harp; NativeSession string}`; `Resolve` mints only when `Resume.Harp == ""`, else `Store.Find(harp)` + fresh `RunID` (still one constructor; two typed identity arms). Split effects from decision: `Resolve` is pure over `Snapshot`; `operations.Launch` = `Resolve` → `Workspace.Prepare` → `Runtime.Prepare` → `Runtime.Start`, with `Prepared.Release()` on any later failure. The "every run mints a harp" ruling reads correctly as "every run HAS a harp".

### R8 — Host-side facts are handed to the container as if universal: `Binary()` has one path and the host home has no typed carrier (A9-1, A2 re-derivation)

**Claim.** 1.5: "`Binary() Binary // {Path, Args} the ORIGINATOR resolved from its config; the runner never re-resolves`". 1.4: "the real home appears only inside the engine's own `Home` seeding (one-way copy of credentials into the session home at creation)". 1.5 `Runtime.Prepare(ctx, axis, eng, id, work)`.

**Counterexample.** `isolation.defaultContainerBinary = /usr/local/bin/ctxloom` and the engine binary is whatever the image's PATH resolves (`claude.enginecli` declares `Binary: "claude"`); a host path resolved by the originator (`~/.local/bin/claude`) does not exist in-image, so the container runner must `LookPath` (the re-derivation the design forbids) or fail. Credential provisioning today reads the host home: `isolation/auth.go` `hostHomeDir = os.UserHomeDir` to bind-mount `.claude/.credentials.json`; `claude.go` and `claude/mcp_registrar.go` call `os.UserHomeDir()` for seeding. The target's `engine.Roots` has `Project, SessionHome, WorkDir, CtxloomHome` and no host home; `Runtime.Prepare` has no seed source; R8 lists `os.Getenv/LookupEnv/Environ` and not `os.UserHomeDir`, so the adapter will re-derive `$HOME` and the rule will not notice.

**Smallest change.** `launch.Binary{Host, Engine string}` resolved on both sides by the originator (engine side from `Description.Container.BinaryPath` when the axis is a container); `Deps.HostHome string` decoded once in `cli/root.go` and passed to `Runtime.Prepare(ctx, axis, eng, id, work, seed engine.HomeSeed{HostHome string})`; extend R8's symbol list with `os.UserHomeDir`, `os.UserConfigDir`, `os.Getwd`, `os.TempDir`, `os.MkdirTemp`, `user.Current`.

### R9 — The routing rule is a substitution arm under a new name (A1 never-degrades, A1 native-surfaces)

**Claim.** 1.4: "`PlanFor` is the ONE planner. It fails — never substitutes". Then, same section: "If the engine declares no approach for a kind the package has items for and the binding did not ask for that kind, the kind is dropped from the static half and served dynamically where a dynamic form exists (… commands and skills have one: MCP prompts …)". 3.3 honoured row: "`delivery.PlanFor` returns `ErrNoApproach`, never substitutes".

**Counterexample.** A package with commands, an engine whose `Declaration` has no `SurfaceCommands` entry, a binding that names nothing for commands (the common case: `surfaces:` is rarely written). The planner serves the commands as MCP prompts the engine never declared, with no error and no acknowledgement, and `Description.Unsupported[SurfaceCommands]`'s reason is never shown. That is the `reroot` shape (seam 3 F4 site 1) relocated from the writer to the planner, and it contradicts 3.3 #2's recommendation "refuse".

**Smallest change.** Delete the sentence. A kind the package has items for and the engine does not declare is `ErrNoApproach` unless `prefs[kind] == "dynamic"` names the MCP form explicitly, on the design's own rule that "an explicit selection is the acknowledgement". Fragments stay in the dynamic half unconditionally because the engine declares that route (the premise catalog is a resource by design), which is a declaration, not a fallback.

### R10 — R1 and R9 are violated on day one by the design's own core: protobuf in `core/bus`, afero in `core/engine` (A6, A1 hexagonal)

**Claim.** R1: "third-party limited to stdlib and `golang.org/x`". R9: "the generated `pb` packages imported only by their own adapter". 1.7: `agentcoordpb` "generated … into a core-owned Go package". 1.2: "`afero.Fs` is the one third-party type permitted inside the core ring".

**Counterexample.** Every generated `.pb.go` imports `google.golang.org/protobuf`, so `core/bus` fails R1 as written. `internal/core/agent/cells.go` and `approaches_generic.go` already import `github.com/spf13/afero`; their seam half becomes `core/engine`, which fails R1 too. `mcpserve` and `runnerlink` both must import `agentcoordpb` (the verbs' request types), which fails R9 as written. Also missing: `afero.NewOsFs()` is constructible from any package, so a core package can still touch the disk through the "filesystem VALUE" exception; nothing forbids it.

**Smallest change.** Write R1 with its two named exceptions (`google.golang.org/protobuf` for `internal/core/bus/agentcoordpb` only; `github.com/spf13/afero` for `core/engine`, `core/delivery` only), R9 with the allowlist {`core/bus`, `adapters/runnerlink`, `adapters/mcpserve`}, and a symbol rule: `afero.NewOsFs`/`afero.OsFs` referenced only in `adapters/staticfiles`, `adapters/sessionstore`, `adapters/projectfs` and the roots. Note for S2: `archlint.layeringRule` today has only `From/Forbid/Allowed`; the `only:` form does not exist and must be built before R1/R2/R4 can be stated (see R23).

### R11 — Reach-back from a container has no home for its endpoint selection and no test in any slice (A9-2)

**Claim.** 1.6: "`CTXLOOM_COORD_URL`, `CTXLOOM_COORD_CRED` … stamped by the originator, onto the RUNNER process (host subprocess env or container env)". 1.7: "the runner reaches the originator through `CTXLOOM_COORD_URL`/`CRED` stamped by the originator"; `Credential.Env(coordURL string)`; `Spawner.Start(ctx, r, cred)` has no URL. 4.1 gates: S9 "an engine started by `run` reaches `agent_run`"; S10 "N3 … container identical delivered set"; S11 "the E3 acceptance journey (container interactive)".

**Counterexample.** Today `coord.Coordinator.ReachURL(runtimeAxis)` (`coord/httpserver.go`) returns the loopback URL for host runners and, for EITHER container ownership mode, `ensureWide()`: on Linux a bridge-gateway/primary-outbound listener opened on demand, on darwin/windows the VM's magic hostname. In the target, `bus.Coordinator` is core (no `net`), `runnerlink.Server` owns the listeners, `adapters/isolation` knows the axis, and R3 forbids adapters importing each other; no signature in Part 1 computes the container-reachable URL, and `Spawner.Start` cannot receive one. The proof gap: `j002400_container.feature` asserts only that "the engine left its record" via the workspace mount, never an `agent_send` from inside a container; `j002300` proves the bus on the host. So reach-back from a container has no acceptance journey today, and none of S8 (mounts change), S9 (endpoint moves), S10 (`RunnerLink` re-typed), S11 (container process shape) names a test that would go red if it broke.

**Smallest change.** `runnerlink.Server.Reach() launch.Reach{Host, Container string}` (the widening stays in the adapter, on demand), threaded by `cli` into `Spawner.Start(ctx, r, cred, reach launch.Reach)`; `Runtime.Start` picks `reach.Container` for a container axis and stamps `Credential.Env(url)` on the container env — a typed input crossing the boundary. Add journey "a `container-rootless` child calls `agent_send(to: parent)` and the originator's roster and inbox show it" as a named gate of S8, S9, S10 and S11.

### R12 — The context and search tools cannot be served by a config-free runner; the relay list is incomplete (A2 hop)

**Claim.** 1.1 runner: "config files (never loads one)". 1.1 mcpserve: "tools (context, search, memory, triggers, status, `agent_*` over `bus.Verbs`)". 1.4: "`Dynamic.Serve(ctx, plan)`". 1.7 item 5: "the seven relayed host tools (compact, load, recover, previous, list, triggers, status)".

**Counterexample.** `internal/adapters/mcp/mcp_tools_context.go` serves `assemble_context`, `search_content` and `search_library` from `s.cfg` (`operations.SearchRemotes` reads the remotes' clones; `operations.AssembleContext` takes arbitrary bundle names). `Plan.Dynamic` carries only the selection's fragments, so `assemble_context(bundles=[a name outside the selection])` and `search_library` have no data in the runner, and the design neither relays them nor carries a catalog view.

**Smallest change.** Add the three to the typed `Custom` relay (the originator holds the `Catalog`), and say in 1.7 item 5 that the relay set is "every tool that reads the Catalog or the sessions root", derived, not a list of seven.

### R13 — S1 is not a pure deletion: three of its targets have live production readers (A5)

**Claim.** 4.1 S1: "Pure deletions … Introduces nothing"; trips "wire (reserving fields nothing reads)"; unattended "yes".

**Counterexample.** `coord.CapPeerMessaging` is read in `coord/runchannel.go` (the `RunnerHello.Capabilities` literal) and `coord/home.go` (the runner's declared capability set): deleting it changes the hello frame's capability set on the wire, and capability negotiation is live (`womanless-quarterly`: "runchannel reads hello.GetCapabilities into the per-run set"). `agent.ChatRequest.ForwardPermissions` has a live codec (`lm/grpc/chat.go` both directions), a live reader in `backends/mock_chat.go` and a producer in `coord/harnessspec.go`. `remote.NewCachingBundleReader`/`LoadAllBytes` are live in `config.remoteBundleReaders`, whose `failures` map feeds `treeBundleReaders` and `reportBundleLoadFailures`: deleting the chain changes which load failures the user is shown unless the tree readers report the same set, which no gate in S1 asserts. (`CompactionConfig.IncludeThinking` has a live reader in `memory.Compactor` but no production setter: safe.)

**Smallest change.** Move `CapPeerMessaging` to S10 (with the capabilities rework), `ForwardPermissions` to S7 (with the plugin-wire codec), and give the reader-chain deletion a gate: "a fixture whose remote fails to fetch produces the same `reportBundleLoadFailures` output before and after".

### R14 — Hard case (c), `ctxloom init` on a machine with no project config, falls through R3

Covered by R3 counterexample 1. The concrete run: `operations.Init → launch.Resolve(Source{Label, Mode})` reads `Deps.Settings` built "once per process" before init wrote anything. The `Library` signature in R3 closes it; without it there is no signature through which init's discovery launch can see the config init just wrote.

### R15 — `Resolve`'s failure contract omits "mode not in `Description.Modes`" (A4-h)

**Claim.** 1.2 `Chat` doc: "An engine that cannot drive a structured chat returns `ErrUnsupportedMode` from `Describe().Modes` — the runner refuses at resolve, not here." 1.5 `Resolve` "fails on: no engine, a binding naming an undeclared surface, an unsafe surface …, a permission the mode cannot honour …, a runtime ownership mismatch …, a mint failure …, a workspace refused …" — no mode check.

**Counterexample.** A codex agent (`Modes = {interactive, oneshot}`) named by `agent_run`: `Verbs.Run` → `Spawner.Resolve` with `Mode = chat` succeeds by the stated contract; the failure surfaces in the runner at `engine.Chat`, after a harp was minted, a session home delivered and an MCP server started. Fail-loud is honoured; fail-early is not.

**Smallest change.** Add "a `Mode` absent from `Description.Modes`" to `Resolve`'s failure list, with `bus.Verbs.Run` choosing `ModeOneshot` when `Description.Resume == ResumeBySessionID` and chat is absent, else refusing with the engine's words.

### R16 — The three design-by-test bodies do not compile against Part 1 as written, and two assertions are vacuous (A5)

Read as a compiler would:
- Test 2: `rec.Owner.Kind` against `Ownership.Owner OwnerKind` (an enum per 1.4) does not compile; it compiles only against the struct R4 needs. `unsafe := delivery.Plan{}; _ = unsafe` is dead code. `require.True(t, afero.IsNotExist(err) || true, …)` is `assert(true)`; the design admits "the exact predicate is the slice's to sharpen", which is the vacuous-assertion pattern the project's mutation gate exists to catch. `staticfiles.ReadOwnership` is undeclared in 1.4 (acceptable as a helper, but name it).
- Test 3: `d.ForChild(identity.Session{…})` — `launch.Deps` has no `ForChild`; `launch.ErrNoHarp` — `Resolve` declares no error variables; `r.Permission().SafeHeadless()` — `engine.Permission` declares no method; `launchtest.RequireEqualResolved(t, r, back2)` requires `Package` equality across a codec, which is R1. `require.NotNil(t, sr.GetHarness().GetManagedConfig())` asserts the loadout rides as `managed_config`, contradicting 2.2's `PlanFor(Resolved.Package …)` in the runner.
- Test 1: `require.NotNil(t, a.Hooks)` is vacuous if `HookCodec` is a struct (1.2 does not say); `engine.Presentation{Kind, HostPath, Args}` adds a `Kind` field today's `present.Presentation` lacks (fine as a proposal, but 1.2 says `present.*` moves "unchanged in shape except …" and does not list `Kind`).

**Smallest change.** Declare `Deps.ForChild(caller identity.Session) Deps`, `launch.ErrNoHarp`, `engine.Permission.SafeHeadless() bool`, `Ownership.Owner OwnerRef{Kind, Harp}`, `Presentation.Kind`; delete the tautology and the dead line; state whether `HookCodec` is an interface.

### R17 — `Plan` and `Target` zero values admit the state the prose says cannot be built (A1)

**Claim.** 1.4: "A session target REQUIRES a session home root: … the value cannot be built." `Plan` has exported fields including `Target Target`.

**Counterexample.** `static.Deliver(ctx, &delivery.Plan{Static: items}, fs)` compiles with a zero `Target` (empty roots, zero owner) and writes items under `""`. The constructor refuses; the type does not.

**Smallest change.** `Plan` with unexported fields and accessors (`PlanFor` the only constructor), or `Static.Deliver` validating `plan.Target.valid()` and returning `ErrNoSessionHome`; test it with a mutant that zeroes the target.

### R18 — The `Launcher` port's second implementation re-creates the engine-launch tail the ruling forbids (A9-1)

**Claim.** 1.2: "`Launcher` is the port an engine uses to start a process. Implemented by `shared/ptyrunner` (host) and by the container runtime adapter." 1.1 isolation row: "`ctxloom runner` becomes the container's main process".

**Counterexample.** If the runner is the container's main process, the ENGINE is started inside the container by the same runner through the same `ptyrunner`; a container `Launcher` in `adapters/isolation` means the engine process is started from the host by `docker exec`, which is the E3 keepalive shape the same row deletes and the seam 1 F1 divergence A9 forbids. The rest of A9-1 HOLDS: `Runtime(container).Start(Resolved)` wraps `runner.Serve(Resolved)`; the raw launch's inputs cross as the `Resolved` codec (wire), `Credential.Env` (env) and `sessiondir.Mounts` (mounts); host and container run the same `PlanFor → Deliver → Serve → Command → Turn/Chat`.

**Smallest change.** One `Launcher` (`ptyrunner`), used by the runner wherever it runs; `adapters/isolation` starts RUNNERS and never engines. Delete the second implementation from 1.2.

### R19 — Hard case (b), a distill one-shot from an MCP tool while the owner is mid-turn: carried, with a cost the design does not state

`compact_session` relays to the originator (1.7 item 5) → `operations.Compact` → `Launch` → `Resolve(Source{Agent: "distiller", Mode: oneshot})` → a host runner subprocess over `pluginwire`, concurrent with the owner's pty `Turn`. Structurally sound: the distill has its own harp, session lock and session home. Not stated: every distill and triage now pays a full static delivery, an MCP listener and a session-lock acquisition (3.3 #1 lists "each distill pays a session home" and omits the listener), and under R3 the distill resolves against whichever `Library` generation is current, not the owner's. Not a refutation once R1–R3 land; recorded for A7.

### R20 — Two mint ports for one act: `identity.Minter.Mint` and `sessiondir.Store.Mint` (A3)

**Claim.** 3.1 B2 row: "two harp mint entry points → `identity.Minter.Mint`". 1.6: `Minter.Mint(ctx, project, engine) (Harp, error)`. 1.8: `Store.Mint(ctx, project, engine) (Record, error)`.

**Counterexample.** The same signature, two ports, two packages; the design deletes "two harp mint entry points" and declares two. Whichever adapter implements both must keep them coherent (mkdir-uniqueness in one, the record in the other).

**Smallest change.** Delete `identity.Minter`; `launch.Deps.Store sessiondir.Store` and `Resolve` calls `Store.Mint`, reading `Record.Harp`.

### R21 — The native session id lives in two records under two names (A3)

**Claim.** 1.7 `bus.RunRecord`/`Journal` facts carry the child's native session for resume ("the coordinator JOURNALS this (run.harness fact)"); 1.8 `sessiondir.Record.NativeSession … bound on first hook` and `Store.Bind`.

**Counterexample.** One fact, two writers (the hook verb through `Store.Bind`; the coordinator through `Journal.Append` on `RunExited{NativeSession}`), two readers (`armResume` reads the journal; `session list` reads the record). They can disagree after a crash between the two writes; nothing reconciles them.

**Smallest change.** The journal fact references the harp only; `NativeSession` lives in `sessiondir.Record`; `armResume` reads it through `Store.Find`.

### R22 — `operations` as one package is the cli/operations smear with a new label (A3)

**Claim.** 1.1: `operations` "absorbs `internal/adapters/operations` minus what the rows above take, `internal/adapters/memory`'s orchestration, the cli-resident orchestrators (`doctor` ×35 checks, `deps check`/`reconcile`, `review` walk, `util config-write`, gitignore reconcile, `classifyHarpWorktrees`, `offerItemTrust`/`offerBundleTrust`, the six `ResolveLocalSigner` copies → one)". 3.1: "orchestrators MOVE in from cli (net neutral for those)".

**Counterexample.** Today `internal/adapters/operations` is 29.0k lines and `cli` 30.4k (the design's own figures); after S13 one package holds launch, materialize, compaction, review, sync, doctor, deps, hooks, init, sessions, gitignore, worktree sweeps and signer resolution. R2 (`operations → core only`) is a ring rule, not a cohesion rule; nothing stops `operations.Doctor` importing `operations.Launch`'s internals, which is the smear the synthesis B1/ML-H measured. `operations.Launch` is also two use cases wearing one name: the CLI needs resolve+start+drive, the `Spawner` needs resolve and start separately (1.7: "Production is `operations.Launch` over the isolation adapter"), so it is either two exported functions or a flag.

**Smallest change.** `internal/adapters/operations/<usecase>` subpackages (`launch`, `materialize`, `sessions`, `trust`, `deps`, `doctor`, `init`) with R2 applied to the tree and a sibling rule "no `operations/<a>` imports `operations/<b>`"; name `operations/launch.Resolve` and `operations/launch.Start` and let `cli` and `Spawner` compose them.

### R23 — Migration ordering defects (A5)

1. **S6 secretly depends on S8.** S6 deletes `config.ResolveBundleMCPServers`, `extractHooksFromBundle`, `operations.AssembleContext` "→ `Package.MCPServers()`/`Hooks()`/`Context()`", but their consumers (`lm/backends/managed.go` loadout assembly, `cli/run.go`'s R6–R19) are replaced only by `PlanFor` in S8 and `Resolve` in S7. S6 either lands with an unnamed interim (`Package → ManagedConfig` bridge) or after S8. Name the bridge in S6 or reorder S6 after S7/S8 with `Resolve` taking today's gate until then.
2. **S10 breaks container children's mail** (R5): replacing `Home.Recv` with `Inbox` over `Spool` inside the runner parks every container child. S10's gate (N3, delivered-set equality) does not measure mail. Add `RunnerLink.Deliver` in S10's first commit and a gate "a container child receives a parent's `agent_send`".
3. **S2 is not unattended-safe as stated.** It introduces a new analyzer form (`only:` allowlists do not exist in `archlint.layeringRule`) and nine rules whose first allowlists are the entire tree; a new analyzer's first output tests its own config. Split: S2a builds the `only:` form and lands with zero rules (unattended); S2b aims R1–R9 with a human reading the first allowlists.
4. **Gate re-aims in S2 point at packages that do not exist until S5b–S13** (`lean_binaries` pinning `internal/engines/claude`, which becomes `engines/claude` in S5b; `ledger_discipline` "one record writer per target", which exists in S8). Move each re-aim to the slice that moves its target.
5. **S7 before S8 delivers the one-shots into today's session home** (`<project>/.ctxloom/state/<harp>/home` until S8 moves it): acceptable as an interim, but the S7 row lists "prompt" and not "on-disk"; say it.
6. **Reach-back has no named gate at S8–S11** (R11).
7. **The `Library` owner (R3) has no slice.** Add it to S3 (type introduction: `Library` wrapping today's `config.Load` memo + `bundles.Loader`, with `Invalidate` called by `Save`/`Sync`) so S6's "one `Catalog` per originator" has an owner when it lands.
8. **`MCPEndpoint` per harp (R2) has no slice.** It is S9's first commit, and it changes `Resolved` (S7's codec test), so S7's `Resolved` must carry it as a reserved field or S9 re-opens the codec.
9. **S1's live callers** (R13).

### R24 — Enforcement gaps in R1–R9 beyond R10 (A6)

- **R7** names `identity.Mint`, which is not a declared function; the act is `Minter.Mint`, an interface method, and archlint's symbol rules key DECLARATIONS (`FuncSymbol`), not calls through an interface. Worse, the real mint call site is `launch.Resolve` (core), reachable from `mcpserve` because the map has `MS --> OPS` and `operations.Launch` calls `Resolve`: a runner-hosted `mcpserve` can mint a harp and start a runner, a second owner. Fix: pin the CONSTRUCTOR of the store's minter (`sessionstore.NewStore`) and `bus.New` to `internal/adapters/cli`; delete the `MS --> OPS` edge (the seven relayed handlers live in `runnerlink.Server` on the originator, not in `mcpserve`).
- **R8** misses `os.UserHomeDir`, `os.Getwd` (the F2 defect's own symbol), `os.TempDir`, `os.MkdirTemp` (`claude.writeChatMCPConfig`'s defect), `user.Current`, and the leaf libraries (`shellenv`, `envswitch`) that call `os.Getenv` by design, so its allowlist is not empty on day one.
- **R3** does not stop `adapters/mcpserve` importing `adapters/runnerlink` for `runnerlink.Client` ("In the runner, `verbs` is `runnerlink.Client`"); the design must say the runner ROOT injects a `bus.Verbs`, which it does not.
- **R6** forbids `cmd/ltk` importing `internal/engines`, but `internal/ltk/engine/claudecode.go` imports `internal/engines/claude` today and the design does not say what ltk's hook install becomes (an item in the ltk bundle delivered by ctxloom, presumably; say it).
- **No rule** forbids a second `Static`/`Dynamic` implementation, a second `PlanFor`, or `composite.Package` on a wire (R1's proposed R10).
- **Gates that go red on the target and the design does not say so:** `lean_binaries` (pins `internal/engines/claude`), `engine_layout_arch_test` (the design deletes it, S14), `vocabulary_adoption` (forbids the harp literal, which R8 subsumes), `tests/arch/layering_test.go` (the design makes it a driver, but every existing row names today's package paths and goes red at S5b, S6, S8, S10, S11 as packages move: each of those slices must carry the row edits, which only S2 mentions).

### R25 — Cost and risk vs the release blockers (A7)

- `varied-tinfoil` (release-blocker, human prerequisite): the fragment-preimage change already invalidates every recorded approval, and the row rules that "anything else needing a preimage bump should ride the same window". S6 introduces `composite.Preimage` as THE preimage; if its bytes differ from `signing.FragmentPreimageContract` "ctxloom-fragment/1" by even the framing, it is a second invalidation window after pre001. The design must state that `composite.Preimage` is byte-identical to the contracts in force (a `preimage_wire_parity` test exists; name it as S6's gate) or land S6 before the install.
- `womanless-quarterly` (release-blocker): the design's `Verbs.Control` covers the `ControlRun` wire pair the row says is missing; the row also says pause/resume "refuse unless `delegation.spool_delivery` is on", and the design retires the spool as the runner's inbound path (R5) without saying what `spool_delivery` becomes. Say it.
- Non-goal 3 (container reuse): with R2 unfixed, a one-shot container child pays a container start AND a new MCP listener per turn. With R2 fixed the endpoint is stable and reuse becomes a scheduling change, as 4.3 claims.
- LOC: the `cli ≈ −6,000` and `operations ≈ −1,500` directions cancel into R22's single-package smear unless `operations` is split; the design's "net neutral for those" is the admission.
- Uncertainty 1 (pty over a mounted socket): the fallback "keeps one file handoff … under `ephemeral/`" reintroduces T1/F19 the design deletes; if it is taken, 3.2's F19 "settled" must be re-marked.

---

## 3. HELD — attacks that failed, one line each

- **A1 plugin engines:** every engine-specific fact I could find in core-facing code today (`defaultLLMPlugin`, `resolvePermissionMode`'s name compare, `forceExport*`, `resumeCapableBackends`, `engineHomeVar`, the hook decoders, the overlay dirs, `mcpEntries`) has a named `Description` field or `Declaration` entry; `Engine` has no method taking a `*config.Config` or a path.
- **A1 one composite package:** `Catalog.Assemble` is the only route to `Package`; `Index` cannot yield an engine-facing value; listing/review read the `Catalog`; I found no second assembler in the signatures (the wire exception is R1, not a second assembler).
- **A1 static+dynamic delivery:** `Route` is a declared trait; both halves plan from one `PlanFor`; materialize and session share the writer (R4 is about ownership, not the pipeline).
- **A1 session home default:** `SessionTarget` requires `roots.SessionHome`; `engine.Roots` has no real-home member; `Traits.Root == RootProjectRoot` reachable only by explicit `prefs`; no `reroot`/`preferOutOfCwd` survives (R9 is the one leak).
- **A1 materialize writes the project root:** `AtRestTarget` is the only project-root `Target` and `operations.Materialize` its only caller; `manage hooks install` is a restriction of it.
- **A1 hexagonal inward-only:** every edge in the 1.1 graph points inward; `MS --> OPS` is legal by the ring rule (its problem is R24's ownership leak, not direction).
- **A1 MCP callback deleted / one owner:** no `ServeStdio`, no forward, no marker; `bus.New` in `cli` only plus the session lock at mint (`sessionlock.Hold` today lives in `operations/sessions.go`, movable); a participant `mcp connect` cannot construct a coordinator. The leak is R24's `Minter` path, not the shim.
- **A1 engines via native surfaces / fail loud:** `Declaration` absent-kind = refuse with `Unsupported[kind]`; `Traits.LaunchOnly` refuses at rest. (R9 is the one substitution.)
- **A1 two isolation axes, fatal mismatch:** `Axes{Workspace, Runtime}`; `Runtime.Prepare` errors on mismatch; `--degraded` reaches `Resolve` as `Settings.Strictness` and drops only runtime; today's `Prepare` silent degrade to `None` is listed deleted.
- **A1 verified-before-delivery, preimage in one place, hash never authority:** `Exposure.Bytes`, `composite.Preimage`, `Catalog.Entry.Hash` unread by `Decide` — holds inside the originator (R1 is where it leaks).
- **A1 orchestrating-agent vocabulary:** "coordinating agent" does not appear; originator/runtime coordinator/orchestrating agent used consistently.
- **A2 identity hops:** `Session` is passed from `Resolve` through both codecs; `Identify(token)` returns the minted value; hook verbs decode once at `main`; I found no re-read of the harp inside a process in the signatures. (`CTXLOOM_ENGINE` in `Session.Env()` is the one addition and it is justified in 4.3 #8.)
- **A2 the ASSUMED node (TERM):** it rides `Resolved.Env()` typed; the assumption is only which process observes the terminal; correct.
- **A3 codecs:** one `pluginwire.Encode/Decode`, one `runnerlink.EncodeStartRun/Decode`, one `bidiSession`; no second `RunStart` builder survives in the signatures.
- **A3 inbox:** one `Inbox` type (its runner instance is R5's problem, not a duplicate).
- **A3 ownership record:** one `Ownership` model (its cardinality is R4).
- **A3 reaper/members:** one `Members` table, one `ReapPolicy`, one `Tree.Walk`; `CoordMembers` is a sibling table for a different tree, not a duplicate.
- **A4-f invalid local signature + valid countersign:** carried: `Decide`'s ladder reaches the first-party arm before the countersign arm, so a LOCAL item admits as `ReasonStaleLocalSignature` today (`bundles/authorizer.go`) and under 3.3 #3b's "withhold" is withheld before any countersign is read; consistent and loud either way. Hidden third option noted in section 7.
- **A4-g two projects under one home tree:** harp uniqueness is mkdir under one sessions root; `Record.Project`, `Store.List(project)`, the coord dir per project-key; `mcp connect <harp>` resolves the project through `Store.Find`. No collision found.
- **A4-h codex without StructuredChat:** refused with the engine's words; only the WHERE is wrong (R15).
- **A5 U2 (`ResolveAndHeal … live`):** the `switch live` in `operations/session_source.go` does the same in all three arms; deleting the parameter is behaviour-preserving. Held.
- **A5 S3, S4, S4r, S5a as unattended type-introductions:** each lands on today's tree; S4r's `trackedGroup` and register-after-hello are pure fixes with a race-forcing gate.
- **A5 S5a before S5b, S5b before S6 (`Exports`), S7 before S8 (`Resolved` first), S8 before S9 (endpoint root), S12 after S8, S13 after S12, S14 after S5b+S8:** each prerequisite is real and in the right direction.
- **A6 R5 (nothing imports a root):** `cmd/*` are themselves roots; no internal package needs `cli` or `runner`.
- **A6 IsLive staleness:** `archlint.allowlistLivenessEnabled` exists today; the "shrinking allowlist" mechanism the design relies on is real.
- **A9-1 composition (except R18):** container = `Runtime(container).Start(Resolved)` over the same `runner.Serve`; inputs cross as wire + env + mounts; no second delivery pipeline or surface list exists in the signatures.
- **A10 addressed directly by the engine:** `.mcp.json` names URL + bearer; no shim process; held. (Per-session and typed-input halves are R2.)

---

## 4. CORRECTIONS to the findings-settled table (3.2)

| Row | Claimed | Actual | Gap |
|---|---|---|---|
| F1 | settled | NOT settled | the child's Setup runs in the runner from a `Package` that cannot arrive (R1); the wire carries a loadout, so F1 is settled only once `Plan` is the wire value |
| F4 | settled | NOT settled | one `Catalog` per originator with no invalidation contract re-creates "two configs feed one spawn" as "one stale config feeds every spawn" (R3) |
| F5 | settled | partial | `Resolved.Binary()` is host-only; a container runner must re-resolve (R8) |
| F9 | settled | partial | the session home root is typed, but the seeding of that home has no typed host-home source (R8) |
| F10 | settled | settled inside the originator; leaks at the wire (R1) | — |
| F17 | settled | partial | `*config.Config` is gone from core, but `Settings`+`Catalog`+`Trust` are three loose values with no owner (R3) |
| F19 | settled | conditional | the design's own fallback for uncertainty 1 restores a file handoff (R25) |
| B6 #2 (c) | settled | settled | — |
| B6 #7 | settled | NOT settled as ruled | one endpoint per runner process, not per session; endpoint minted after launch (R2) |
| B6 #8 | settled | partial | one verbs layer, but a runner-side `Inbox` over a file spool breaks container children (R5) and `mcpserve → operations` leaves a second constructor path to `Resolve` (R24) |
| B6 #10 | settled pending rulings | partial | `TrustContext` is one holder; the `Package` crossing the wire adds a second decision point unless R1 lands |
| "Not settled: none" | — | wrong | the four rows above plus the two hard cases (a), (d) the design refuses by type (R6) or cannot resume (R7) |

---

## 5. MIGRATION corrections

Reordered and split list (changes to 4.1 only where A5 found a defect; unchanged slices keep their number):

1. **S1** minus `CapPeerMessaging` (→ S10) and `ForwardPermissions` (→ S7); add the failure-report parity gate for the reader chain.
2. **S2a** the `only:` rule form + N1, zero rules aimed (unattended). **S2b** R1–R9 aimed with the corrected wording (R10, R24), first allowlists read by a human.
3. **S3** + `composite.Library` (R3) wrapping today's memo and loader, `Invalidate` wired to `Save`/`Sync`/`agent set`; `launch.Settings` obtained only through it. Gate: a test that writes `config.yaml` then calls `Current` and sees the new binding without a process restart; a mutant that skips `Invalidate` fails it.
4. **S4, S4r, S5a, S5b** unchanged.
5. **S6** with the named interim: `Package` → today's `ManagedConfig` bridge (`delivery.PlanFor` is not yet there), deleted in S8; gate adds `preimage_wire_parity` against the contracts in force (R25).
6. **S7** with `Resolved` carrying `MCPEndpoint` (reserved until S9), `Source.Resume{Harp}` (R7), `Binary{Host, Engine}` (R8); `PlanFor` moved here or to S8 but always ORIGINATOR-side; the codec carries `Plan`, never `Package` (R1).
7. **S8** with per-entry `Ownership` (R4); the routing-rule sentence deleted (R9); gate adds "a project root with a session's unsafe delivery survives a `materialize` and an uninstall by the other owner".
8. **S9** first commit: `identity.MCPEndpoint` minted per harp in `Resolve`, `Dynamic.Serve(ctx, plan, ep)` binds it (R2); gate adds "a one-shot child's `.mcp.json` names the same endpoint on turn 1 and turn 2" and the container reach-back journey (R11).
9. **S10** first commit: `RunnerLink.Deliver` + runner `Inbox.Push` (R5); `Spawner.Start(…, reach launch.Reach)` (R11); delete the `mcpserve → operations` edge (R24); gate adds the container `agent_send` journey and "a container child receives a parent's mail".
10. **S11** unchanged in content; gate adds the container reach-back journey (R11) and, if uncertainty 1's fallback is taken, re-marks F19.
11. **S12, S13** unchanged; S13 splits `operations` into use-case subpackages (R22).
12. **S14, S15** unchanged.

Slices to split: S2 (analyzer form vs rules). Slices to merge: none. Slices whose "unattended: yes" must become "human present": S1 as written (live callers), S2b.

---

## 6. What I could not determine, and what would settle it

- **Whether any test today proves reach-back from a container child.** j002400 proves location, j002300 proves the bus on the host, the coordinator notes prove a developer-host child. Settle: run a `container-rootless` child that calls `agent_send(to: parent)` and read the originator's roster; if it works, write it as the journey R11 names.
- **Whether `composite.Preimage` as designed is byte-identical to `signing.FragmentPreimageContract` and `CountersignContract /2`.** The design says "one place"; it does not say "the same bytes". Settle: the `preimage_wire_parity` test the design mentions, run against S6's first commit.
- **Whether the pty over a mounted go-plugin socket carries resize/signals** (the design's own uncertainty 1). Settle: the E3 journey on a real terminal before S11 is scheduled.
- **The per-package LOC directions.** Not measurable until the packages exist; the design says so. Settle: nothing before S13; treat 3.1's numbers as direction only, as it asks.
- **What `delegation.spool_delivery` becomes** when the runner's inbound path is push-only (R5, R25). Settle: a sentence in 1.7.
- **Whether the human's "two agent depths" ruling means a type or a default** (R6). Settle: one question, section 7.

---

## 7. A8 — the ten rulings, cold-readability, hidden options, contradictions

1. **earthly-city.** Statable cold. Cost line understates: add "and an MCP listener and a session lock per distill". No contradiction.
2. **reroot.** Statable. The design's own routing rule (R9) contradicts its recommendation "refuse"; delete R9's sentence before asking.
3. **exec-gate.** Three questions in one; split them. (b) contradicts the documented contract in `bundles/authorizer.go` (`ReasonStaleLocalSignature` admits with a warning; the doc cites DECISIONS.md P12), which the design does not cite. Hidden third option for (b): "withhold unless countersigned over these exact bytes", which is today's REMOTE rule (`ReasonTampered` + countersign allows) applied to local content.
4. **one or two signatures.** Statable. Hidden third option: keep both files, ONE verifier reads the manifest and treats the sibling as an index (never an authority), so a single-file bundle still publishes.
5. **agentcoordpb in core.** Statable; but R1/R9 must be reworded either way (R10).
6. **owner run deleted.** Statable; the design already names the no-cost third option. Note A10's ruling favours roster visibility for every session's endpoint; ask whether the owner's run should appear in the roster for that reason.
7. **codex/opencode.** Statable; cost unsized (two thin packages + conformance). No contradiction.
8. **reaper clock.** Statable. No contradiction.
9. **nifty-rival.** Statable. No contradiction.
10. **E3 process shape.** Statable; the fallback re-opens F19 (R25); say so in the question.

**Rulings the design needs and does not ask:** (a) depth as a type vs a default cap (R6); (b) deleting hot reload per spawn, a documented requirement, in favour of an owner with invalidation (R3); (c) `Package` never crossing the wire, `Plan` as the wire value (R1); (d) one MCP endpoint per harp minted at `Resolve` (R2, now ruled by A10 and to be written into 1.7).

---

STATUS: COMPLETE. Counts: refuted 25 (R1–R25, of which R14 and R19 are hard-case restatements), held 33, corrections to 3.2: 11 rows, migration corrections: 12 entries.
