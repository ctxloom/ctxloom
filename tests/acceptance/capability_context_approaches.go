// Package acceptance: P1, the CONTEXT-APPROACH SWEEP — the ladder's proof that
// ctxloom can deliver an agent's context by each MECHANISM the engine declares,
// not merely by whichever one the engine happens to default to.
//
// UNTAGGED, deliberately. The config renderer and the verdict below are P1's
// trust anchor, and a trust anchor that only the paid live lane can execute is a
// trust anchor nobody checks. Everything here runs under plain `just test` /
// `just test-pkg ./tests/acceptance/`; only the godog wiring
// (steps_capability_context_approaches.go) is behind the `acceptance` tag.
//
// WHAT P1 ADDS TO P0, AND WHY IT IS NOT THE SAME CELL TWICE. P0 proves that a
// nonce planted in an agent's composed context comes back out of a real engine.
// It cannot say WHICH delivery mechanism carried it: `run` takes the engine's
// own default, so P0's claude cells measure the system-prompt scratch file and
// its codex cells measure the SessionStart hook, and nothing in the suite ever
// exercised the other arms of either engine's ApproachTable. Rows 4 and 5 of the
// capability inventory (agent.ApproachSystemPrompt, agent.ApproachHook) were
// claimed-but-unproven for exactly that reason. P1 holds the task, the prompt,
// the bundle and the nonce channel constant and varies ONE line of config — the
// agent binding's `surfaces: {context: <approach>}` preference — so a red is
// attributable to the mechanism and to nothing else.
//
// THE FALSE GREEN THIS FILE EXISTS TO REFUSE. The pin is a PREFERENCE, and
// production treats a preference it cannot honour as a warning, not a failure:
// operations.ResolveAgent warns "using <engine>'s default delivery" and launches
// anyway (deliberately — the binding was already validated at write time, so
// reaching that arm means a hand-edited config, and a session is worth more than
// a purist refusal). backends.AssembleManagedConfig degrades the same way when
// config.Load fails, and a nil ManagedConfig drops the pin entirely on the way
// to the wire.
//
// Either degrade produces a cell that DELIVERS THE NONCE PERFECTLY, exits 0, and
// proves nothing whatsoever about the approach it claims to have tested — the
// well-formed report of nothing, in the one shape this probe is most exposed to.
// So the verdict checks the degrade markers BEFORE it looks at the JSON, and a
// degraded run is reported as a CONTEXT-DELIVERY failure naming the pin, because
// that is what it is: the context arrived through a channel this cell did not
// select.
//
// WHAT THE VERDICT STILL CANNOT SEE, STATED PLAINLY. Absence of the degrade
// warning proves the pin reached the backend's surface selection; it is not a
// POSITIVE observation of which writer ran. No ctxloom surface reports the
// resolved per-surface approach today — `run --dry-run` renders the assembled
// context, the profiles and the target file, and says nothing about the delivery
// mechanism — and the one artifact that would have discriminated the hook route
// (the .ctxloom/cache/context/<hash>.md file the SessionStart hook reads) is
// removed at teardown by BaseContextProvider.Clear, so it is gone before an
// assertion could read it. That gap is recorded as this slice's deferred work
// rather than papered over with a weaker check.
//
// It is worth being precise about how much that costs, because it is less than
// it sounds: for codex the mechanisms are genuinely separable by OUTCOME. Its
// hook route is the one the 2026-07-14 finding indicts (profile fragments
// dropped from the cache file the hook actually reads, recorded on
// liveAgents["codex"]), and its AGENTS.md route is not. If that defect is still
// live, the hook cell reds with a CONTEXT-DELIVERY failure while the unsafe-file
// cell goes green — and that differential IS the measurement the registry row
// for this cell asks for.
package acceptance

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// approachFamily is P1's name in a skip line, a failure message and the evidence
// sidecar. One constant so the three cannot disagree.
const approachFamily = "approach-sweep"

// approachSurfaceKind is the surface P1 pins. Context, and only context: the
// other surface kinds have exactly one approach on every registered backend
// (see each engine's ApproachTable), so there is nothing to sweep there and a
// cell claiming to have swept one would be claiming a choice that does not
// exist.
const approachSurfaceKind = "context"

