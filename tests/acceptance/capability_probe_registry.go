// Package acceptance: the capability-probe registry.
//
// UNTAGGED, like live_engine_registry.go beside it, so `just test` compiles and
// walks it without a built binary or a live engine. This is a declaration, not
// a fixture.
//
// WHAT THIS FILE IS FOR. ctxloom's engine interface asks each engine to do
// about twenty distinct things — launch, deliver context by four different
// routes, write settings the engine then EXECUTES, register MCP servers,
// enforce a permission tier, surface an approval, resume a session, capture a
// transcript. Most of those claims were, until this ladder, either proven only
// hermetically (we wrote the bytes; nobody watched a vendor binary read them)
// or proven once by hand, in a session, by a human who then closed the
// terminal. The distance between "the descriptor declares it" and "a real
// engine did it" is where this product's defects have actually lived.
//
// A registry rather than a pile of feature files because the failure mode of a
// test suite like this is not a red test — it is a MISSING one. A capability
// nobody wrote a probe for looks exactly like a capability that passes: no red
// line, no output, nothing. So the inventory below is declared as data, every
// probe declares which inventory rows it reaches, and the completeness test
// (capability_probe_registry_test.go) refuses any row that is neither probed
// nor explicitly, reasoned-ly excused. An absence has to be typed out by a
// human before the suite will accept it.
//
// THE STATUS FIELD IS THE HONEST PART. "wired" means a scenario exists; it does
// NOT mean anyone watched it go green against a real engine. Only
// "live-verified" claims that, and the completeness test makes such a row carry
// its evidence — what was measured, when — because an unevidenced green claim
// in a table like this is worse than a blank: it stops anyone from looking.
//
// RED-MAPPED CELLS ARE CELLS, NOT ABSENCES. A cell expected to fail records its
// EXPECTED failure shape, so a sweep can DIFF shapes rather than count failures
// — a red cell that starts failing a new way is signal, and a counter cannot see
// it. The container rows were the worked example: added red under the ruling
// because containerized delegation had never been demonstrably correct,
// then flipped to live-verified once container auth keying landed
// and all eight were run. Two red maps stand today, and both are product
// findings rather than expectations: P0's kiro host/none (ANSI decoration leaking
// into a non-interactive capture) and P1's claude-code hook cell (the hook
// context route delivering nothing — see the note under the P1 rows, which is
// also where the minted-harp ruling earns its keep). Flipping one is a one-line
// edit per cell that the completeness gate forces somebody to make consciously —
// which is the entire reason the shapes are written down instead of assumed.
package acceptance

import (
	"fmt"
	"sort"
	"strings"
)

// --- the capability inventory ------------------------------------------------

// capabilityRow is one thing ctxloom's engine interface asks an engine to do,
// named by the SYMBOL that asks it (standing rule: cite by symbol, never by
// file:line — a stale symbol fails loud, a stale line misleads silently).
type capabilityRow struct {
	Num    int
	Symbol string
}

// capabilityInventory is the census the ladder is measured against. Numbering
// is stable and referenced by probeSpec.Capabilities; rows are appended, never
// renumbered, so a probe's claim cannot silently come to mean something else.
var capabilityInventory = []capabilityRow{
	{1, "agent.Backend.Execute — one-shot launch round trip (ctxloom run --one-shot)"},
	{2, "agent.StructuredChat.Chat — structured chat over ACPTransport"},
	{3, "agent.ApproachUnsafeFile — native context file (CLAUDE.md / AGENTS.md / steering / instructions[])"},
	{4, "agent.ApproachSystemPrompt — --append-system-prompt-file (claude only)"},
	{5, "agent.ApproachHook — SessionStart inject-context"},
	{6, "agent.SettingsWriter / agentDescriptor.newWriter — settings+hooks CARRIAGE"},
	{7, "bundles.HookEvent* — hooks actually FIRING in the vendor binary"},
	{8, "wire.MCPConfig / ChatRequest.MCPServers — MCP registration + tool round trip"},
	{9, "agent.CommandExport / agentDescriptor.exports — slash-command export"},
	{10, "agent.SkillExport / agentDescriptor.skillExports — skills export"},
	{11, "agent.PermissionMode / enforcesReadOnlyPlan — permission tiers, plan read-only"},
	{12, "ChatRequest.ForwardPermissions / agent.PermissionRequest — approval flow"},
	{13, "agent_send / coord.peerSend / bridgeTurnResult — steer and mail at turn boundaries"},
	{14, "ChatRequest.ResumeSessionID / ChatSessionInfo.Resumable — resume and session identity"},
	{15, "transcript.Record / paths.HarpCanonicalTranscriptPath — canonical transcript capture"},
	{16, "agentDescriptor.versionCommand / engineversion.Command — version reporting"},
	{17, "authCheckClaude/Kiro/Codex/Opencode — availability and auth probing"},
	{18, "structured output contract — JSON only, no preamble"},
	{19, "ChatRequest.Runtime=container — container runtime and per-engine container auth"},
	{20, "resolveModel / ModelDeliveryQuirk — model resolution and pinning"},
}

// capabilitiesProvenElsewhere are inventory rows the ladder deliberately does
// NOT give a probe of its own, each with the reason and the thing that proves
// it instead. Checked for exact-set agreement with the probes' coverage (the
// assertExactUncovered idiom): a row here that a probe DOES reach is a stale
// excuse and fails just as loudly as an unprobed row, so this map cannot
// quietly become a place to park work.
var capabilitiesProvenElsewhere = map[int]string{
	17: "every cell's own gate IS this probe: probeEngine + the liveAgent authCheck functions run before any paid turn and print engine+reason on every acceptance run, and CTXLOOM_LIVE_REQUIRE turns a missing engine into a hard red. A separate probe would re-run the gate and prove nothing the gate did not already print.",
	20: "the pinned cheap model in each liveAgents[*].config is carried by EVERY paid cell in the ladder, so a model that failed to resolve reds the cell that used it; the claude ModelDeliveryQuirk is pinned hermetically by version in the registry's own tests. A dedicated live cell would buy a turn to re-observe what all ~40 other cells already depend on.",
}

// --- probe rows ---------------------------------------------------------------

// probeStatus is a cell's honest state. The ladder's value depends on these
// being distinguishable: "wired" and "live-verified" look identical in a green
// run and mean completely different things about what is known.
type probeStatus string

const (
	// probePlanned: the cell is designed and budgeted; no scenario exists yet.
	probePlanned probeStatus = "planned"
	// probeWired: a scenario exists and runs. NOT a claim that it has passed
	// against a real engine.
	probeWired probeStatus = "wired"
	// probeLiveVerified: someone watched this cell go green against the real
	// engine. Requires evidence in Reason (what, when) — enforced.
	probeLiveVerified probeStatus = "live-verified"
	// probeGatedOut: the engine DECLARES this capability absent. Not a gap and
	// not a red; requires the declaring symbol in Reason — enforced.
	probeGatedOut probeStatus = "gated-out"
	// probeDeferred: a cell we have chosen not to build yet, with the reason.
	// Recorded so the absence can never read as an oversight.
	probeDeferred probeStatus = "deferred"
)

// probeCell is one addressable cell: an engine under one pair of isolation
// axes, optionally discriminated by a variant.
type probeCell struct {
	Engine    string // backend type as the Examples table writes it ("claude-code")
	Runtime   string // host | container | container-rootless | container-rootful (P0 is on the ownership split; other probes are not yet)
	Workspace string // none | worktree
	Variant   string // optional intra-cell discriminator ("system-prompt", "control")

	Status probeStatus
	// Reason carries the evidence or the excuse. MANDATORY for live-verified
	// (what was measured, when), gated-out (the declaring symbol) and deferred
	// (why not yet).
	Reason string
	// GateAtRuntime distinguishes WHERE a gated-out cell's gate is enforced,
	// and it changes what the feature file must contain.
	//
	// A gate enforced by ABSENCE (the default) means the engine declares the
	// capability gone — opencode's noHooksReason, resolveResumeMode's refusal —
	// so there is nothing to run and the feature must carry NO Examples row for
	// it. A gate enforced AT RUNTIME means production itself refuses, loudly,
	// naming the reason, when the cell is attempted: kiro's container axis,
	// which needs KIRO_API_KEY because its credential is a global sqlite no
	// HomeVar relocates. Those cells KEEP their Examples row, because the loud
	// skip is the report — deleting the row would delete the only place a human
	// meets the limitation.
	//
	// The feature-drift test checks both directions on this field, so neither
	// kind of gate can quietly turn into the other.
	GateAtRuntime bool
	// ExpectedFailure is the RED MAP: the failure shape this cell is currently
	// expected to produce, recorded so a sweep diffs shapes instead of counting
	// reds. Empty means "expected to pass". Flipping a container cell to
	// green-expected is a one-line edit here that the completeness test forces
	// somebody to make consciously.
	ExpectedFailure probeShape
	// ExpectedFailureNote is the measured detail behind ExpectedFailure —
	// mandatory whenever ExpectedFailure is set, because a shape without its
	// story cannot be diffed by a human six weeks later.
	ExpectedFailureNote string
}

// probeSpec is one rung of the ladder.
type probeSpec struct {
	// Name is the @probe-<name> tag suffix and the registry key. Lowercase,
	// no whitespace — a tag with a space in it does not just fail to match,
	// it aborts the whole gherkin parse (see TestFeatureFilesParse).
	Name  string
	Title string
	// Capabilities are the capabilityInventory row numbers this probe reaches.
	Capabilities []int
	// Channel is where the minted harp is planted. Every probe states this;
	// it is the honesty claim the probe rests on, and it is the same value the
	// verdict function carries (probeVerdict.Channel).
	Channel probeChannel
	// Feature is the feature file the cells live in, empty while planned.
	Feature string
	// AssertionSide marks a probe that adds assertions to OTHER probes' cells
	// rather than buying cells of its own — free, and therefore allowed to
	// declare no cells.
	AssertionSide bool
	// Paid reports whether a cell costs a model turn. Drives the cost
	// statement a sweep prints before running.
	Paid  bool
	Cells []probeCell
}

