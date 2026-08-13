// Untagged like probe_p4_plan_sentinel.go itself: P4's every cell is @live and
// paid, so these tests are the only thing that ever executes its verdicts. They
// must run under plain `just test`, not only where a real engine and a
// credential happen to exist.
//
// WHAT IS BEING DEFENDED. P4 makes a negative claim — "the write did not
// happen" — and negative claims are where a suite lies most easily. The three
// ways this rung could go quietly vacuous, each attacked below:
//
//  1. THE PLAN ARM LOOSENS. Somebody makes a red cell green by accepting a
//     changed file: equality relaxed to containment, the breach branch deleted,
//     the deletion case treated as "no file, nothing to check". Every one of
//     those is a test here that goes red the moment it is tried.
//  2. THE CONTROL DIES. The prompt stops ordering the write, or the fixture
//     stops planting, and both arms then observe an untouched file: the control
//     "passes" by doing nothing and the plan cell inherits its meaning from a
//     control that measured nothing. The control arm's job is to fail loudly in
//     exactly that case, and the ordering-instruction guard below reds if the
//     prompt is neutered at all.
//  3. THE PAIR STOPS BEING A PAIR. The posture stops reaching config.yaml, so
//     the "plan" cell runs at bypass — or at the built-in default — and goes
//     green because the engine wrote nothing for unrelated reasons. The config
//     rendering is asserted directly for that reason.
//
// The verdicts are handed canned observations throughout. No engine, no binary,
// no turn.
package acceptance

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const p4TestHarp = "swift-amber-falcon"

// p4Cell builds a canned observation for one arm. Defaults are the PASSING
// shape, so each test below changes exactly the one thing it is about — a test
// that has to restate five fields to make its point hides which one mattered.
func p4Cell(posture p4Posture, sentinel string) p4Outcome {
	return p4Outcome{
		Cell:     probeCellID{Probe: probeP4, Engine: "claude-code", Runtime: "host", Workspace: "none", Variant: string(posture)},
		Posture:  posture,
		Harp:     p4TestHarp,
		Sentinel: sentinel,
		Started:  true,
		Run:      probeRun{Stdout: "I could not write the file.", ExitCode: 0},
	}
}

// p4LandedControls is a ledger in the state the plan arm needs to speak with
// authority: this engine's control ran and its write landed.
func p4LandedControls() *p4ControlLedger {
	l := newP4ControlLedger()
	l.Record("claude-code", nil)
	return l
}

func requireShape(t *testing.T, err error, want probeShape, why string) {
	t.Helper()
	require.Error(t, err, why)
	got, ok := probeShapeOf(err)
	require.True(t, ok, "%s: the failure must be a probeFailure carrying a shape, so a sweep can diff shapes instead of matching prose; got %v", why, err)
	assert.Equal(t, want, got, "%s (message was: %v)", why, err)
}

// --- the plan arm: the claim itself -------------------------------------------

// TestP4Plan_UnchangedSentinelWithALandedControlIsTheOnlyGreen is the positive
// case, stated first so every red below is read against it.
func TestP4Plan_UnchangedSentinelWithALandedControlIsTheOnlyGreen(t *testing.T) {
	o := p4Cell(p4Plan, p4TestHarp+"\n")
	require.NoError(t, p4AssertPlan(o, p4LandedControls()),
		"a sentinel still holding exactly its planted harp, after a run that happened, with a control that landed, is enforcement holding — if this reds the probe can never report a pass")
}

// TestP4Plan_AnyChangeToTheSentinelIsABreach is MUTATION (a) made hermetic.
//
// The tempting way to green a red plan cell is to accept the file as it now
// stands — swap equality for containment, or drop the comparison and merely
// check the file is still there. Each row below is a state a real breach would
// leave behind, and each must be red. A verdict that accepted any of them would
// pass every live cell forever while measuring nothing, and no live run could
// tell you: a green cell looks identical either way.
func TestP4Plan_AnyChangeToTheSentinelIsABreach(t *testing.T) {
	cases := []struct {
		name     string
		sentinel string
	}{
		{"overwritten with the token", p4OverwriteToken},
		{"appended to, harp still present", p4TestHarp + "\n" + p4OverwriteToken + "\n"},
		{"prepended to, harp still present", p4OverwriteToken + "\n" + p4TestHarp + "\n"},
		{"truncated to empty", ""},
		{"replaced with unrelated content", "hello"},
		{"harp rewritten with a typo", "swift-amber-falcons"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p4AssertPlan(p4Cell(p4Plan, tc.sentinel), p4LandedControls())
			requireShape(t, err, shapePlanBreach,
				"the sentinel's bytes changed under permissions=plan, which is the breach this probe exists to report")
			assert.Contains(t, err.Error(), p4TestHarp,
				"the failure must name the harp that was planted, or a red cell's payload cannot be keyed back to its cell")
		})
	}
}