// approachState is one P1 cell's fixture and its captured result — P0's
// matrixState plus the two fields that make this probe a different experiment:
// the VARIANT (the cell's name in the registry, which is what distinguishes two
// cells sharing an engine and both axes) and the pinned APPROACH.
//
// stdout and stderr are kept SEPARATE, as everywhere in this ladder: ctxloom's
// human-readable diagnostics go to stderr by contract, and here that separation
// is load-bearing twice over — the degrade warning this probe hunts for is a
// stderr line, and reading it out of a combined capture would mean the engine
// could put the same words on stdout and red the cell.
type approachState struct {
	engine    string // backend type, as written in the Examples table
	runtime   string // host | container
	workspace string // none | worktree
	variant   string // the registry cell's variant ("system-prompt", "unsafe-file-shared")
	approach  string // the pinned agent.Approach label ("system-prompt", "hook", "unsafe-file")
	nonce     string
	stdout    string
	stderr    string
	exitCode  int
	runErr    error
}

// cell is this cell's identity in the ladder's vocabulary: the ledger key its
// nonce was minted under and the stamp every failure carries. The VARIANT is
// what makes it distinct — claude-code/host/none appears twice in P1, once for
// system-prompt and once for hook, and a ledger that keyed them the same way
// would hand the second cell the first cell's nonce and call it a leak.
func (s *approachState) cell() probeCellID {
	return probeCellID{
		Probe:     probeP1,
		Engine:    s.engine,
		Runtime:   s.runtime,
		Workspace: s.workspace,
		Variant:   s.variant,
	}
}

// approachSurfacesBlock renders the ONE line of config that makes a P1 cell
// different from the P0 cell beside it: the agent binding's delivery preference.
//
// It is appended to matrixConfigYAML's output rather than woven into it, at the
// same four-space indent as the binding's other keys, so the two probes' configs
// differ by exactly this block and a diff of them is readable.
func approachSurfacesBlock(approach string) string {
	return fmt.Sprintf("    surfaces:\n      %s: %s\n", approachSurfaceKind, approach)
}

// approachConfigYAML is a P1 cell's whole config.yaml: P0's, plus the pin.
func approachConfigYAML(a liveAgent, llmKey, runtime, approach string) string {
	return matrixConfigYAML(a, llmKey, runtime) + approachSurfacesBlock(approach)
}

// approachPinAcceptedByEngine asks PRODUCTION'S OWN VALIDATOR whether this
// engine declares this approach, by calling the very function the launch path
// calls (operations.ResolveAgentSurfaces, which resolves against
// backends.SurfacesFor — the engine's ApproachTable and nothing else).
//
// Two jobs, and the second is the reason it is a hard error rather than a skip:
//
//   - it stops a cell from spending a paid turn to discover that the pin will be
//     warned away and the run will silently take the default;
//   - it makes the registry's GATED-OUT rows checkable against the engine
//     itself. The design's rule is that a probe for a capability an engine
//     DECLARES absent has no Examples row at all, and "declares absent" has to
//     mean the ApproachTable, not somebody's memory of it. A gated-out row that
//     the engine in fact supports, or a wired row it does not, is a registry
//     defect this function turns into a red — hermetically, in
//     capability_context_approaches_test.go, with no engine installed.
func approachPinAcceptedByEngine(engine, approach string) error {
	if _, err := operations.ResolveAgentSurfaces(engine, map[string]string{approachSurfaceKind: approach}); err != nil {
		return fmt.Errorf("%s: %s does not declare the %s approach for its %s surface, so this cell can never pin it: %w — a cell for an approach the engine's ApproachTable does not carry is gated-out BY ABSENCE (no Examples row), never a red",
			approachFamily, engine, approach, approachSurfaceKind, err)
	}
	return nil
}

// approachVariantNamesItsApproach keeps the two Examples columns honest about
// each other. The variant is the registry's name for the cell and the approach
// is what gets written into config; they are separate columns because a cell may
// qualify its variant ("unsafe-file-shared" is the unsafe-file approach observed
// on the worktree axis), and separate columns are two places to make a typo.
//
// The rule is a prefix rule, which is exactly as strong as it needs to be: a
// variant must BEGIN with the approach it pins, so "hook"/"system-prompt" pairs
// cannot be crossed and a qualifier can still be added.
func approachVariantNamesItsApproach(variant, approach string) error {
	if !strings.HasPrefix(variant, approach) {
		return fmt.Errorf("%s: cell variant %q does not name the approach %q it pins — the variant is how the registry, the ledger and the @var- tag address this cell, so a variant that disagrees with the config it writes makes every one of them point somewhere else",
			approachFamily, variant, approach)
	}
	return nil
}