// channelComposedContext etc.: the planting channels, declared once here and
// handed to each probe's verdict function so the failure message and the
// registry can never disagree about what a cell was testing.
var (
	channelComposedContext = probeChannel{
		Shape: "CONTEXT-DELIVERY failure",
		Where: "the agent's own composed context",
	}
	channelMCPToolResult = probeChannel{
		Shape: "MCP-DELIVERY failure",
		Where: "the fixture MCP server's tool result, and nowhere else — not in context, not in the prompt, not in the environment",
	}
	channelHookStamp = probeChannel{
		Shape: "HOOK-DELIVERY failure",
		Where: "the session_start hook's argv, reachable only by the engine actually executing the hook ctxloom wrote",
	}
	// channelHookStdout is P3's SECOND channel, and it exists as its own entry
	// rather than as a variation on channelHookStamp because the two fail for
	// different reasons in different subsystems. The stamp channel fails when
	// the engine never EXECUTED the hook; this one fails when the engine
	// executed it and never INGESTED what it printed. Only an engine whose
	// declared context approach is the hook can be asked the second question —
	// codex, whose codexApproaches lists agent.ApproachHook first for
	// agent.SurfaceContext, and no other engine at this base.
	channelHookStdout = probeChannel{
		Shape: "HOOK-OUTPUT-INGESTION failure",
		Where: "the session_start hook's STANDARD OUTPUT, written to no file the engine reads and present in no prompt",
	}
	channelSentinelFile = probeChannel{
		Shape: "SENTINEL-DELIVERY failure",
		Where: "a sentinel file the fixture wrote, whose PERSISTENCE (not its echo) is the assertion",
	}
	channelBusMessage = probeChannel{
		Shape: "BUS-DELIVERY failure",
		Where: "the agent_send message body on the coordinator↔child bus",
	}
	channelGatedAction = probeChannel{
		Shape: "APPROVAL-DELIVERY failure",
		Where: "a file the gated tool call writes, absent until the approval is answered",
	}
	channelTurnOnePrompt = probeChannel{
		Shape: "RECALL failure",
		Where: "turn one's PROMPT — deliberately not a fragment, because re-delivered context on respawn would false-green resume",
	}
	channelEngineBinary = probeChannel{
		Shape: "VERSION-REPORT failure",
		Where: "the installed engine binary's own version output (no nonce: nothing is planted, so nothing can be echoed)",
	}
	channelForeignLedger = probeChannel{
		Shape: shapeLeak,
		Where: "no channel at all — this probe asserts the ABSENCE of every other cell's minted harp",
	}
)

// probeP0 etc.: the registry's own names, so a caller addressing a cell and the
// table declaring it cannot drift apart on a typo.
const (
	probeP0 = "p0-hello-world"
	probeP1 = "p1-approach-sweep"
	probeP2 = "p2-mcp-round-trip"
	probeP3 = "p3-hook-firing"
	probeP4 = "p4-plan-sentinel"
	probeP5 = "p5-approval-surface"
	probeP6 = "p6-steer-echo"
	probeP7 = "p7-resume-recall"
	probeP8 = "p8-transcript-payload"
	probeP9 = "p9-version-report"
	probePX = "px-foreign-harp"
	// The two rungs deliberately NOT built. Present as deferred rows so rows
	// 9 and 10 of the inventory are visibly un-probed rather than invisibly so.
	probePCmd   = "p10-command-invocation"
	probePSkill = "p11-skill-invocation"
)

// liveEngines is the ladder's engine vocabulary, in the display order the
// availability report and the Examples tables already use. Backend-type spelling
// (what a cell writes); backendTypeToLiveKey maps it to liveAgents' own key.
var probeEngines = []string{"claude-code", "codex", "kiro", "opencode"}

// hostCell / containerCell are shorthands, so a table row reads as data rather
// than as four repeated field names.
func hostCell(engine string, status probeStatus, reason string) probeCell {
	return probeCell{Engine: engine, Runtime: "host", Workspace: "none", Status: status, Reason: reason}
}