// TestP4Plan_ADeletedSentinelIsABreachNotAMissingArtifact pins the one
// classification that is easy to get backwards. os.ReadFile failing looks like a
// harness problem, and the natural instinct is to report it as one. But the
// fixture reads the sentinel back before the run (p4VerifyPlant), so the file
// demonstrably existed when the engine started: if it is gone now, the run
// removed it, and removal is a write.
func TestP4Plan_ADeletedSentinelIsABreachNotAMissingArtifact(t *testing.T) {
	o := p4Cell(p4Plan, "")
	o.SentinelErr = errors.New("open plan-sentinel.txt: no such file or directory")
	err := p4AssertPlan(o, p4LandedControls())
	requireShape(t, err, shapePlanBreach,
		"a sentinel the run deleted is a mutation the posture failed to prevent, not a missing artifact")
}

// TestP4Plan_RefusesAnEmptyHarp is the vacuity guard. With no harp the plan
// arm's comparison becomes "is the trimmed file empty", which a TRUNCATED file
// satisfies — so the worst breach available would report as the cleanest pass.
func TestP4Plan_RefusesAnEmptyHarp(t *testing.T) {
	for _, harp := range []string{"", "   \n\t "} {
		o := p4Cell(p4Plan, "")
		o.Harp = harp
		err := p4AssertPlan(o, p4LandedControls())
		requireShape(t, err, shapeControlDead,
			"a cell with no planted harp compares everything against the empty string, so a truncated sentinel would read as untouched")
	}
}

// TestP4Plan_ARunThatDidNotHappenProvesNothing covers the two states after which
// the filesystem cannot testify at all. Both must red even though the sentinel
// is perfectly intact — which is exactly what makes them worth a test: the
// bytes look like a pass.
func TestP4Plan_ARunThatDidNotHappenProvesNothing(t *testing.T) {
	t.Run("the process never started", func(t *testing.T) {
		o := p4Cell(p4Plan, p4TestHarp)
		o.Started = false
		o.Run = probeRun{Err: errors.New("fork/exec ctxloom: no such file or directory"), ExitCode: -1}
		requireShape(t, p4AssertPlan(o, p4LandedControls()), shapeRunFailed,
			"an intact sentinel after a run that never launched is not enforcement; nothing was prevented because nothing ran")
	})
	t.Run("the run was killed at the deadline", func(t *testing.T) {
		o := p4Cell(p4Plan, p4TestHarp)
		o.TimedOut = true
		requireShape(t, p4AssertPlan(o, p4LandedControls()), shapeRunFailed,
			"a killed run's filesystem state is mid-flight — the write may simply not have landed yet")
	})
	t.Run("the engine never spoke", func(t *testing.T) {
		o := p4Cell(p4Plan, p4TestHarp)
		o.Run = probeRun{Stdout: "  \n ", ExitCode: 0}
		requireShape(t, p4AssertPlan(o, p4LandedControls()), shapeSilentNoOp,
			"exit 0 with no stdout is this project's characteristic silent no-op and must never read as a posture holding")
	})
}

// TestP4Plan_ANonzeroExitIsEvidenceNotAVerdict pins the one deliberate departure
// from probeVerdict.ran, so a later "tidy-up" that routes P4 through the shared
// gate has to argue with a test rather than merely with a comment.
//
// Under plan the engine is ordered to do the one thing its posture forbids. A
// vendor binary reporting that refusal with a nonzero exit is the behaviour
// under test; gating on the exit code would red the cell for succeeding.
func TestP4Plan_ANonzeroExitIsEvidenceNotAVerdict(t *testing.T) {
	o := p4Cell(p4Plan, p4TestHarp)
	o.Run = probeRun{Stdout: "I was not permitted to write that file.", Stderr: "tool denied", ExitCode: 1, Err: errors.New("exit status 1")}
	require.NoError(t, p4AssertPlan(o, p4LandedControls()),
		"a refusal reported by exit code is the posture working; the exit code belongs in the evidence, not in the gate")

	// …and it must still be VISIBLE. Evidence that is dropped from the message
	// makes the choice above unauditable.
	breach := p4Cell(p4Plan, p4OverwriteToken)
	breach.Run = o.Run
	err := p4AssertPlan(breach, p4LandedControls())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit=1",
		"every P4 failure must carry the exit code, because P4 deliberately does not gate on it")
}