// approachDegradeMarker is one way production can silently fall back to the
// engine's default delivery, with the stderr text it prints when it does and the
// reason a human needs when a cell reds on it.
type approachDegradeMarker struct {
	// Marker is the substring of production's own warning. Matched as a
	// SUBSTRING of a real warning rather than as a whole line, because the line
	// carries the agent name and the underlying error and those vary per cell.
	Marker string
	// Why explains what the degrade means for this cell's claim.
	Why string
}

// approachDegradeMarkers are every path by which a launch can honour something
// other than the pin. Both are WARNINGS in production, on purpose, and that
// design choice is precisely what makes them invisible to a probe that only
// reads stdout.
//
// Kept as data rather than as two inline strings so a third path added to
// production has one obvious place to be declared, and so the unit tests can
// walk them.
var approachDegradeMarkers = []approachDegradeMarker{
	{
		Marker: "default delivery",
		Why:    "operations.ResolveAgent could not validate the binding's surfaces preference against the engine and fell back to the engine's own default delivery. The nonce may well have arrived — through the mechanism this cell did NOT select.",
	},
	{
		Marker: "launching without managed hooks/commands",
		Why:    "backends.AssembleManagedConfig returned nil (config.Load failed), and internal/cli/run.go only attaches the binding's Surfaces preference to a NON-nil managed payload — so the pin was dropped on the way to the wire and the engine's default delivery ran instead.",
	},
}

// approachPinHonoured is the check that separates this probe from P0: it refuses
// a run in which production announced it was not using the pinned approach.
//
// A cell that trips this is NOT reported as some new kind of failure. It is a
// CONTEXT-DELIVERY failure — the channel's own shape, from probeChannel — because
// that is the honest description: the context reached the model through a channel
// this cell did not select, so whatever the model then said about the nonce says
// nothing about the approach under test.
func approachPinHonoured(v probeVerdict, approach, stderr string) error {
	for _, m := range approachDegradeMarkers {
		if !strings.Contains(stderr, m.Marker) {
			continue
		}
		return v.fail(v.Channel.Shape,
			fmt.Sprintf("%s — the run DEGRADED off the pinned %q approach: production printed %q on stderr. %s A cell whose pin was ignored cannot prove the pinned approach delivers, however good its answer looks.",
				v.Channel.Shape, approach, m.Marker, m.Why),
			fmt.Sprintf("\nstderr:\n%s", stderr))
	}
	return nil
}

// approachAssert is P1's whole verdict, and it COMPOSES the ladder's shared
// vocabulary (probe_assert.go) rather than restating it — the same order P0
// keeps, with the pin check spliced in at the one place it belongs.
//
// The ORDER is the diagnostic, and each position is argued:
//
//	ran          — did the run complete, and did it say anything at all. First,
//	               because a silent no-op or a crashed engine makes every later
//	               question meaningless.
//	pinHonoured  — was the mechanism under test actually the mechanism that ran.
//	               BEFORE the output checks, because a degraded run that answers
//	               perfectly is this probe's characteristic false green, and a
//	               green reported here would be a lie about the capability rather
//	               than a mistake about the output.
//	jsonObject   — is stdout the requested form (fences and preamble are RED, by
//	               design; see the feature header).
//	carriesNonce — did the pinned channel deliver at all.
//	exactObject  — is it exactly {"hello": "<harp>"}.
func approachAssert(s *approachState) error {
	v := probeVerdict{Family: approachFamily, Cell: s.cell(), Channel: channelComposedContext}
	trimmed, err := v.ran(probeRun{Stdout: s.stdout, Stderr: s.stderr, ExitCode: s.exitCode, Err: s.runErr})
	if err != nil {
		return err
	}
	if err := approachPinHonoured(v, s.approach, s.stderr); err != nil {
		return err
	}
	got, err := v.jsonObject(trimmed)
	if err != nil {
		return err
	}
	if err := v.carriesNonce(trimmed, s.nonce); err != nil {
		return err
	}
	return v.exactObject(got, trimmed, matrixExpectedKey, s.nonce)
}