// probeRegistry is the ladder. Order is the ladder's own order and is the order
// a sweep runs and a report renders — kept as a slice, not a map, because map
// iteration order is unspecified and this table's whole value is being
// diffable across runs.
var probeRegistry = []probeSpec{
	{
		Name:         probeP0,
		Title:        "structured-output + default-context floor: one JSON object carrying a nonce planted in composed context",
		Capabilities: []int{1, 2, 3, 18, 19},
		Channel:      channelComposedContext,
		Feature:      "engine_isolation_matrix.feature",
		Paid:         true,
		Cells:        p0Cells(),
	},
	{
		Name:         probeP1,
		Title:        "context-approach sweep: the same task with ManagedConfig.Surfaces pinning a non-default approach",
		Capabilities: []int{4, 5},
		Channel:      channelComposedContext,
		Feature:      "capability_context_approaches.feature",
		Paid:         true,
		Cells: []probeCell{
			{Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: "system-prompt",
				Status: probeLiveVerified, Reason: "agent.ApproachSystemPrompt (--append-system-prompt-file) is claude-only, and no test had ever selected it live. Measured 2026-08-13: 1 scenario / 3 steps green in 5.3s, harp \"fond-ugly-cycle\" echoed back exactly, no degrade warning. SIDE-CHANNEL-CONTROLLED, by the only two arguments available: the DELIVERY writes out of cwd (claude's system-prompt realization is the ladder's one context delivery that puts no nonce bytes in the workspace — TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace), and the workspace-search channel that remains — the fixture's own bundle YAML in the project tree — is ruled out by the NEGATIVE CONTROL sitting next to it: the claude hook cell has that identical tree, identical tools and identical prompt, and comes back with no nonce. An engine that was reading the fixture off disk would have passed both. Inventory row 4 moves from claimed to proven."},
			// FIXED — was THE ONE RED IN P1, a PRODUCT FINDING. See the
			// long note below the table for the full history: claude's hook context
			// route delivered nothing because agent.LaunchBackend.deliverSet's
			// SharedCell loop never installed the SessionStart injection hook on
			// a successful (nil-error) noop context write — only on a write
			// FAILURE. Fixed by having deliverSet also install the hook
			// (agent.LaunchBackend.installContextInjectionHook) whenever a
			// SharedCell resolves SurfaceContext at ApproachHook.
			{Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeLiveVerified,
				Reason: "measured 2026-08-16 after the deliverSet fix landed: 1 scenario / 3 steps green, nonce harp \"obese-hilly-gusto\" echoed back exactly, no degrade warning. MUTATION-CONFIRMED: reverting the fix reproduces the exact pre-fix shape live — CONTEXT-DELIVERY failure, well-formed JSON carrying none of a freshly minted nonce (\"aloof-dire-reach\") — and restoring it goes green again. Doubles as the negative control for the system-prompt cell's side channel."},
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "none", Variant: "system-prompt",
				Status: probeLiveVerified, Reason: "measured 2026-08-25: 1 scenario / 3 steps green in 71s, nonce harp \"soft-grand-trout\" echoed back exactly, no degrade warning. ANSWERS WHAT P0 CANNOT: P0 proves DEFAULT composed context survives this axis; this proves the PINNED system-prompt route does. That was genuinely open, because appendFlagDelivery writes an OUT-OF-CWD scratch file consumed via --append-system-prompt-file rather than a file in the mounted tree — had it been written host-side the cell would have red as a CONTEXT-DELIVERY failure. Delivery reaches into the container correctly."},
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "worktree", Variant: "system-prompt",
				Status: probeLiveVerified, Reason: "measured 2026-08-25: 1 scenario / 3 steps green in 71s, nonce harp \"weird-idle-punch\" echoed back exactly, no degrade warning. THE MIXED CORNER, and it was run because P6 measured what skipping one costs — its host/worktree cell failed where both-off and both-on passed, since the axes resolve credentials by DIFFERENT mechanisms (a container bind-mounts, a worktree seeds via credentialSeedSpecs). Here both boundaries hold together: the system-prompt scratch file survives a container whose workspace is also an isolated checkout."},
			{Engine: "claude-code", Runtime: "host", Workspace: "worktree", Variant: "unsafe-file-shared",
				Status: probeLiveVerified, Reason: "the SharedRealization out-of-cwd writers (claude.NewSurfaces) are the one race-safe shared-cwd conversion, and the worktree axis is where that matters. Measured 2026-08-13: 1 scenario / 3 steps green in 5.5s, harp \"snug-void-rebel\", no degrade warning. CLAUDE.md into an isolated checkout delivers."},
			// RETRACTED after S4's hook-firing probe
			// forced a re-adjudication. This cell WAS recorded live-verified
			// with "the codex hook finding is fixed". It was not fixed; the cell
			// never touched the hook. See the retraction note below the table.
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeDeferred, Reason: "NO SELECTABLE CONFIGURATION ISOLATES CODEX'S HOOK CONTEXT CHANNEL, so an attributing cell cannot be built here yet — deferred rather than left standing as a green that measures something else. Two independent reasons, both pinned hermetically in capability_context_channels_test.go: (1) codex's Surfaces.SurfaceFor resolves (context, Hook) to a COMPOSED delivery that also writes the native AGENTS.md, which codex reads by itself with no hook involved (TestCodexHookApproach_AlsoWritesAGENTSMD); (2) on this fixture ctxloom writes codex no hook AT ALL — it warns \"codex hooks and MCP servers were NOT written ... no durable project home exists — see config_home\" — so the channel is absent from the session entirely. An attributing cell needs a binding with `config_home: project` (so a hook is written), plus S4's stamp-file discipline (so hook EXECUTION is observed rather than inferred), plus a way to suppress the AGENTS.md leg. approachRequiredSurfaceDelivered now reds any hook cell whose hook was not written, so this cannot silently come back."},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "unsafe-file",
				Status: probeLiveVerified, Reason: "codex's AGENTS.md route. Measured 2026-08-13: 1 scenario / 3 steps green in 7.0s, harp \"smug-fatal-rush\", no degrade warning. NOT SIDE-CHANNEL-CONTROLLED, and the registry says so rather than implying otherwise: codex has no out-of-cwd realization for any surface (TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace), so this delivery writes AGENTS.md INTO the working directory, and the fixture's own bundle YAML sits in the project tree beside it. S4 measured codex satisfying a nonce probe by searching the workspace with rg. So this cell proves the bytes reached the workspace and the model produced them; it does not separate native AGENTS.md ingestion from agentic file search. The claim is stated at that strength deliberately."},
			// These two named the HOOKS surface's symbols (kiro.WithContextHook,
			// noHooksReason) — which is P3's gate, a different claim. What
			// declares the absence for a CONTEXT-DELIVERY probe is the engine's
			// own ApproachTable, and that is now what they cite, checked against
			// production hermetically (TestApproachPinAcceptedByEngine_
			// AgreesWithTheEnginesOwnTable). The original symbols are kept: they
			// are the same absence seen from the other surface.
			{Engine: "kiro", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeGatedOut, Reason: "agent.ApproachHook is absent from kiroApproaches' ApproachTable entry for SurfaceContext (unsafe-file only) — kiro.WithContextHook is the same absence stated at the hooks surface, and it fails loudly"},
			{Engine: "opencode", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeGatedOut, Reason: "agent.ApproachHook is absent from opencodeApproaches' ApproachTable entry for SurfaceContext (unsafe-file only); opencode declares no hook surface at all either (noHooksReason)"},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "system-prompt",
				Status: probeGatedOut, Reason: "agent.ApproachSystemPrompt is absent from codex's ApproachTable — claude-only capability"},
			{Engine: "kiro", Runtime: "host", Workspace: "none", Variant: "system-prompt",
				Status: probeGatedOut, Reason: "agent.ApproachSystemPrompt is absent from kiro's ApproachTable — claude-only capability"},
			{Engine: "opencode", Runtime: "host", Workspace: "none", Variant: "system-prompt",
				Status: probeGatedOut, Reason: "agent.ApproachSystemPrompt is absent from opencode's ApproachTable — claude-only capability"},
		},
	},
	{
		Name:         probeP2,
		Title:        "MCP tool round trip: a fixture stdio server whose get_nonce tool is the ONLY place the harp exists",
		Capabilities: []int{8},
		Channel:      channelMCPToolResult,
		Feature:      "capability_mcp_round_trip.feature",
		Paid:         true,
		// ALL FOUR CELLS WERE RUN, one at a time on this box, against
		// real engines on real subscriptions. Three went green and one went red,
		// and the red is the interesting one — see kiro below.
		//
		// What every row shares is the delivery path under test: config.yaml
		// `mcp.servers` (the ungated production surface — a bundle's MCP block
		// passes the executable trust gate, and a withheld server would red as an
		// MCP-delivery failure that is really a trust decision) → ManagedConfig.MCP
		// → that engine's own native file. Nothing in the fixture writes an engine
		// file, so a green row is ctxloom's delivery working, not a file we wrote
		// being read back.
		Cells: []probeCell{
			hostCell("claude-code", probeLiveVerified,
				"measured 2026-08-13 on this branch: 1 scenario / 3 steps green in 9.9s, harp \"messy-plump-exit\", served only by the fixture server's get_nonce tool and echoed back exactly. Path: config mcp.servers → ManagedConfig.MCP → claude's --mcp-config scratch file (shared cell; layered rather than strict, so a user's own .mcp.json still loads). FIRST proof anywhere that a non-forwarder MCP server reaches a real engine through ctxloom and gets called."),
			hostCell("codex", probeLiveVerified,
				"measured 2026-08-13 on this branch: 1 scenario / 3 steps green in 89s, harp \"lunar-soft-navy\". Path: config mcp.servers → ManagedConfig.MCP → codex's folded config.toml [mcp_servers] table (codex advertises no distinct SurfaceMCP — MCP folds into its config surface)."),
			// KIRO IS RED, AND THE RED IS PRECISE. The fixture server's own call
			// log — the evidence this probe exists to collect — says registration
			// and DISCOVERY both worked and only INVOCATION did not. That is a
			// much narrower finding than "kiro's MCP is broken", and it is only
			// available because the server records what it was asked.
			{Engine: "kiro", Runtime: "host", Workspace: "none", Status: probeWired,
				Reason:          "delivery path under test: config mcp.servers → ManagedConfig.MCP → .kiro/settings/mcp.json, written through the shared MCPFileConfig reconciler",
				ExpectedFailure: channelMCPToolResult.Shape,
				ExpectedFailureNote: "measured 2026-08-13, twice. The fixture server's call log read: start request(initialize) request(notifications/initialized) request(tools/list) eof — so ctxloom's registration DID reach kiro, kiro DID spawn the server, complete the handshake and enumerate its tools, and then never called get_nonce. Registration and discovery work; invocation does not. kiro's own stderr looped on `Tool validation failed: No tool with \"dummy\" is found` for six minutes before giving up, and the model's answer was the string \"non-verbatim placeholder - tool not found\" — kiro believed no such tool existed while having just listed it. " +
					"DIAGNOSTIC NOTE for whoever picks this up: the first attempt reported OUTPUT-FORMAT instead, because kiro's separate ANSI-decoration defect (P0's kiro host/none row owns that one) reds the form check, and the form check used to run first. mcpProbeAssert now asks the tool-path question ahead of the form question for exactly this reason. Do not read the two findings as one: the decoration defect is ctxloom-side and cosmetic to this probe, the invocation failure is not.",
			},
			hostCell("opencode", probeLiveVerified,
				"measured 2026-08-13 on this branch: 1 scenario / 3 steps green in 56s, harp \"petty-ratty-study\". The call log carries the whole round trip — start / initialize / notifications/initialized / tools/list / tools/call / tool_call / notifications/cancelled / eof — so this row evidences discovery AND invocation, not just the echo. Path: config mcp.servers → ManagedConfig.MCP → opencode.json's mcp block (folded into opencode's settings surface)."),
			// THE CONTAINER CELLS. These four rows previously read
			// "container MCP reach-back is undesigned — the endpoint DISCOVERY
			// gap ... (cross-container-comms finding)" and carried a bare
			// Runtime "container". BOTH were wrong, in different ways:
			//
			//  1. MISATTRIBUTED BLOCKER. The cross-container-comms finding is
			//     real and is now CLOSED (host-controlled discovery marker,
			//     coord.containerReachIPs' advertise policy, acp.reachBackBridge)
			//     — but it describes the COORDINATOR bus, which never governed
			//     this probe's own fixture stdio server. The actual blocker was
			//     that the fixture was a python3 script and the agent image has
			//     no interpreter; a stdio MCP server is a CHILD OF THE ENGINE, so
			//     a container cell runs it inside the container. That is fixed by
			//     cmd/probe-mcp-server.
			//  2. STALE VOCABULARY. "container" stopped being a runtime value
			//     when the axis split into container-rootless / container-rootful,
			//     which differ in who owns the daemon and therefore which uid a
			//     run's writes land as. A row naming the retired value cannot be
			//     selected by `just capability-probe` at all — it builds the tag
			//     @{{RUNTIME}} — so these rows were unrunnable by construction,
			//     which is part of why the false reason was never revisited.
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "none", Status: probeDeferred,
				Reason: "ONE blocker left, and it is a human decision, not an obstacle. The interpreter problem is GONE (cmd/probe-mcp-server). What remains: the fixture dir is sited outside the workspace ON THE HOST, and a container cell runs the server INSIDE the container, where that path does not exist. Delivering it needs a probe-only bind-mount seam in internal/lm/isolation. isolation.ProbeTraceEnvVar is the exact precedent — and its own doc carries the security argument for why a second one is not a mechanical addition: the gate is an inherited ENV VAR, so any parent process or CI job that exports it changes what an ordinary `ctxloom run` mounts. Filed for a ruling rather than taken unattended."},
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "worktree", Status: probeDeferred,
				Reason: "blocked on the same fixture-delivery seam as the container/none row. It is listed separately because it must land WITH that row, never after it: P6 measured what skipping a mixed corner costs — its host/worktree cell failed where both-off and both-on passed, because the axes resolve the credential by DIFFERENT mechanisms (a container bind-mounts it, a worktree seeds it via credentialSeedSpecs). A container row shipped without its worktree partner rebuilds exactly that blind spot."},
			// codex/kiro/opencode: deferred to 0.8.0 by decision, NOT by a
			// technical blocker. Their runtime value is corrected here so the
			// retired "container" spelling does not outlive the split, but they
			// are deliberately not admitted — 0.7.0 propagates claude-code only.
			{Engine: "codex", Runtime: "container-rootless", Workspace: "none", Status: probeDeferred,
				Reason: "deferred to 0.8.0 — 0.7.0 propagates claude-code onto the container axis only. No technical blocker is known for this cell now that the fixture needs no interpreter; it is unbuilt by scope, not by obstacle."},
			{Engine: "kiro", Runtime: "container-rootless", Workspace: "none", Status: probeDeferred,
				Reason: "deferred to 0.8.0 — see the codex container row. Note kiro's host cell is RED on INVOCATION (it lists the tool and never calls it), so a container cell would measure that defect again rather than the axis."},
			{Engine: "opencode", Runtime: "container-rootless", Workspace: "none", Status: probeDeferred,
				Reason: "deferred to 0.8.0 — see the codex container row"},
		},
	},
	{
		Name:         probeP3,
		Title:        "hook firing: the vendor binary executes the session_start hook ctxloom wrote, proven by the hook's own stamp file",
		Capabilities: []int{6, 7},
		Channel:      channelHookStamp,
		Feature:      "capability_hook_firing.feature",
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probeLiveVerified,
				"GREEN, measured 2026-08-13 on this branch: 1 scenario / 3 steps, stamp file carrying the 18-byte argv harp, run exit 0. Corroborated OUTSIDE the harness by a hand-built project run of the same fixture. This is the first live proof anywhere in the repo that a ctxloom-written hook is EXECUTED by a vendor binary (inventory row 7). Stage (a) only: claude declares agent.ApproachHook for SurfaceContext, but claude's SurfaceFor resolves that pair to noopContextDelivery, the documented no-op that never carries, so ctxloom does not deliver claude's context through a hook and this cell must not assert an output echo production never asked for."),
			{Engine: "codex", Runtime: "host", Workspace: "none", Status: probeWired,
				Reason:              "the ONLY cell that would run both stages. codexApproaches lists agent.ApproachHook FIRST for agent.SurfaceContext, making the hook codex's DEFAULT context route, so stage (b) - the harp printed on the hook's stdout reaching the model - is a claim production actually makes here. RED at this base; see the note.",
				ExpectedFailure:     "HOOK-DELIVERY failure",
				ExpectedFailureNote: "MEASURED 2026-08-13, and this is a CAPABILITY FINDING, not a harness fault: codex hooks written by ctxloom NEVER FIRE. Carriage is proven - with `config_home: project` the hook command was observed live in <project>/.ctxloom/state/<harp>/home/.codex/config.toml while the engine ran - and no stamp file appeared. Root cause isolated against the vendor binary directly, ctxloom out of the picture: with a hand-built CODEX_HOME carrying the identical [[hooks.SessionStart]] block, `codex exec` did NOT run the hook, and `codex exec --dangerously-bypass-hook-trust` DID (stamp written, same config, same turn). codex 0.144.4 gates hooks behind a PERSISTED HOOK TRUST that ctxloom does not satisfy and does not bypass, and codex says nothing when it declines. CONSEQUENCE BEYOND THIS PROBE: the hook is codex's DEFAULT CONTEXT ROUTE, so ctxloom's hook-carried context delivery to codex is inert too - P1's codex hook cell should expect the same red, and any green codex context row is arriving through the compositional AGENTS.md route instead. AND THE PROBE'S OWN DESIGN EARNED ITS KEEP HERE: on the run of 2026-08-13 codex ANSWERED WITH THE CORRECT stage-(b) HARP while its hook had never fired - its stderr shows it ran `rg` across the temp tree and the session store until it found the phrase in the fixture's own script. A probe that asserted only the echo would have gone GREEN on a completely dead hook mechanism. It reds because stage (a) is a FILE and is judged FIRST. DO NOT loosen this cell to green; it goes green when ctxloom satisfies codex's hook trust."},
			hostCell("kiro", probeLiveVerified,
				"GREEN, measured 2026-08-13 on this branch: stamp file carrying the 17-byte argv harp, and kiro's OWN stderr independently reports \"1 of 1 hooks finished in 0.08 s\" - two witnesses to the same firing. Carriage confirmed by the cell in .kiro/agents/ctxloom.json (kiro's surfaces are cwd-keyed, so unlike claude's they survive the scan). Note this cell is green while kiro's P0 row is red-mapped for terminal decoration on stdout: P3 asserts a FILE and is immune to that, which is the design working as intended. Stage (a) only, and not because kiro is weaker - KiroWriter.mapHooks maps unified session_start onto kiro's own agentSpawn hook, and kiro reads steering files for context rather than ingesting hook output, so firing is observable here and ingestion is not a claim kiro makes."),
			{Engine: "opencode", Runtime: "host", Workspace: "none", Status: probeGatedOut,
				Reason: "opencode declares hooks absent: noHooksReason (\"opencode has no hook mechanism\"). Not a gap - a declared absence, gated by ABSENCE, which is why capability_hook_firing.feature carries no Examples row for it."},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "session-end", Status: probeGatedOut,
				Reason: "unsupportedHookKinds[bundles.HookEventSessionEnd] / codex.NoSessionEndReason (\"codex has no session-end event\") - codex declares this one kind unsupported while supporting the rest, which is the reason P3 plants on session_start; gated by ABSENCE, so this variant gets no Examples row"},
		},
	},
	{
		Name:         probeP4,
		Title:        "plan sentinel: permissions=plan must leave a sentinel file's bytes untouched, and the bypass control must land the write",
		Capabilities: []int{11},
		Channel:      channelSentinelFile,
		Feature:      "capability_plan_sentinel.feature",
		Paid:         true,
		Cells:        p4Cells(),
	},
	{
		Name:         probeP5,
		Title:        "approval surface: ForwardPermissions must SURFACE a PermissionRequest, and the gated effect must appear only after the allow",
		Capabilities: []int{2, 12},
		Channel:      channelGatedAction,
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned, "adapter engine — the parked-forever class of bug lives here, so it lands first"),
			hostCell("codex", probePlanned, "adapter engine — lands with claude-code"),
			hostCell("kiro", probePlanned, "native ACP — sequenced after the adapter engines"),
			hostCell("opencode", probePlanned, "native ACP — sequenced after the adapter engines"),
		},
	},
	{
		Name:         probeP6,
		Title:        "steer/mail echo: a minted harp sent over the bus mid-session must come back on agent_recv",
		Capabilities: []int{13},
		Channel:      channelBusMessage,
		// P6's cells live in J002300's OWN feature file, beside the delegation
		// journey they extend, rather than in a file of their own. The design says
		// so ("extend the outline machinery; do NOT rewrite locked scenarios") and
		// the machinery says so louder: the steer echo needs agent_run, the
		// harp-remembering step, the payload-draining agent_recv and the per-engine
		// gate, all four of which already exist there. Copying them into a second
		// file would fork the one set of steps this journey's history has hardened.
		//
		// THE DRIFT CHECK SURVIVES A SHARED FILE, and it is worth saying why,
		// because the obvious worry — "the registry is now compared against every
		// scenario in a 400-line journey" — is real and happens to be answered.
		// featureCellKeys reads Examples blocks carrying engine AND runtime AND
		// workspace columns, by NAME. J002300's own per-engine floor tabulates
		// | engine | marker | and contributes nothing; P6's outline tabulates all
		// three and is compared exactly. The two coexist, in both directions, with
		// no change to the check itself.
		//
		// THE STANDING HAZARD, stated because it is invisible otherwise: if someone
		// later adds runtime/workspace columns to J002300's own per-engine floor,
		// those rows begin counting as P6 cells and this probe's drift test reds,
		// naming cells the registry does not declare. That red is CORRECT — it
		// means two probes have come to share a table shape — and the fix is a
		// column rename or a separate feature file, never deleting the check.
		Feature: "j002300_cross_engine_delegation.feature",
		Paid:    true,
		Cells: []probeCell{
			hostCell("claude-code", probeLiveVerified,
				"measured 2026-08-13 on this branch: 1 scenario / 11 steps green, the child echoed the minted harp `still-brave-ankle` as its whole body, and the coordinator's steer was on disk as in/consumed/…coord.md carrying that harp. RE-PROVES what J002300-LIVE-ECHO-TOKEN proves inside a LOCKED scenario; the two now guard the claim by different routes and this one asserts the spool substrate as well. Assertion-side mutation run (the verdict looking for harp+\"-MUTANT\") went RED with a BUS-DELIVERY shape."),
			hostCell("codex", probeLiveVerified,
				"measured 2026-08-13: FIRST live proof of coordinator→child mid-session steer on codex (inventory row 13 previously read 'marker only; no mid-session steer'). 1 scenario / 11 steps green, echo `elder-zippy-dad`, steer on disk in the child's in plane. Assertion-side mutation went RED with a BUS-DELIVERY shape."),
			hostCell("kiro", probeLiveVerified,
				"measured 2026-08-13: FIRST live proof of coordinator→child mid-session steer on kiro. 1 scenario / 11 steps green, echo `fluid-paced-badge`, steer on disk in the child's in plane. Assertion-side mutation went RED with a BUS-DELIVERY shape. NOTE this engine's P0 host/none cell is red-mapped for terminal decoration on one-shot stdout — that defect is in the run-output channel and does not touch the bus, which is why this cell is green while that one is not."),
			hostCell("opencode", probeLiveVerified,
				"measured 2026-08-13: FIRST live proof of coordinator→child mid-session steer on opencode, the engine whose delegated-child path shipped on the StartRun/runner model with no live round trip behind it. 1 scenario / 11 steps green, echo `slack-inept-prude`; slowest of the four (the echo turn landed ~79s after the steer), so do not shorten the 240s budget on its account. Assertion-side mutation went RED with a BUS-DELIVERY shape."),
			// THE ISOLATED CELL. Every row above runs host/none — isolated on
			// NEITHER axis — so until this one goes green, "delegation works"
			// and "delegation works across the isolation boundary" are
			// different claims and only the first is proven. That gap is what
			// uninvited-maternity is about: a container boundary nothing
			// exercises end to end.
			//
			// THE ISOLATED CELL, and the first proof that delegation survives
			// the isolation boundary. Every other row here runs host/none —
			// isolated on NEITHER axis — so until this one went green,
			// "delegation works" and "delegation works when isolated" were
			// different claims and only the first had evidence.
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "worktree",
				Status: probeLiveVerified,
				Reason: "measured 2026-08-24: 1 scenario / 11 steps green in 59s. A real claude-code child ran in a rootless container on an isolated worktree, reported wake marker P6-WAKE-MARKER-CLAUDE-CODE-CTRWT-9d4f21ab from its own composed context, then echoed back the harp `stony-worse-muck` that the coordinator steered into its LIVE session mid-flight, with the steer on disk in the child's own spool. FIVE RUNS TO GET HERE, and the four failures are the useful part because each was a different real gap, recorded in uninvited-maternity: (1) the fixture committed before ctxloom materialized its own managed files, so agent_run refused a dirty tree — fixed with dirty_tree_handler: copy, since the default \"commit\" handler needs dirty_tree_commit_ack, a human act no automated cell can perform; (2) the image build ran opencode's installer for a claude cell and died on GitHub's rate limit — see frosted-pony, and the cell now pins isolation_engines; (3) that pin went into the PROJECT config, where a machine-scoped key is dropped with a warning rather than applied; (4) container auth mounts the host's ~/.claude/.credentials.json READ-WRITE (rotation must write back), resolves it from $HOME which is the harness's fake home, and cannot use CLAUDE_CONFIG_DIR because claudeAuthEnvVars excludes it — fixed by SYMLINKING the real credential in, verified against docker directly. container-rootful is absent rather than declared-and-skipped: no box this suite runs on has a reachable rootful daemon."},
			// THE TWO MIXED CORNERS. host/none and container-rootless/worktree
			// prove the bus with both boundaries off and both on; these prove it
			// with exactly ONE present, which is the half a combination test
			// cannot reach. worktree alone relocates the child's files and its
			// config home; container alone relocates its process and its
			// credentials. Either could break the mail plane by itself.
			{Engine: "claude-code", Runtime: "host", Workspace: "worktree",
				Status: probeLiveVerified,
				Reason: "measured 2026-08-24: 11/11 steps in 15s, steer harp `sweet-jumpy-tiger` echoed back. THIS CORNER FOUND A BUG THE EXTREMES COULD NOT, which is the argument for it existing. Its first run failed with \"worktree isolation: no host claude credentials found to seed the per-agent config-home — the agent would start logged out\": both isolated axes resolve the credential from isolation.hostHomeDir ($HOME) by DIFFERENT mechanisms — a container BIND-MOUNTS the file (claudeCredentialMounts), a worktree SEEDS it (credentialSeedSpecs sourceFiles) — and only host+none reads the exported CLAUDE_CONFIG_DIR. host/none seeds no per-agent home and container/worktree was already covered by the runtime gate, so the defect lived exactly where ONE boundary is present. Fixed 5399eff5."},
			{Engine: "claude-code", Runtime: "container-rootless", Workspace: "none",
				Status: probeLiveVerified,
				Reason: "measured 2026-08-24: 11/11 steps in 29s. The container half without the worktree half — the child's process is isolated and its credential mounted, but it shares the project working directory. Completes the claude matrix: with host/none, host/worktree and container-rootless/worktree, coordinator<->child messaging is now proven on every combination of the two axes rather than only on the diagonal."},
		},
	},
	{
		Name:         probeP7,
		Title:        "resume recall: a harp planted in turn one's prompt must come back after teardown and ResumeSessionID",
		Capabilities: []int{14},
		Channel:      channelTurnOnePrompt,
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned, "coord.resumeCapableBackends and oneShotSupportedBackends both hold {claude-code, codex}"),
			hostCell("codex", probePlanned, "coord.resumeCapableBackends and oneShotSupportedBackends both hold {claude-code, codex}"),
			{Engine: "kiro", Runtime: "host", Workspace: "none", Status: probeGatedOut,
				Reason: "resolveResumeMode refuses loudly for kiro — resume is a declared absence, not an untested claim"},
			{Engine: "opencode", Runtime: "host", Workspace: "none", Status: probeGatedOut,
				Reason: "resolveResumeMode refuses loudly for opencode — resume is a declared absence, not an untested claim"},
		},
	},
	{
		Name:          probeP8,
		Title:         "transcript payload: after any cell, the canonical transcript must be non-empty, schema-enveloped, and carry the cell's harp",
		Capabilities:  []int{15},
		Channel:       channelComposedContext,
		AssertionSide: true,
		Paid:          false,
		Cells:         nil,
	},
	{
		Name:         probeP9,
		Title:        "version report: the installed binary's parsed version matches what a session records on sessions.Entry.EngineVersion",
		Capabilities: []int{16},
		Channel:      channelEngineBinary,
		Paid:         false,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned, "unpaid: versionCommand is a local exec, no model turn"),
			hostCell("codex", probePlanned, "unpaid: versionCommand is a local exec, no model turn"),
			hostCell("kiro", probePlanned, "unpaid: versionCommand is a local exec, no model turn"),
			hostCell("opencode", probePlanned, "unpaid: versionCommand is a local exec, no model turn"),
		},
	},
	{
		Name:          probePX,
		Title:         "foreign-harp absence: no other cell's minted harp may appear in this cell's output, delivered files or transcript",
		Capabilities:  nil, // asserts an ISOLATION property across cells, not one interface capability
		Channel:       channelForeignLedger,
		AssertionSide: true,
		Paid:          false,
		Cells:         nil,
	},
	{
		Name:         probePCmd,
		Title:        "slash-command INVOCATION (deliberately not built): does a delivered command actually run when the engine is asked for it",
		Capabilities: []int{9},
		Channel:      channelComposedContext,
		Paid:         true,
		Cells: func() []probeCell {
			const why = "headless invocation of a delivered slash command is not a uniform engine surface: claude -p \"/cmd\" is plausible, codex prompts are $CODEX_HOME-global, kiro conflates the command and skill dirs. Delivery BYTES stay proven hermetically by the golden tests; invocation becomes a rung when someone needs the claim."
			var cells []probeCell
			for _, e := range probeEngines {
				cells = append(cells, hostCell(e, probeDeferred, why))
			}
			return cells
		}(),
	},
	{
		Name:         probePSkill,
		Title:        "skill LOADING (deliberately not built): does a delivered skill actually load when the engine is asked for it",
		Capabilities: []int{10},
		Channel:      channelComposedContext,
		Paid:         true,
		Cells: func() []probeCell {
			const why = "same non-uniform surface as command invocation: there is no headless, engine-agnostic way to ask an engine to load a named skill and observe that it did. Delivery bytes stay hermetic (golden tests, SurfaceSkills across the launch wire); loading becomes a rung when someone needs the claim."
			var cells []probeCell
			for _, e := range probeEngines {
				cells = append(cells, hostCell(e, probeDeferred, why))
			}
			return cells
		}(),
	},
}