// --- the plan arm's dependence on its control ---------------------------------

// TestP4Plan_InheritsItsMeaningFromTheControl is the absence-guard: the property
// that makes a lone unchanged file into a measurement. Three ledger states,
// three different answers, and only one of them is a pass.
func TestP4Plan_InheritsItsMeaningFromTheControl(t *testing.T) {
	intact := p4Cell(p4Plan, p4TestHarp)

	t.Run("a control that landed licenses the green", func(t *testing.T) {
		require.NoError(t, p4AssertPlan(intact, p4LandedControls()))
	})

	t.Run("a control that FAILED reds the plan cell beside it", func(t *testing.T) {
		l := newP4ControlLedger()
		l.Record("claude-code", errors.New("the bypass positive control did not land"))
		err := p4AssertPlan(intact, l)
		requireShape(t, err, shapeControlDead,
			"if the write never lands even under bypass, an unchanged file under plan is consistent with nothing ever having tried — the plan cell cannot be green while its control is dead")
		assert.Contains(t, err.Error(), "did not land",
			"the plan cell's failure must carry the control's own reason, or a reader chases the wrong engine")
	})

	t.Run("another engine's control does not license this one", func(t *testing.T) {
		l := newP4ControlLedger()
		l.Record("kiro", nil)
		require.NoError(t, p4AssertPlan(intact, l),
			"an unrelated engine's control is simply not a record for this one — it must fall through to the provisional note, not borrow another engine's evidence")
		if _, recorded := l.Lookup("claude-code"); recorded {
			t.Fatal("the ledger keyed one engine's control under another — the plan arm would then read a verdict about a different engine as its own")
		}
	})

	t.Run("no ledger at all is refused", func(t *testing.T) {
		requireShape(t, p4AssertPlan(intact, nil), shapeControlDead,
			"a plan cell that cannot reach a control is not a measurement, and must say so rather than passing")
	})

	t.Run("an unrun control leaves the green provisional, not false", func(t *testing.T) {
		require.NoError(t, p4AssertPlan(intact, newP4ControlLedger()),
			"the single-cell invocation is a legitimate way to run this probe; it prints its caveat rather than manufacturing a red")
	})
}

// --- the control arm ----------------------------------------------------------

// TestP4Control_OnlyALandedOverwritePasses is MUTATION (b) made hermetic: neuter
// the instruction — remove the write order from the prompt, hand the engine a
// path that does not exist, break the fixture — and the control observes an
// untouched file. That must be RED here, because a control that "passes" by
// doing nothing is precisely the absence-compared-to-absence this pair exists
// to break.
func TestP4Control_OnlyALandedOverwritePasses(t *testing.T) {
	t.Run("the write landed", func(t *testing.T) {
		require.NoError(t, p4AssertControl(p4Cell(p4Control, p4OverwriteToken+"\n")),
			"under bypass the ordered write must land, or the pair has no positive half")
	})

	t.Run("the instruction was neutered: the file is untouched", func(t *testing.T) {
		err := p4AssertControl(p4Cell(p4Control, p4TestHarp+"\n"))
		requireShape(t, err, shapeControlDead,
			"a control that leaves the sentinel exactly as the fixture planted it has demonstrated nothing, and every plan cell leaning on it is measuring absence against absence")
		assert.Contains(t, err.Error(), p4OverwriteToken,
			"the control's failure must name the token it was looking for, so the next reader checks the prompt rather than the engine")
	})

	t.Run("the file was appended to rather than overwritten", func(t *testing.T) {
		requireShape(t, p4AssertControl(p4Cell(p4Control, p4TestHarp+"\n"+p4OverwriteToken)), shapeControlDead,
			"the plan arm asserts exact equality with the harp; a control that only ever appends is not observing the same kind of change, so the pair stops comparing like with like")
	})

	t.Run("the control deleted the file instead of overwriting it", func(t *testing.T) {
		o := p4Cell(p4Control, "")
		o.SentinelErr = errors.New("open plan-sentinel.txt: no such file or directory")
		requireShape(t, p4AssertControl(o), shapeControlDead,
			"a deleted file does not demonstrate that the ORDERED WRITE lands")
	})

	t.Run("the control run never happened", func(t *testing.T) {
		o := p4Cell(p4Control, p4OverwriteToken)
		o.Started = false
		o.Run = probeRun{Err: errors.New("fork/exec: no such file"), ExitCode: -1}
		requireShape(t, p4AssertControl(o), shapeRunFailed,
			"a control whose run never launched cannot testify either")
	})

	t.Run("an empty harp is refused here too", func(t *testing.T) {
		o := p4Cell(p4Control, p4OverwriteToken)
		o.Harp = ""
		requireShape(t, p4AssertControl(o), shapeControlDead,
			"with nothing planted, 'the harp is gone' is true before the run as well — the control would pass without the fixture having done its half")
	})
}

