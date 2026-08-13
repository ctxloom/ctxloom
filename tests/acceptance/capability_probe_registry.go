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
// it. The container rows were the worked example: added red under the 2026-08-12
// ruling because containerized delegation had never been demonstrably correct,
// then flipped to live-verified on 2026-08-13 once container auth keying landed
// and all eight were run. Only one red map is left standing (kiro host/none's
// ANSI-decoration finding). Flipping one is a one-line edit per cell that the
// completeness gate forces somebody to make consciously — which is the entire
// reason the shapes are written down instead of assumed.
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
	Runtime   string // host | container
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
		Paid:         true,
		Cells: []probeCell{
			{Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: "system-prompt",
				Status: probePlanned, Reason: "agent.ApproachSystemPrompt is claude-only (--append-system-prompt-file); no test selects it live today"},
			{Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probePlanned},
			{Engine: "claude-code", Runtime: "host", Workspace: "worktree", Variant: "unsafe-file-shared",
				Status: probePlanned, Reason: "the SharedRealization out-of-cwd writers (claude.NewSurfaces) are the one race-safe shared-cwd conversion; the worktree axis is where that matters"},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probePlanned, Reason: "must settle the 2026-07-14 finding recorded on liveAgents[\"codex\"]: profile fragments dropped from the hook's cache file. This cell either reconfirms it or records that it is fixed."},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "unsafe-file",
				Status: probePlanned, Reason: "codex's AGENTS.md route is compositional with its hook; the cell proves the turn carries the nonce under the pinned approach"},
			{Engine: "kiro", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeGatedOut, Reason: "kiro.WithContextHook fails loudly — the engine declares no hook context route"},
			{Engine: "opencode", Runtime: "host", Workspace: "none", Variant: "hook",
				Status: probeGatedOut, Reason: "opencode declares no hook surface at all (noHooksReason)"},
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
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned, ""),
			hostCell("codex", probePlanned, ""),
			hostCell("kiro", probePlanned, ""),
			hostCell("opencode", probePlanned, ""),
			{Engine: "claude-code", Runtime: "container", Workspace: "none", Status: probeDeferred,
				Reason: "container MCP reach-back is undesigned — the endpoint DISCOVERY gap, not the transport (cross-container-comms finding). Deferred rather than red so an undesigned thing is not measured as a defect."},
			{Engine: "codex", Runtime: "container", Workspace: "none", Status: probeDeferred,
				Reason: "container MCP reach-back is undesigned — see the claude-code container row"},
			{Engine: "kiro", Runtime: "container", Workspace: "none", Status: probeDeferred,
				Reason: "container MCP reach-back is undesigned — see the claude-code container row"},
			{Engine: "opencode", Runtime: "container", Workspace: "none", Status: probeDeferred,
				Reason: "container MCP reach-back is undesigned — see the claude-code container row"},
		},
	},
	{
		Name:         probeP3,
		Title:        "hook firing: the vendor binary executes the session_start hook ctxloom wrote, proven by the hook's own stamp file",
		Capabilities: []int{6, 7},
		Channel:      channelHookStamp,
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned, ""),
			hostCell("codex", probePlanned, ""),
			hostCell("kiro", probePlanned, ""),
			{Engine: "opencode", Runtime: "host", Workspace: "none", Status: probeGatedOut,
				Reason: "opencode declares hooks absent: noHooksReason. Not a gap — a declared absence."},
			{Engine: "codex", Runtime: "host", Workspace: "none", Variant: "session-end", Status: probeGatedOut,
				Reason: "unsupportedHookKinds[bundles.HookEventSessionEnd] — codex declares this one kind unsupported while supporting the rest"},
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
		Paid:         true,
		Cells: []probeCell{
			hostCell("claude-code", probePlanned,
				"the existing live proof is j002300's J002300-LIVE-ECHO-TOKEN step; this row PORTS it into the outline. Do not rewrite the locked scenario."),
			hostCell("codex", probePlanned, "child→parent is proven by j002300; coordinator→child mid-session steer is not"),
			hostCell("kiro", probePlanned, "child→parent is proven by j002300; coordinator→child mid-session steer is not"),
			hostCell("opencode", probePlanned, "child→parent is proven by j002300; coordinator→child mid-session steer is not"),
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

// p0Cells is the 16-cell floor that engine_isolation_matrix.feature already
// runs: four engines × (host|container) × (none|worktree).
//
// The two evidenced exceptions are written out rather than generated, because
// they carry MEASURED findings and a generated row cannot hold one.
func p0Cells() []probeCell {
	// THE CONTAINER ROWS ARE NO LONGER RED-MAPPED. The 2026-08-12 ruling put
	// them here red on purpose — containerized delegation had never been
	// demonstrably correct, and the map of which cells failed and how was the
	// measure of the container-auth work. That work landed at this base
	// (303881ee, "container auth keys on the engine"), and the coordinator then
	// ran all eight container cells serially: claude-code, codex and opencode
	// went GREEN on both container axes against real engines through the
	// real-home read-write credential mount; kiro's two gated, loudly, on its
	// own production limitation.
	//
	// So the red map is spent, and flipping it is exactly the conscious one-line
	// edit the completeness gate was built to force. What replaces it is not a
	// blanket assumption in the other direction: every one of the eight rows now
	// carries its own evidence or its own gate reason, written out below.
	const containerEvidence = "coordinator serial chain 2026-08-13, task bpjje2q53, post auth-keying merge (303881ee): 1 scenario / 3 steps green against the real engine in a container, credentials through the real-home read-write mount. Measured with the PRE-HARP hex nonce — see the note below."

	var cells []probeCell
	for _, e := range probeEngines {
		for _, rt := range []string{"host", "container"} {
			for _, ws := range []string{"none", "worktree"} {
				cells = append(cells, probeCell{Engine: e, Runtime: rt, Workspace: ws, Status: probeWired})
			}
		}
	}

	for _, e := range []string{"claude-code", "codex", "opencode"} {
		for _, ws := range []string{"none", "worktree"} {
			setCell(cells, e, "container", ws, func(c *probeCell) {
				c.Status = probeLiveVerified
				c.Reason = containerEvidence
			})
		}
	}

	// kiro's container axes: gated, and the gate is PRODUCTION'S, not the
	// harness's. credentialSeedSpecs["kiro"] marks XDG_DATA_HOME GatedOnCreds
	// with HonoursVarForCreds FALSE, because kiro's subscription credential is a
	// global sqlite that no HomeVar relocates — so ctxloom refuses to start
	// rather than silently hand the agent a fresh, logged-out data home, and
	// KIRO_API_KEY is the only key that opens the axis.
	//
	// Recorded as gated-out per the coordinator's ruling, with the nuance stated
	// rather than smoothed away: this is a CONDITIONAL gate, not a declared
	// absence. kiro's container axis works when KIRO_API_KEY is present; it is
	// unreachable on this box's subscription auth. That is why these two rows
	// keep their Examples blocks (GateAtRuntime below) — the loud skip IS the
	// report, and deleting the rows would delete the only place the limitation
	// is stated where somebody runs into it.
	for _, ws := range []string{"none", "worktree"} {
		setCell(cells, "kiro", "container", ws, func(c *probeCell) {
			c.Status = probeGatedOut
			c.GateAtRuntime = true
			c.Reason = "gated 2026-08-13 in the coordinator's serial chain with production's own skip message: credentialSeedSpecs[\"kiro\"] marks XDG_DATA_HOME GatedOnCreds with HonoursVarForCreds FALSE (the subscription credential is a global sqlite no HomeVar relocates), so KIRO_API_KEY is the only key that opens this axis. CONDITIONAL, not a declared absence — with the key set, this cell runs."
		})
	}

	// kiro host/none: RED, and the finding is NOT about the model. Measured
	// 2026-08-13: kiro produced the requested object byte-perfectly and ctxloom
	// handed it back wrapped in TERMINAL DECORATION (an ANSI colour sequence
	// and an interactive "> " prompt marker), so a one-shot kiro run's stdout is
	// not machine-readable. That is a ctxloom-side channel defect, not an engine
	// one. The feature carries the same finding at its @wip tag.
	setCell(cells, "kiro", "host", "none", func(c *probeCell) {
		c.ExpectedFailure = shapeOutputFormat
		c.ExpectedFailureNote = "measured 2026-08-13: stdout was \\x1b[38;5;141m> \\x1b[0m{\"hello\":\"...\"}\\x1b[0m — the interactive prompt echo leaking into a non-interactive capture. DO NOT fix by stripping ANSI in the assertion: the contract under test is \"stdout IS the JSON\", and a matcher that launders the stream would report success here while every real consumer still breaks."
	})
	// opencode host/worktree: passes, but FLAKY, recorded rather than smoothed
	// over — two consecutive attempts 2026-08-13, first failed, second passed.
	setCell(cells, "opencode", "host", "worktree", func(c *probeCell) {
		c.Status = probeLiveVerified
		c.Reason = "measured 2026-08-13 (with the PRE-HARP hex nonce — see the note below): green in 100s on the second of two consecutive attempts. The failing attempt dialled 127.0.0.1:1 — a placeholder reach-back address, not a live one (same family as the standup-death silence fixed at 2725325e). If this cell reds in a lane, check the dial address before blaming the engine."
	})
	// The ONE cell measured with the minted-harp nonce — see the note below.
	setCell(cells, "claude-code", "host", "none", func(c *probeCell) {
		c.Status = probeLiveVerified
		c.Reason = "the cheapest cell and the ladder's canary. Measured 2026-08-13 on this branch with the MINTED-HARP nonce: `just engine-matrix claude-code host none`, 1 scenario / 3 steps passed in 46s, no skip. Previously green on the j002300 per-engine live floor 2026-08-12 with the pre-harp hex nonce."
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
// So it was measured: claude-code host/none, on this branch, 2026-08-13. The
// cell ran (1 scenario, 3 steps, no skip), the harp reached the engine through
// composed context, and the engine echoed it back exactly. That closes the
// question for the delivery path every probe in the ladder shares.
//
// EVERY OTHER live-verified row above still records a PRE-HARP measurement and
// says so in its own reason. They are not invalidated — the swap is upstream of
// what they test — but they have not been re-observed. If one of them reds with
// a CONTEXT-DELIVERY failure while the engine is plainly healthy, the nonce is
// still the first thing to rule out, and matrixBundleYAML is where to look.

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
	return cells
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