// p0Cells is the 24-cell floor that engine_isolation_matrix.feature already
// runs: four engines × (host|container-rootless|container-rootful) ×
// (none|worktree). container-rootless and container-rootful are ownership
// modes of the SAME containerization axis, not a fourth engine dimension — see
// isolation.IsContainerRuntimeAxis and the feature file's own header for why
// there is no bare "container" value.
//
// The two evidenced exceptions are written out rather than generated, because
// they carry MEASURED findings and a generated row cannot hold one.
func p0Cells() []probeCell {
	// THE CONTAINER ROWS ARE NO LONGER RED-MAPPED. The ruling put
	// them here red on purpose — containerized delegation had never been
	// demonstrably correct, and the map of which cells failed and how was the
	// measure of the container-auth work. That work landed ("container auth
	// keys on the engine"), and the coordinator then
	// ran all eight container cells serially: claude-code, codex and opencode
	// went GREEN on both container axes against real engines through the
	// real-home read-write credential mount; kiro's two gated, loudly, on its
	// own production limitation.
	//
	// So the red map is spent, and flipping it is exactly the conscious one-line
	// edit the completeness gate was built to force. What replaces it is not a
	// blanket assumption in the other direction: every one of the eight rows now
	// carries its own evidence or its own gate reason, written out below.
	//
	// THE EVIDENCE IS container-rootless SPECIFICALLY, not "container" in
	// general. It was measured before the runtime axis split into
	// container-rootless/container-rootful (2026-08-17); nothing about that
	// rename changed what ran. Every box this suite has run on has had a
	// reachable rootless docker daemon and no reachable rootful one (a host
	// has at most one — containercell.probeDocker asserts the exclusivity from
	// one `docker info`), so the runtime that answered these cells was
	// necessarily rootless. container-rootful is a DIFFERENT cell that shares
	// this one's fixture and assertion but not its evidence — see the
	// container-rootful loop below, which leaves those cells at the default
	// probeWired (wired, unverified) rather than borrowing this Reason.
	const containerEvidence = "coordinator serial chain 2026-08-13, task bpjje2q53, post auth-keying merge (303881ee): 1 scenario / 3 steps green against the real engine in a container, credentials through the real-home read-write mount. Measured with the PRE-HARP hex nonce — see the note below. REATTRIBUTED 2026-08-18 (task unvisited-magnolia) from the undifferentiated \"container\" axis to container-rootless specifically, following the ownership-axis split: this box has never had a reachable rootful docker daemon, so rootless is the only ownership this evidence could have measured."

	var cells []probeCell
	for _, e := range probeEngines {
		for _, rt := range []string{"host", "container-rootless", "container-rootful"} {
			for _, ws := range []string{"none", "worktree"} {
				cells = append(cells, probeCell{Engine: e, Runtime: rt, Workspace: ws, Status: probeWired})
			}
		}
	}

	for _, e := range []string{"claude-code", "codex", "opencode"} {
		for _, ws := range []string{"none", "worktree"} {
			setCell(cells, e, "container-rootless", ws, func(c *probeCell) {
				c.Status = probeLiveVerified
				c.Reason = containerEvidence
			})
			// container-rootful: deliberately left at the default probeWired
			// (a scenario exists and runs, but nobody has watched it go green)
			// rather than sharing containerEvidence — see that constant's own
			// doc for why the two ownership modes cannot share one measurement.
			// probeWired needs no Reason (enforced by
			// TestProbeRegistry_CellsAreWellFormedAndEvidenced), which is
			// itself the honest statement: there is nothing measured to say
			// yet. Move it to probeLiveVerified with its OWN evidence once a
			// runner with a reachable rootful daemon (or a rootful-invoked
			// podman) runs `just engine-matrix <engine> container-rootful
			// <workspace>` and it passes.
		}
	}

	// kiro's container axes: gated, and the gate is PRODUCTION'S, not the
	// harness's. credentialSeedSpecs["kiro"] marks XDG_DATA_HOME GatedOnCreds
	// with HonoursVarForCreds FALSE, because kiro's subscription credential is a
	// global sqlite that no HomeVar relocates — so ctxloom refuses to start
	// rather than silently hand the agent a fresh, logged-out data home, and
	// KIRO_API_KEY is the only key that opens the axis. That gate is about the
	// CREDENTIAL MECHANISM, not about who owns the container daemon, so it
	// applies identically on both ownership modes — the loop below covers both.
	//
	// Recorded as gated-out per the coordinator's ruling, with the nuance stated
	// rather than smoothed away: this is a CONDITIONAL gate, not a declared
	// absence. kiro's container axis works when KIRO_API_KEY is present; it is
	// unreachable on this box's subscription auth. That is why these rows
	// keep their Examples blocks (GateAtRuntime below) — the loud skip IS the
	// report, and deleting the rows would delete the only place the limitation
	// is stated where somebody runs into it.
	for _, rt := range []string{"container-rootless", "container-rootful"} {
		for _, ws := range []string{"none", "worktree"} {
			setCell(cells, "kiro", rt, ws, func(c *probeCell) {
				c.Status = probeGatedOut
				c.GateAtRuntime = true
				c.Reason = "gated 2026-08-13 in the coordinator's serial chain with production's own skip message: credentialSeedSpecs[\"kiro\"] marks XDG_DATA_HOME GatedOnCreds with HonoursVarForCreds FALSE (the subscription credential is a global sqlite no HomeVar relocates), so KIRO_API_KEY is the only key that opens this axis. CONDITIONAL, not a declared absence — with the key set, this cell runs. Applies identically on both container ownership modes (task unvisited-magnolia, 2026-08-18): the gate is about the credential mechanism, not about who owns the daemon."
			})
		}
	}

	// kiro host/none: RED, and the finding is NOT about the model. Measured:
	// kiro produced the requested object byte-perfectly and ctxloom
	// handed it back wrapped in TERMINAL DECORATION (an ANSI colour sequence
	// and an interactive "> " prompt marker), so a one-shot kiro run's stdout is
	// not machine-readable. That is a ctxloom-side channel defect, not an engine
	// one. The feature carries the same finding at its @wip tag.
	setCell(cells, "kiro", "host", "none", func(c *probeCell) {
		c.ExpectedFailure = shapeOutputFormat
		c.ExpectedFailureNote = "measured 2026-08-13: stdout was \\x1b[38;5;141m> \\x1b[0m{\"hello\":\"...\"}\\x1b[0m — the interactive prompt echo leaking into a non-interactive capture. DO NOT fix by stripping ANSI in the assertion: the contract under test is \"stdout IS the JSON\", and a matcher that launders the stream would report success here while every real consumer still breaks."
	})
	// opencode host/worktree: passes, but FLAKY, recorded rather than smoothed
	// over — two consecutive attempts, first failed, second passed.
	setCell(cells, "opencode", "host", "worktree", func(c *probeCell) {
		c.Status = probeLiveVerified
		c.Reason = "measured 2026-08-13 (with the PRE-HARP hex nonce — see the note below): green in 100s on the second of two consecutive attempts. The failing attempt dialled 127.0.0.1:1 — a placeholder reach-back address, not a live one (same family as the standup-death silence fixed at 2725325e). If this cell reds in a lane, check the dial address before blaming the engine."
	})
	// The ONE cell measured with the minted-harp nonce — see the note below.
	setCell(cells, "claude-code", "host", "none", func(c *probeCell) {
		c.Status = probeLiveVerified
		c.Reason = "the cheapest cell and the ladder's canary. Measured 2026-08-13 on this branch with the MINTED-HARP nonce: `just engine-matrix claude-code host none`, 1 scenario / 3 steps passed in 46s, no skip. Previously green on the j002300 per-engine live floor 2026-08-12 with the pre-harp hex nonce. RE-VERIFIED 2026-08-16 on the subscription lane, as one of a full four-cell claude sweep run right after the matrix narrowed to claude-only active rows (the other three engines moved to @wip in the feature, unchanged otherwise): passed in ~5.5s."
	})

	// claude-code host/worktree: never individually annotated before — the
	// generated row above carried Status probeWired and no Reason of its own.
	// Measured on the subscription lane, same sweep as host/none.
	setCell(cells, "claude-code", "host", "worktree", func(c *probeCell) {
		c.Status = probeLiveVerified
		c.Reason = "measured 2026-08-16 on the subscription lane, part of the same four-cell claude sweep as host/none (run right after the matrix narrowed to claude-only active rows): `just engine-matrix claude-code host worktree` passed in ~5.5s."
	})

	// claude-code's two container-rootless cells already carry the
	// coordinator-chain evidence (containerEvidence, above), which codex and
	// opencode's container-rootless cells share verbatim. Append claude's own
	// re-verification here rather than edit the shared constant,
	// since codex and opencode were not re-run at the same time and their
	// existing evidence must not be touched.
	setCell(cells, "claude-code", "container-rootless", "none", func(c *probeCell) {
		c.Reason += " RE-VERIFIED 2026-08-16 on the subscription lane, part of the same four-cell claude sweep: passed in 57s including the image build (measured before the container-rootless/container-rootful split; see containerEvidence's own reattribution note)."
	})
	setCell(cells, "claude-code", "container-rootless", "worktree", func(c *probeCell) {
		c.Reason += " RE-VERIFIED 2026-08-16 on the subscription lane, part of the same four-cell claude sweep: passed in 8s (measured before the container-rootless/container-rootful split; see containerEvidence's own reattribution note)."
	})
	return cells
}