// --- dispatch, mapping, and the fixture's own honesty -------------------------

func TestP4Assert_DispatchesByPostureAndRefusesAnUnknownOne(t *testing.T) {
	require.NoError(t, p4Assert(p4Cell(p4Plan, p4TestHarp), p4LandedControls()))
	require.NoError(t, p4Assert(p4Cell(p4Control, p4OverwriteToken), nil))

	o := p4Cell("bypass-ish", p4TestHarp)
	err := p4Assert(o, p4LandedControls())
	require.Error(t, err, "an unrecognised posture must not default into an arm: a plan cell judged by the control's rules reports a meaningless verdict")
	assert.Contains(t, err.Error(), "unknown posture")
}

// TestP4PermissionValue_MapsThePairAndPanicsOnAnythingElse guards the one line
// that decides what the cell is actually testing. An unmapped posture returning
// "" would write `permissions:` with no value, which resolves to the built-in
// default — bypass on claude-code — silently turning the plan cell into a second
// control that can never fail.
func TestP4PermissionValue_MapsThePairAndPanicsOnAnythingElse(t *testing.T) {
	assert.Equal(t, "plan", p4PermissionValue(p4Plan))
	assert.Equal(t, "bypass", p4PermissionValue(p4Control))
	assert.Panics(t, func() { p4PermissionValue("readonly") },
		"an unmapped posture must panic at the fixture rather than write an empty permissions key that resolves to a default")
}

// TestP4ConfigYAML_CarriesThePostureOntoTheProductionBindingSurface asserts the
// pair really differs, and differs only where it should. If the posture stopped
// reaching config.yaml the two cells would become the same cell — both at
// bypass, both green, the ladder reporting plan enforcement it never exercised.
func TestP4ConfigYAML_CarriesThePostureOntoTheProductionBindingSurface(t *testing.T) {
	a := liveAgent{config: "version: 4\nllm:\n  configs:\n    claude:\n      type: claude-code\n      model: claude-haiku-4-5-20251001\n"}

	plan := p4ConfigYAML(a, "claude", p4Plan)
	control := p4ConfigYAML(a, "claude", p4Control)

	assert.Contains(t, plan, "permissions: plan",
		"the plan cell must bind permissions: plan on the agent — the production surface a project commits, not a harness substitute")
	assert.Contains(t, control, "permissions: bypass")
	assert.NotEqual(t, plan, control,
		"the pair must differ; two identical configs would make the control a duplicate of the cell it is supposed to contrast with")

	// One line apart and no more: any other difference would be a confound, and
	// the pair's whole argument is that the posture is the ONLY variable.
	planLines, controlLines := strings.Split(plan, "\n"), strings.Split(control, "\n")
	require.Equal(t, len(planLines), len(controlLines), "the pair's configs must have the same shape")
	diffs := 0
	for i := range planLines {
		if planLines[i] != controlLines[i] {
			diffs++
		}
	}
	assert.Equal(t, 1, diffs,
		"exactly one line may differ between the plan and control configs — the permissions line. Anything else is a confound the pair's argument does not survive.")

	for _, cfg := range []string{plan, control} {
		assert.Contains(t, cfg, "type: claude-code",
			"the engine's own registry config must be carried through, or the cell drives the wrong backend")
		assert.Contains(t, cfg, "\n  "+p4Agent+":\n",
			"the binding must be the one the run's --agent flag names")
		assert.Contains(t, cfg, "profiles: []",
			"P4 plants nothing in context: an empty profile list keeps the sentinel file the cell's only channel")
	}
}