// THE MINTED-HARP NONCE HAS NOW SURVIVED A LIVE RUN — once, on one cell.
//
// The swap from a hex nonce to a three-word harp changed the value planted in an
// agent's composed context and echoed back through a JSON string. Nothing about
// it SHOULD matter — the harp goes into the fixture bundle through %q and comes
// back out of a JSON string, both indifferent to hyphens — but "should not
// matter" is not a measurement, and this table's discipline is that a green
// claim names what was actually observed.
//
// So it was measured: claude-code host/none, on this branch. The
// cell ran (1 scenario, 3 steps, no skip), the harp reached the engine through
// composed context, and the engine echoed it back exactly. That closes the
// question for the delivery path every probe in the ladder shares.
//
// EVERY OTHER live-verified row above still records a PRE-HARP measurement and
// says so in its own reason. They are not invalidated — the swap is upstream of
// what they test — but they have not been re-observed. If one of them reds with
// a CONTEXT-DELIVERY failure while the engine is plainly healthy, the nonce is
// still the first thing to rule out, and matrixBundleYAML is where to look.

// RETRACTION: P1's CODEX HOOK FINDING WAS WRONG, AND THE WAY IT WAS
// WRONG IS THE MOST USEFUL THING THIS PROBE HAS PRODUCED.
//
// P1 first recorded, against the codex hook cell: "THE CODEX HOOK FINDING IS
// FIXED, and this cell is the measurement that says so." The cell was green, the
// pin validated, no degrade warning. Every word of the evidence was true and the
// conclusion did not follow. S4's hook-firing probe forced the re-check by
// measuring, independently, that codex hooks never fire under `codex exec` and
// that codex will satisfy a nonce probe by searching the workspace for the
// phrase.
//
// The re-adjudication needed no new paid turn, because the disproof was already
// in the first run's own captured stderr, unread:
//
//	ctxloom: warning: codex hooks and MCP servers were NOT written: codex
//	settings/prompts/skills are delivered per-session at launch; no durable
//	project home exists — see config_home. ... codex's cwd-keyed AGENTS.md
//	context is unaffected and was still written.
//
// There was no hook in that session. ctxloom said so, on the run that was cited
// as proof that the hook worked. Two independent facts, both now pinned
// hermetically in capability_context_channels_test.go, close it:
//
//  1. codex's SurfaceFor resolves (context, Hook) to a COMPOSED delivery that
//     ALSO writes the native AGENTS.md, which codex reads by itself. So even
//     with a hook installed, a green hook-pinned codex cell cannot attribute
//     anything to the hook.
//  2. On this fixture no codex hook is written at all.
//
// So the fragment-drop finding is REOPENED. It was never re-measured;
// a different channel answered and the answer was credited to the wrong one. The
// cell is deferred with what an attributing version would require.
//
// WHAT WENT WRONG METHODOLOGICALLY, stated plainly because it generalises. The
// verdict asked "did the pinned approach get selected" and never "did the
// mechanism get installed", and it read the model's answer instead of ctxloom's
// report. For a tool-using engine the model's answer is downstream of every
// channel at once, so it can never attribute one. approachRequiredSurfaceDelivered
// is the corrective, and it is retrospective: it exists because of this, not in
// anticipation of it.
//
// WHAT EVERY OTHER CONTEXT PROBE INHERITS — P0 INCLUDED. The nonce lives in a
// bundle file inside the project tree, and every engine except claude-at-
// system-prompt also has its context DELIVERED into the working directory (no
// out-of-cwd realization exists for codex, kiro or opencode). So for any
// tool-using engine, a nonce-echo context cell cannot separate "ctxloom
// delivered the context" from "the engine read the bytes off disk". P0's sixteen
// cells are all in this position. That is not a reason to delete them — they
// still prove the run completes, the isolation scheme survives, and the output
// contract holds — but "context delivery survived that isolation scheme" is a
// stronger claim than they can support, and P0's header currently makes it.
// Recorded here for S9/S11 rather than edited into P0's file mid-wave.
//
// P1's SURVIVING FINDING: CLAUDE'S HOOK CONTEXT APPROACH IS AN EMPTY
// DELIVERY — AND THE MINTED-HARP RULING IS WHAT CAUGHT IT.
//
// The cell is claude-code host/none at agent.ApproachHook: the agent binding
// pins `surfaces: {context: hook}`. Three consecutive runs, three freshly minted
// harps, one shape: exit 0, well-formed JSON, no trace of the nonce.
//
// THE MECHANISM, read from production rather than guessed from the answers:
// claude's Surfaces.SurfaceFor resolves (context, ApproachHook) to
// noopContextDelivery — "a documented no-op", justified by claude's APPLY path
// carrying context through the settings-borne inject hook plus a regenerated
// cache file. A LAUNCH is not that path: the context surface is the thing that
// would have written the cache file, and at this approach it writes nothing.
// TestClaudeHookApproach_DeliversNothing holds that fact so this attribution
// cannot rot the way the codex one did.
//
// So the finding is sharper than "the hook route is broken": a user-selectable
// config key elects a context delivery of ZERO BYTES, and the session launches
// and reports success. That is this project's characteristic bug — exit 0, no
// payload — sitting in the delivery layer, reachable from config.yaml.
//
// WHAT IT IS NOT. Not a degrade: no degrade marker appeared on stderr, so the
// pin reached the wire (approachPinHonoured is the check that tells those
// apart). Not an unwritten hook surface either — claude has a durable project
// home, so unlike codex its hook IS written; approachRequiredSurfaceDelivered
// stays silent here, correctly. Not an empty assembly: the same stderr reports
// "context: 2 fragment(s), ~336 tokens". And not the output contract — the JSON
// was perfect every time.
//
// NOW THE PART THAT MATTERS BEYOND THIS CELL. Two of the three runs answered
// with that run's OWN SESSION HARP — {"hello":"bumpy-stony-sixth"} and
// {"hello":"mere-teal-jet"} — a value ctxloom had just printed in its
// start-session banner and exports as CTXLOOM_SESSION_HARP. The model, asked for
// "the nonce string that appears in the additional context", had no context,
// found a plausible three-word phraselet in its ambient environment, and returned
// that.
//
// Under the PRE-RULING design, where the planted nonce WAS the session's own
// harp, this cell would have gone green. It would have reported that claude's
// hook route delivers context, on the strength of the engine reading a value the
// hook never carried. That is precisely the ambient-channel false green the
// minted-harp ruling was made to rule out (probe_assert.go's header
// argues it a priori; this is the measurement). The ruling has now paid for
// itself once, on the second probe to use it.
//
// The cell stays exactly as strict as it is. It was NOT relaxed to accept a
// run whose context arrived some other way: this cell's whole subject is
// which way.
//
// RESOLVED. Root cause, read from production rather than guessed:
// agent.LaunchBackend.deliverSet's SharedCell delivery loop only installed the
// SessionStart injection hook (recoverContextViaHook) when a surface's write
// returned a non-nil error. claude's ApproachHook context surface
// (noopContextDelivery) "succeeds" with a nil error and a nil handle BY
// DESIGN, so nothing on the success path ever installed the hook — the
// mechanism selected, the context composed, the hook fired (S4's P3 probe),
// and still nothing reached the model, exactly as measured. Fixed by
// factoring the hash-materialize-and-append-hook logic into
// installContextInjectionHook and calling it from deliverSet whenever a
// SharedCell resolves SurfaceContext at ApproachHook, mirroring the existing
// failure-triggered fallback. Measured: green with nonce harp
// "obese-hilly-gusto" echoed exactly; mutation-confirmed by reverting the fix
// and reproducing the identical CONTEXT-DELIVERY shape live (nonce
// "aloof-dire-reach", well-formed JSON, no trace of it) before restoring it.
//
// A CORRECTION to the "ambient environment" reading above. A later re-run
// against the still-broken code produced a FOURTH shape:
// {"hello":"prone-wide-deity"} for nonce harp "pale-young-getup" — a string
// that is not any nonce minted in that run and appears nowhere ctxloom
// emitted it. The model INVENTED a harp-shaped value unprompted; it did not
// need to read one from its ambient environment. So "found a plausible
// phraselet in its ambient environment" explains only the two answers above
// that were verifiably that run's own CTXLOOM_SESSION_HARP — it is not the
// general mechanism. Two consequences: a future matcher must check the EXACT
// minted value, never harp SHAPE alone (a shape check would have been
// vacuously green on the invented answer too); and the false-green risk this
// cell's minted-harp design closes remains real for the session-harp leak
// channel specifically, which the two matching answers still demonstrate.

// setCell applies fn to the one cell matching engine/runtime/workspace. It
// PANICS when the cell is not there: this runs at package init, and a silent
// miss would drop a measured finding from the table while leaving the table
// looking complete — the exact failure mode this registry exists to prevent.
func setCell(cells []probeCell, engine, runtime, workspace string, fn func(*probeCell)) {
	for i := range cells {
		c := &cells[i]
		if c.Engine == engine && c.Runtime == runtime && c.Workspace == workspace && c.Variant == "" {
			fn(c)
			return
		}
	}
	panic(fmt.Sprintf("capability probe registry: no cell [engine=%s runtime=%s workspace=%s] to annotate — a measured finding would have been silently dropped", engine, runtime, workspace))
}

// p4Cells is the plan-sentinel ladder: four engines, each with the cell under
// test and its own positive control. Eight rows, eight paid turns, and the
// pairing is the design — see capability_plan_sentinel.feature's header and
// probe_p4_plan_sentinel.go's.
//
// WHY THE CONTROLS ARE ROWS AND NOT AN IMPLEMENTATION DETAIL. A control that
// lives inside the plan cell's scenario is invisible: nobody can address it,
// nobody can see whether it ran, and its failure would surface as "the plan cell
// is red" rather than as "the probe is broken". Declared as cells, each control
// is addressable (`@var-control`), its status is recorded beside the claim it
// underwrites, and the completeness gate forces its existence to be typed out —
// which matters more here than anywhere else in the ladder, because P4 is the
// one rung whose claim is NEGATIVE. Delete the control rows and the plan rows go
// on passing forever, measuring nothing, with no red anywhere to say so.
//
// HOST/NONE ONLY, and the two absences are declared rather than left blank. A
// container cell would need the sentinel observed from outside the container
// after the run, and a worktree cell would move the sentinel out from under the
// assertion's own path; both are different fixtures, not different Examples
// rows. They are recorded as deferred on the p4 rows' own reasons rather than as
// silently missing axes.
func p4Cells() []probeCell {
	// Why each engine is here, in its own words. The first two rows PORT proofs
	// that already exist but are unrepeatable; the second two make a claim that
	// has never been checked against a running binary at all.
	why := map[string]string{
		"claude-code": "ports the AD HOC live proof recorded at enforcesReadOnlyPlan (2026-07-15, sentinel denied) into a repeatable cell. Production surface: permissionArgs maps plan to --permission-mode plan plus an explicit --disallowedTools Bash,Edit,Write,NotebookEdit.",
		"kiro":        "ports the AD HOC live proof recorded at enforcesReadOnlyPlan (2026-07-15, sentinel + positive controls) into a repeatable cell. Production surface: kiro.buildArgs maps plan to --trust-tools=fs_read.",
		"codex":       "--sandbox read-only is asserted host-side and has never been live-run. Production surface: codex.buildArgs maps plan to --sandbox read-only.",
		"opencode":    "the written permission {edit:deny,bash:deny} is asserted stricter than opencode's own plan agent and has never been live-run. Production surface: opencode's interactiveManaged/chat managed config sets readOnly for plan.",
	}
	const controlWhy = "the bypass positive control: an unchanged file is equally consistent with a posture that refused the write and with a run that never attempted one, so the control's success is part of the plan cell's assertion (p4AssertPlan consults the control ledger and reds when the control is dead)."

	var cells []probeCell
	for _, e := range probeEngines {
		// Control first, matching the feature's own block order: the plan arm
		// reads a record the control has to have written.
		cells = append(cells,
			probeCell{Engine: e, Runtime: "host", Workspace: "none", Variant: string(p4Control),
				Status: probeWired, Reason: controlWhy},
			probeCell{Engine: e, Runtime: "host", Workspace: "none", Variant: string(p4Plan),
				Status: probeWired, Reason: why[e]},
		)
	}

	// THE CLAUDE-CODE PAIR HAS BEEN RUN, AS A PAIR, AND THAT IS THE ONLY WAY IT
	// COUNTS. Verified on this branch: `just plan-sentinel
	// claude-code pair`, 2 scenarios / 6 steps passed, no skip, control 12.9s
	// then plan 8.1s in one process — so the plan cell read a control record
	// rather than printing its PROVISIONAL note, which is itself the observable
	// that the pairing works.
	//
	// What each run showed, per the EVIDENCE line the When step prints:
	//
	//   control (harp legal-rosy-pouch):   exit=0, 55B stdout,  plan-oneshot-warning=false
	//   plan    (harp proud-saucy-amino):  exit=0, 273B stdout, plan-oneshot-warning=true
	//
	// The warning flag is the independent confirmation that the ONE line of
	// config.yaml separating the pair really reached the resolver:
	// warnPlanOneshotCancels fires only when a plan posture survives
	// resolvePermissionMode's ONESHOT floor into the run, and it fired on
	// exactly the cell that bound `permissions: plan`. So the two runs differed
	// in posture and not merely in outcome — which is the question a lone green
	// plan cell could never answer.
	//
	// Also measured, and worth writing down because the verdict deliberately
	// tolerates the opposite: claude's plan one-shot exits ZERO. The refusal is
	// reported in the turn's prose, not in the exit status. p4RunHappened's
	// choice not to gate on the exit code was therefore not needed HERE — it is
	// insurance for the three engines whose plan behaviour is still unobserved,
	// and it stays.
	setP4Cell(cells, "claude-code", p4Control,
		"live-verified 2026-08-13 as HALF OF A PAIR (`just plan-sentinel claude-code pair`, 2 scenarios / 6 steps, no skip): under permissions=bypass the ordered overwrite LANDED — the sentinel planted with harp legal-rosy-pouch came back carrying the overwrite token instead. exit=0, 55B stdout, plan-oneshot-warning=false (correctly absent: the control is not a plan run). This is what licenses the plan cell beside it.")
	setP4Cell(cells, "claude-code", p4Plan,
		"live-verified 2026-08-13 as HALF OF A PAIR, same process and same fixture as its control, differing in one line of config.yaml: under permissions=plan the sentinel planted with harp proud-saucy-amino came back BYTE-UNCHANGED after the engine was ordered to overwrite it. exit=0 (claude reports the refusal in prose, not in its exit status), 273B stdout, plan-oneshot-warning=true — production's own warnPlanOneshotCancels confirming the plan posture survived the ONESHOT floor into this run, on exactly the cell that bound it. Replaces the AD HOC 2026-07-15 terminal proof recorded at the descriptor's enforcesReadOnlyPlan with something anybody can re-run.")

	// CODEX: THE PAIR RAN AND BOTH HALVES PASSED, which makes this the first
	// live evidence anywhere that codex's --sandbox read-only actually stops a
	// write. The claim had only ever been asserted host-side, against the argv
	// codex.buildArgs produces, which proves what ctxloom TYPED and nothing
	// about what codex then did with it.
	//
	// `just plan-sentinel codex pair`: 2 scenarios / 6 steps, no skip.
	//
	//   control (harp silly-wide-many):  exit=0, 18B stdout,  plan-oneshot-warning=false
	//   plan    (harp pure-true-flyer):  exit=0, 52B stdout,  plan-oneshot-warning=true
	//
	// The warning flag is again the independent witness that the pair differed
	// in POSTURE and not merely in outcome: warnPlanOneshotCancels fires only
	// when a plan posture survives resolvePermissionMode's ONESHOT floor into
	// the run, and it fired on exactly the cell that bound `permissions: plan`.
	//
	// Codex ALSO exits zero under plan, like claude — so two of the four
	// engines now report the refusal in prose rather than in the exit status,
	// and p4RunHappened's decision not to gate on the exit code has been the
	// right one on every engine measured so far.
	setP4Cell(cells, "codex", p4Control,
		"live-verified 2026-08-13 as HALF OF A PAIR (`just plan-sentinel codex pair`, 2 scenarios / 6 steps, no skip): under permissions=bypass the ordered overwrite LANDED — the sentinel planted with harp silly-wide-many came back carrying the overwrite token instead. exit=0, 18B stdout, plan-oneshot-warning=false (correctly absent: the control is not a plan run). This is what licenses the plan cell beside it.")
	setP4Cell(cells, "codex", p4Plan,
		"live-verified 2026-08-13 as HALF OF A PAIR, same process and same fixture as its control, differing in one line of config.yaml: under permissions=plan the sentinel planted with harp pure-true-flyer came back BYTE-UNCHANGED after the engine was ordered to overwrite it. exit=0 (codex, like claude, reports the refusal in prose rather than in its exit status), 52B stdout, plan-oneshot-warning=true — production's own warnPlanOneshotCancels confirming the plan posture reached this run and not its control. FIRST live evidence that codex.buildArgs' --sandbox read-only actually stops a write; before this the claim was asserted host-side against the argv only, which proves what ctxloom typed and nothing about what codex did with it.")

	// KIRO: BOTH ARMS RED, and the red is a CAPABILITY FINDING about the
	// engine, not the ANSI-decoration defect its P0 row carries. That defect is
	// in kiro's one-shot STDOUT; P4 asserts a FILE and is immune to it, exactly
	// as P3's green cell is.
	//
	// What was measured, `just plan-sentinel kiro pair`, 2 scenarios / 6 steps,
	// 4 passed 2 failed, no skip:
	//
	//   control (harp smart-mean-snort):  exit=0, 137B stdout, plan-oneshot-warning=false
	//   plan    (harp icky-local-gore):   exit=0, 136B stdout, plan-oneshot-warning=true
	//
	// In BOTH arms kiro's own stdout says, naming the fixture's absolute path:
	//
	//   > I wrote the file /tmp/ctxloom-integration-.../project/plan-sentinel.txt
	//     with the requested content.
	//
	// and in BOTH arms the sentinel came back BYTE-UNCHANGED, still holding its
	// planted harp. Tools were not the obstacle: kiro's own banner on the same
	// stderr reads "All tools are now trusted (!). Kiro will execute tools
	// without asking for confirmation." So under BYPASS, with every tool
	// trusted, kiro reported a write it did not make — this project's
	// characteristic silent no-op, arriving from the engine side.
	//
	// AND THE PAIRING EARNED ITS KEEP A SECOND TIME. The plan arm's sentinel
	// survived. Read alone it is a textbook green: ordered to overwrite,
	// unchanged. It reds only because p4AssertPlan consults the control ledger
	// and finds this engine's control dead — which is precisely the false green
	// the control rows were declared to rule out, now demonstrated on a real
	// engine rather than argued.
	//
	// DO NOT loosen either cell. There is nothing to loosen: the file was
	// untouched and the engine said otherwise. They go green when a kiro run
	// under bypass actually writes the file it claims to have written.
	redMapP4Cell(cells, "kiro", p4Control, shapeControlDead,
		"MEASURED 2026-08-13, `just plan-sentinel kiro pair`. Under permissions=bypass, with kiro's own banner reporting \"All tools are now trusted (!)\", the ordered overwrite DID NOT LAND: the sentinel planted with harp smart-mean-snort came back byte-unchanged while kiro's stdout said \"I wrote the file /tmp/ctxloom-integration-.../project/plan-sentinel.txt with the requested content\". exit=0, 137B stdout. Not the ANSI-decoration defect its P0 row carries — that one is stdout-shaped and this assertion is on a FILE, the same immunity P3's green kiro cell has. Isolating it against the vendor binary directly (does kiro's fs_write reach the filesystem in a one-shot at all, or is the write hallucinated with no tool call?) is UNDONE — see DEFERRED below.")
	redMapP4Cell(cells, "kiro", p4Plan, shapeControlDead,
		"MEASURED 2026-08-13, same process as its control. The sentinel planted with harp icky-local-gore came back BYTE-UNCHANGED — read alone, a textbook green — and this cell reds anyway, because p4AssertPlan consults the control ledger and this engine's control is dead. That is the pairing working: kiro's plan-arm stdout ALSO claims \"I wrote the file ... with the specified content\", so an unchanged sentinel here is equally consistent with a posture that refused and with an engine that never writes anything. exit=0, 136B stdout, plan-oneshot-warning=true (the posture did reach the run). This cell says nothing about kiro's --trust-tools=fs_read until the control lands.")

	// OPENCODE: THE CONTROL LANDED, TWICE; THE PLAN ARM DIED BEFORE THE ENGINE
	// SPOKE, TWICE, AND THE TWO DEATHS HAD DIFFERENT CAUSES.
	//
	// Run once, then run again precisely because the first cause looked
	// determinate and worth confirming. `just plan-sentinel opencode pair`,
	// 2 scenarios / 6 steps, 5 passed 1 failed, both times:
	//
	//   run 1  control (harp dense-same-blade):   exit=0, 18B stdout — LANDED
	//          plan    (harp gruff-spicy-mulch):  exit=1, 0B stdout
	//            502 Upstream error from Darkbloom: "model emitted an
	//            undeclared tool call" (error_type provider_unavailable)
	//   run 2  control (harp peaky-neat-rule):    exit=0, 18B stdout — LANDED
	//          plan    (harp zippy-lean-jiffy):   exit=1, 0B stdout
	//            502 Upstream error from Darkbloom: all providers for model
	//            "gpt-oss-20b" are at capacity (queue timeout)
	//
	// WHY THE SECOND RUN WAS BOUGHT, and what it settled. Run 1's cause was
	// interesting rather than boring: opencode's plan posture writes
	// {edit:deny,bash:deny}, so the write tool is not declared to the model,
	// and "model emitted an undeclared tool call" would then be the plan
	// posture PROVOKING a provider-level error rather than producing a graceful
	// refusal — a real finding if it reproduced. It did not: run 2 died on a
	// capacity queue timeout instead. So the sub-cause is NOT determinate and
	// the hypothesis is unproven, neither confirmed nor refuted.
	//
	// WHAT IS REPRODUCIBLE is the outer shape: the plan arm produces NO stdout
	// at all and exits 1, twice, while its control speaks and writes in the
	// same process minutes earlier. Whether the plan arm is genuinely more
	// fragile or merely went second both times is open.
	//
	// NOT LOOSENED. The verdict already tolerates a nonzero exit; what it will
	// not tolerate is SILENCE, because the prompt ends with an unconditional
	// one-sentence report and an engine that got the turn says something either
	// way. An untouched sentinel after a process that died before speaking
	// proves nothing about the posture, which is the whole reason this reds.
	setP4Cell(cells, "opencode", p4Control,
		"live-verified 2026-08-13, and TWICE: under permissions=bypass the ordered overwrite LANDED on both runs of `just plan-sentinel opencode pair` (harps dense-same-blade then peaky-neat-rule, exit=0, 18B stdout, plan-oneshot-warning=false each time). The control's own claim — bypass lands the write — is fully measured. Note it does NOT license the plan cell beside it here, because that cell never produced a run to license: see its red map.")
	redMapP4Cell(cells, "opencode", p4Plan, shapeSilentNoOp,
		"MEASURED TWICE 2026-08-13 and red both times with the same shape and DIFFERENT causes: the run produced no stdout at all and exited 1, while its control landed the write in the same process. Run 1 (harp gruff-spicy-mulch): 502 from the provider, \"model emitted an undeclared tool call\", error_type provider_unavailable. Run 2 (harp zippy-lean-jiffy): 502, all providers for model \"gpt-oss-20b\" at capacity (queue timeout). The second run was bought deliberately because run 1's cause would have been a finding if it reproduced — opencode's plan posture writes {edit:deny,bash:deny}, so an undeclared-tool-call error could be the posture provoking a provider fault instead of a graceful refusal. It did not reproduce, so that reading is unproven. What IS reproducible is the outer shape. NOT loosened: the verdict tolerates a nonzero exit by design, but not silence — the prompt ends with an unconditional one-sentence report, so a process that died before the engine spoke leaves an untouched sentinel proving nothing about the posture.")

	return cells
}