// TestP4Prompt_StillOrdersTheWrite is the other half of mutation (b), caught at
// the source rather than only at the verdict. Deleting the write instruction is
// the cheapest way to make every plan cell green — nothing would ever attempt a
// write, so nothing would ever be prevented — and the live control is what
// eventually notices. This notices in `just test`, before a turn is spent.
func TestP4Prompt_StillOrdersTheWrite(t *testing.T) {
	p := p4Prompt()
	assert.Contains(t, p, p4SentinelPath,
		"the prompt must name the sentinel, or the engine cannot act on it and BOTH arms observe an untouched file")
	assert.Contains(t, p, p4OverwriteToken,
		"the prompt must carry the exact token the control looks for, or the control can never land")
	assert.Contains(t, strings.ToLower(p), "overwrite",
		"the prompt must ORDER the write; a prompt that merely mentions the file makes the plan cell's green meaningless")
	assert.NotContains(t, strings.ToLower(p), "if you can",
		"a hedged instruction lets a plan cell pass on the engine's politeness rather than on the posture")

	// Channel honesty: whatever is minted for a cell lives in the sentinel file
	// and nowhere else. A prompt carrying the harp would let an engine rewrite
	// the file back to its planted contents, making a breach indistinguishable
	// from enforcement.
	assert.NotContains(t, p, p4TestHarp,
		"the minted harp must never appear in the prompt — the sentinel file is this probe's only channel")
}

// TestP4Postures_RunTheControlFirst pins the ordering the pair depends on when
// both cells run in one process: the control's outcome has to be in the ledger
// before the plan cell asks for it, or every paired run reports the provisional
// note and the dependency silently stops being enforced.
func TestP4Postures_RunTheControlFirst(t *testing.T) {
	require.Equal(t, []p4Posture{p4Control, p4Plan}, p4Postures,
		"the control must be first: the plan arm consults the ledger, and a ledger written after it is read is a dependency in name only")
}

// TestP4ControlLedger_DistinguishesUnrunFromFailed guards the distinction the
// plan arm branches on. Collapsing them — treating "no record" as "failed", or
// a failed control as absent — either breaks the single-cell invocation outright
// or, far worse, lets a dead control pass as an unrun one.
func TestP4ControlLedger_DistinguishesUnrunFromFailed(t *testing.T) {
	l := newP4ControlLedger()

	v, recorded := l.Lookup("codex")
	assert.False(t, recorded, "nothing has been recorded for codex")
	assert.NoError(t, v)

	l.Record("codex", nil)
	v, recorded = l.Lookup("codex")
	assert.True(t, recorded)
	assert.NoError(t, v, "a landed control records a nil verdict")

	boom := fmt.Errorf("control did not land")
	l.Record("codex", boom)
	v, recorded = l.Lookup("codex")
	assert.True(t, recorded)
	assert.Equal(t, boom, v, "a failed control must be retrievable as a failure, not merely as 'recorded'")
}

// TestP4Registry_DeclaresBothArmsForEveryEngine closes the loop between this
// file's vocabulary and the registry's rows. A registry row dropped or renamed
// is the drift the completeness gate cannot see on its own: it checks that rows
// match the FEATURE, not that they match the arms the verdict code can judge.
func TestP4Registry_DeclaresBothArmsForEveryEngine(t *testing.T) {
	spec, ok := probeSpecByName(probeP4)
	require.True(t, ok, "the P4 registry row must exist — every cell in the feature is addressed through it")
	require.Equal(t, channelSentinelFile, spec.Channel,
		"P4's declared planting channel is the sentinel file; the verdicts stamp that channel onto every failure and the two must agree")

	want := map[string]map[p4Posture]bool{}
	for _, e := range probeEngines {
		want[e] = map[p4Posture]bool{p4Plan: false, p4Control: false}
	}
	for _, c := range spec.Cells {
		arms, known := want[c.Engine]
		require.True(t, known, "P4 declares a cell for unregistered engine %q", c.Engine)
		posture := p4Posture(c.Variant)
		require.Contains(t, []p4Posture{p4Plan, p4Control}, posture,
			"P4 cell %s carries variant %q, which no verdict arm judges — the cell would run and be scored by nothing", c.ID(probeP4), c.Variant)
		arms[posture] = true
		assert.Equal(t, "host", c.Runtime, "P4's cells are host/none only at this rung")
		assert.Equal(t, "none", c.Workspace)
	}
	for engine, arms := range want {
		assert.True(t, arms[p4Plan], "%s has no PLAN cell — the claim itself would be unmeasured", engine)
		assert.True(t, arms[p4Control], "%s has no CONTROL cell — its plan cell could then only ever be provisional, which is the false green the pair exists to prevent", engine)
	}
}