// setP4Cell is p4Cells' own annotator, and it panics on a miss for the same
// reason setCell does: these are MEASURED findings, and a silent miss would drop
// one while leaving the table looking complete. Separate from setCell because a
// P4 cell is identified by its VARIANT — setCell matches only variant-less rows,
// so calling it here would find nothing at all and panic for a confusing reason
// rather than the real one.
func setP4Cell(cells []probeCell, engine string, posture p4Posture, reason string) {
	for i := range cells {
		c := &cells[i]
		if c.Engine == engine && c.Variant == string(posture) {
			c.Status = probeLiveVerified
			c.Reason = reason
			return
		}
	}
	panic(fmt.Sprintf("capability probe registry: no p4 cell [engine=%s variant=%s] to annotate — a measured live verdict would have been silently dropped", engine, posture))
}

// redMapP4Cell records a P4 cell that was RUN and came back RED, with the shape
// it produced and the story behind it. The status stays wired on purpose: the
// cell runs, it is expected to fail, and a sweep diffs its shape rather than
// counting it — which is what separates a recorded finding from a cell somebody
// quietly deleted because it was inconvenient.
//
// It panics on a miss for the same reason setP4Cell does, and the reason bites
// harder here: a dropped red is a finding that stops being reported while the
// table goes on looking complete.
func redMapP4Cell(cells []probeCell, engine string, posture p4Posture, shape probeShape, note string) {
	for i := range cells {
		c := &cells[i]
		if c.Engine == engine && c.Variant == string(posture) {
			c.Status = probeWired
			c.ExpectedFailure = shape
			c.ExpectedFailureNote = note
			return
		}
	}
	panic(fmt.Sprintf("capability probe registry: no p4 cell [engine=%s variant=%s] to red-map — a measured failure shape would have been silently dropped", engine, posture))
}

// --- derived views ------------------------------------------------------------

// ID returns the cell's addressable identity under the given probe.
func (c probeCell) ID(probe string) probeCellID {
	return probeCellID{Probe: probe, Engine: c.Engine, Runtime: c.Runtime, Workspace: c.Workspace, Variant: c.Variant}
}

// Tags is the cell's gherkin tag line: the addressing idiom
// isolation_probe.feature established, extended with the probe name so
// `just capability-probe <probe> <engine> <runtime> <workspace>` selects
// exactly one cell.
//
// A tag may not contain whitespace — the gherkin parser rejects the whole FILE
// on one that does, taking every scenario in it with it — so the completeness
// test checks that property on every tag this returns.
func (c probeCell) Tags(probe string) []string {
	tags := []string{"@live", "@probe-" + probe, "@" + c.Engine, "@" + c.Runtime, "@ws-" + c.Workspace}
	if c.Variant != "" {
		tags = append(tags, "@var-"+c.Variant)
	}
	return tags
}

// TagExpression is the ACCEPTANCE_TAGS value that selects exactly this cell.
func (c probeCell) TagExpression(probe string) string {
	return strings.Join(c.Tags(probe), " && ")
}

// probeSpecByName returns the registry row named name.
func probeSpecByName(name string) (probeSpec, bool) {
	for _, p := range probeRegistry {
		if p.Name == name {
			return p, true
		}
	}
	return probeSpec{}, false
}

// probeCapabilityCoverage maps each inventory row number to the probes that
// claim to reach it, in registry order.
func probeCapabilityCoverage() map[int][]string {
	out := map[int][]string{}
	for _, p := range probeRegistry {
		for _, n := range p.Capabilities {
			out[n] = append(out[n], p.Name)
		}
	}
	return out
}

// probePaidCellCount is the ladder's cost in cells: how many cells would
// actually buy a model turn if everything currently planned or wired ran.
// Gated-out and deferred cells cost nothing because they never run.
func probePaidCellCount() int {
	n := 0
	for _, p := range probeRegistry {
		if !p.Paid {
			continue
		}
		for _, c := range p.Cells {
			if c.Status == probeGatedOut || c.Status == probeDeferred {
				continue
			}
			n++
		}
	}
	return n
}

// formatProbeRegistryReport renders the ladder as a stable, diffable table —
// the same role formatLiveEngineReport plays for engine availability, and the
// starting point for the sweep report. Sorted within a probe so two runs of the
// same registry produce byte-identical output.
func formatProbeRegistryReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "capability probe ladder: %d probes, %d paid cells\n", len(probeRegistry), probePaidCellCount())
	for _, p := range probeRegistry {
		fmt.Fprintf(&b, "  %s — %s\n", p.Name, p.Title)
		if p.AssertionSide {
			fmt.Fprintf(&b, "    (assertion-side: folds into other probes' cells, buys none of its own)\n")
		}
		cells := append([]probeCell(nil), p.Cells...)
		sort.SliceStable(cells, func(i, j int) bool {
			a, z := cells[i], cells[j]
			if a.Engine != z.Engine {
				return a.Engine < z.Engine
			}
			if a.Runtime != z.Runtime {
				return a.Runtime < z.Runtime
			}
			if a.Workspace != z.Workspace {
				return a.Workspace < z.Workspace
			}
			return a.Variant < z.Variant
		})
		for _, c := range cells {
			line := fmt.Sprintf("    %-12s %s", c.Status, c.ID(p.Name))
			if c.ExpectedFailure != "" {
				line += fmt.Sprintf(" RED-MAPPED(%s)", c.ExpectedFailure)
			}
			if c.Reason != "" {
				line += " — " + c.Reason
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
