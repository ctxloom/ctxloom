package priority

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// fixedNow is the injected "now" every test in this file uses — proof that
// Compute never calls time.Now() itself; every age-dependent result here is
// reproducible byte for byte across runs.
var fixedNow = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

func mustSchema(t *testing.T, decls ...string) *tagschema.Schema {
	t.Helper()
	s, err := tagschema.Parse(decls)
	require.NoError(t, err)
	return s
}

// defaultTriageSchema mirrors internal/taskloom/config.DefaultTagSchema's
// priority_fn/decay_fn declarations verbatim (both on target
// "triage:severity"), so these tests exercise the actual shipped formulas,
// not a simplified stand-in.
func defaultTriageSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	return mustSchema(t,
		`tagma.decay_fn:"triage:severity"="{{triage:exposed}} > 0 ? 1 + {{age_days}}/({{age_days}}+90) : {{triage:blind-gate}} > 0 ? 1 + {{age_days}}/({{age_days}}+30) : {{triage:impact=capability}} > 0 ? 0.4 + 0.6 * 0.5 ** ({{age_days}}/120) : {{triage:impact=tooling}} > 0 ? 0.5 + 0.5 * 0.5 ** ({{age_days}}/180) : {{triage:impact=maintainability}} > 0 ? 0.6 + 0.4 * 0.5 ** ({{age_days}}/270) : {{triage:impact=performance}} > 0 ? 0.7 + 0.3 * 0.5 ** ({{age_days}}/365) : 1"`,
		`tagma.priority_fn:"triage:severity"="{{triage:severity}} * (1 + 0.25*{{triage:regression}} + 0.5*{{triage:data-loss}}) * {{age_factor}} / (1 + {{triage:effort}}/2) * (1 + {{triage:blocks-release}})"`,
	)
}

func TestCompute_SeverityOnly(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "low", Status: tasks.StatusToDo, Tags: []string{`triage:severity=2`}, CreatedAt: fixedNow},
		{HarpID: "high", Status: tasks.StatusToDo, Tags: []string{`triage:severity=8`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// age_days=0, no escalation tag present -> decay_fn's final "else"
	// branch, the literal 1; no effort/blocks-release -> raw == severity.
	assert.Equal(t, 2.0, results["low"].Raw)
	assert.Equal(t, 8.0, results["high"].Raw)
	assert.False(t, results["low"].Overridden)
	assert.False(t, results["high"].Overridden)

	// Rank-normalized over {2,8}: low is <= itself only (1/2 of the
	// population) -> 2.5; high is <= both (2/2) -> 5 (the population max
	// always reaches the ceiling).
	assert.Equal(t, 2.5, results["low"].Priority)
	assert.Equal(t, Max, results["high"].Priority)
}

func TestCompute_WithModifiersRaisesRawScore(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`}, CreatedAt: fixedNow},
		{HarpID: "regressed", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`, `triage:regression`}, CreatedAt: fixedNow},
		{HarpID: "both", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`, `triage:regression`, `triage:data-loss`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 4.0, results["plain"].Raw)
	assert.Equal(t, 5.0, results["regressed"].Raw, "4 * (1 + 0.25)")
	assert.Equal(t, 7.0, results["both"].Raw, "4 * (1 + 0.25 + 0.5)")

	// Monotonic: a strictly higher raw score must never rank below a lower one.
	assert.Greater(t, results["both"].Priority, results["regressed"].Priority)
	assert.Greater(t, results["regressed"].Priority, results["plain"].Priority)
}

// TestCompute_EffortAndBlocksReleaseAdjustRawScore pins the two priority_fn
// factors TestCompute_WithModifiersRaisesRawScore doesn't touch: effort
// divides the score down (a more expensive fix ranks lower, all else equal),
// blocks-release multiplies it up.
func TestCompute_EffortAndBlocksReleaseAdjustRawScore(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`}, CreatedAt: fixedNow},
		{HarpID: "costly", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`, `triage:effort=2`}, CreatedAt: fixedNow},
		{HarpID: "blocker", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`, `triage:blocks-release`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 4.0, results["plain"].Raw)
	assert.Equal(t, 2.0, results["costly"].Raw, "4 / (1 + 2/2) == 2")
	assert.Equal(t, 8.0, results["blocker"].Raw, "4 * (1 + 1) == 8")
}

func TestCompute_ExploitedInWildOverridesToMax(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "boring", Status: tasks.StatusToDo, Tags: []string{`triage:severity=1`}, CreatedAt: fixedNow},
		{HarpID: "exploited", Status: tasks.StatusToDo, Tags: []string{`triage:severity=1`, `triage:exploited-in-wild`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// Raw is still just the formula's output — the override only affects
	// display Priority, never Raw.
	assert.Equal(t, 1.0, results["exploited"].Raw)
	assert.Equal(t, Max, results["exploited"].Priority)
	assert.True(t, results["exploited"].Overridden)
	assert.False(t, results["boring"].Overridden)
}

// TestCompute_DeferredTaskSkipsDecay uses the "exposed" escalation branch
// (the only way the shipped default decay_fn moves with age at all — see
// TestCompute_SeverityAgeCrossover_MaxEscalationCapsAt2x) to prove a
// Deferred task's age never moves its score, while an otherwise-identical
// non-Deferred task's does.
func TestCompute_DeferredTaskSkipsDecay(t *testing.T) {
	schema := defaultTriageSchema(t)
	created := fixedNow.Add(-100 * 24 * time.Hour) // 100 days old
	all := []tasks.Task{
		{HarpID: "parked", Status: tasks.StatusDeferred, Trigger: "something", Tags: []string{`triage:severity=4`, "triage:exposed"}, CreatedAt: created},
		{HarpID: "active", Status: tasks.StatusToDo, Tags: []string{`triage:severity=4`, "triage:exposed"}, CreatedAt: created},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// active: age_factor = 1 + 100/(100+90); parked: age must not move a
	// Deferred task's score, so age_factor stays exactly 1 regardless of age.
	assert.Equal(t, 4.0, results["parked"].Raw, "Deferred: decay is skipped, age_factor pinned to 1")
	assert.InDelta(t, 4.0*(1+100.0/190.0), results["active"].Raw, 1e-9, "non-Deferred: decay raises the score with age (exposed branch)")
	assert.Greater(t, results["active"].Raw, results["parked"].Raw)
}

func TestCompute_DecayRaisesRawScoreWithAge(t *testing.T) {
	// Isolate decay's effect: priority_fn is just age_factor itself.
	schema := mustSchema(t,
		`tagma.priority_fn:"triage:impact"="{{age_factor}}"`,
		`tagma.decay_fn:"triage:impact"="1 + {{age_days}} / 365"`,
	)
	fresh := tasks.Task{HarpID: "fresh", Status: tasks.StatusToDo, CreatedAt: fixedNow}
	yearOld := tasks.Task{HarpID: "year-old", Status: tasks.StatusToDo, CreatedAt: fixedNow.Add(-365 * 24 * time.Hour)}

	results, _, err := Compute([]tasks.Task{fresh, yearOld}, schema, fixedNow)
	require.NoError(t, err)

	assert.InDelta(t, 1.0, results["fresh"].Raw, 1e-9)
	assert.InDelta(t, 2.0, results["year-old"].Raw, 1e-9, "a full year old should double the anti-rot factor")
	assert.Greater(t, results["year-old"].Raw, results["fresh"].Raw)
}

// TestCompute_SeverityAgeCrossover_MaxEscalationCapsAt2x locks the INVARIANT
// the shipped default decay_fn's exposed/blind-gate branches are designed
// around: 1 + age_days/(age_days+K) asymptotically approaches (but never
// reaches) 2 as age_days grows without bound, so escalation is capped at
// exactly 2x a task's bare severity, however old it gets. That cap produces
// an exact crossover point: an ancient severity-2 task (ceiling ~4.0) must
// NEVER outrank a merely fresh severity-5 task (raw exactly 5.0, no
// escalation tag), but an ancient severity-3 EXPOSED task (ceiling ~6.0)
// DOES exceed it — 2.5 is the exact severity threshold 2x escalation must
// clear to beat a fresh 5.
func TestCompute_SeverityAgeCrossover_MaxEscalationCapsAt2x(t *testing.T) {
	schema := defaultTriageSchema(t)
	// Asymptotically old (bounded by time.Duration's int64-nanosecond range,
	// ~292 years, so this stays well inside it): age_days/(age_days+90) is
	// within 0.001 of 1 at this magnitude, without needing a literal
	// infinite age_days.
	ancient := fixedNow.Add(-100_000 * 24 * time.Hour)

	all := []tasks.Task{
		{HarpID: "ancient-sev2-exposed", Status: tasks.StatusToDo, Tags: []string{"triage:severity=2", "triage:exposed"}, CreatedAt: ancient},
		{HarpID: "ancient-sev3-exposed", Status: tasks.StatusToDo, Tags: []string{"triage:severity=3", "triage:exposed"}, CreatedAt: ancient},
		{HarpID: "fresh-sev5", Status: tasks.StatusToDo, Tags: []string{"triage:severity=5"}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.InDelta(t, 4.0, results["ancient-sev2-exposed"].Raw, 0.01, "severity 2 at the ~2x escalation ceiling")
	assert.InDelta(t, 6.0, results["ancient-sev3-exposed"].Raw, 0.01, "severity 3 at the ~2x escalation ceiling")
	assert.Equal(t, 5.0, results["fresh-sev5"].Raw, "fresh, no escalation tag: decay_fn's final else branch is the literal 1")

	assert.Less(t, results["ancient-sev2-exposed"].Raw, results["fresh-sev5"].Raw,
		"an ancient LOW-severity task must never outrank a fresh HIGH-severity one once escalation is capped at 2x")
	assert.Greater(t, results["ancient-sev3-exposed"].Raw, results["fresh-sev5"].Raw,
		"but a high enough severity DOES cross a fresh higher-severity task once escalated")
}

// TestCompute_TerminalTasksExcludedFromNormalizationPopulation proves a
// Done/Archived task's outsized raw score can't distort a live task's
// percentile — it never enters the population rankNormalize builds from —
// and that the terminal task itself is left at Priority 0 (never ranked).
func TestCompute_TerminalTasksExcludedFromNormalizationPopulation(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "finished", Status: tasks.StatusDone, Tags: []string{`triage:severity=5`}, CreatedAt: fixedNow},
		{HarpID: "only-live", Status: tasks.StatusToDo, Tags: []string{`triage:severity=1`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Zero(t, results["finished"].Priority, "a terminal task is never assigned a display priority")
	assert.Equal(t, Max, results["only-live"].Priority, "the sole non-terminal task is 100th percentile of its own population")
}

func TestCompute_NilSchemaIsInertNotAnError(t *testing.T) {
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	results, diag, err := Compute(all, nil, fixedNow)
	require.NoError(t, err)
	assert.Zero(t, results["a"].Raw)
	assert.True(t, diag.NoPriorityFn, "a nil schema declares no priority_fn")
}

// TestCompileAll_FormulaOnAnyTargetIsSyntaxChecked proves the fix this
// package's history calls out: BEFORE this change, a formula declared on
// any target other than the one hardcoded target Compute happened to
// evaluate was never even syntax-checked — a garbage formula there compiled
// "successfully" by never being looked at. Now every declared priority_fn is
// compiled, regardless of which target it's declared against, so a bad one
// is a loud error naming the offending target.
func TestCompileAll_FormulaOnAnyTargetIsSyntaxChecked(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:foo"="{{{{ this is not a valid expr (("`,
	)
	_, _, err := Compute([]tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}, schema, fixedNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "widget:foo")
}

// TestCompileAll_MoreThanOnePriorityFnIsAnAmbiguityError proves declaring
// priority_fn on two different targets is a hard, loud error naming both
// conflicting targets — never a silent pick of whichever target happened to
// sort first.
func TestCompileAll_MoreThanOnePriorityFnIsAnAmbiguityError(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"triage:severity"="{{triage:severity}}"`,
		`tagma.priority_fn:"widget:foo"="{{widget:foo}}"`,
	)
	_, _, err := Compute([]tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}, schema, fixedNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "priority_fn")
	assert.Contains(t, err.Error(), "triage:severity")
	assert.Contains(t, err.Error(), "widget:foo")
}

// TestCompileAll_MoreThanOneDecayFnIsAnAmbiguityError mirrors the priority_fn
// case for decay_fn.
func TestCompileAll_MoreThanOneDecayFnIsAnAmbiguityError(t *testing.T) {
	schema := mustSchema(t,
		`tagma.decay_fn:"triage:severity"="1"`,
		`tagma.decay_fn:"widget:foo"="1"`,
	)
	_, _, err := Compute([]tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}, schema, fixedNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decay_fn")
}

// TestCompute_Diagnostics_NoPriorityFn proves NoPriorityFn is set precisely
// when the schema declares no priority_fn at all (an arity-only schema, in
// this case) — the classic "every Raw is 0" silent-no-op condition.
func TestCompute_Diagnostics_NoPriorityFn(t *testing.T) {
	schema := mustSchema(t, `tagma.arity:"triage:severity"=scalar`)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:severity=1"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"triage:severity=5"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.True(t, diag.NoPriorityFn)
}

// TestCompute_Diagnostics_NoPriorityFn_FalseWhenDeclared is the negative
// case: a declared (and evaluated) priority_fn clears NoPriorityFn.
func TestCompute_Diagnostics_NoPriorityFn_FalseWhenDeclared(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:severity=1"}, CreatedAt: fixedNow}}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.False(t, diag.NoPriorityFn)
}

// TestCompute_Diagnostics_AllTied proves AllTied fires when more than one
// non-terminal task shares the exact same Raw score — here because neither
// carries a tag priority_fn reads, so both land on the formula's own zero
// value regardless of a declared priority_fn.
func TestCompute_Diagnostics_AllTied(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.True(t, diag.AllTied)
}

// TestCompute_Diagnostics_AllTied_FalseWhenScoresDiffer is the negative
// case: distinguishing tag data breaks the tie.
func TestCompute_Diagnostics_AllTied_FalseWhenScoresDiffer(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:severity=1"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"triage:severity=5"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.False(t, diag.AllTied)
}

// TestCompute_Diagnostics_AllTied_FalseWithSingleNonTerminalTask proves the
// ">1 non-terminal task" precondition: a lone active task trivially "ties"
// with nothing, and that must not read as a meaningless ranking.
func TestCompute_Diagnostics_AllTied_FalseWithSingleNonTerminalTask(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.False(t, diag.AllTied)
}

// TestCompute_Diagnostics_ScoredTasks counts only the non-terminal tasks
// carrying a tag priority_fn/decay_fn's own formula text actually
// references — here "triage:severity" (read by priority_fn) — so a task
// carrying only unrelated tags doesn't count as "scored".
func TestCompute_Diagnostics_ScoredTasks(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "scored", Status: tasks.StatusToDo, Tags: []string{"triage:severity=3"}, CreatedAt: fixedNow},
		{HarpID: "unrelated-tag", Status: tasks.StatusToDo, Tags: []string{"urgent"}, CreatedAt: fixedNow},
		{HarpID: "no-tags", Status: tasks.StatusToDo, CreatedAt: fixedNow},
		{HarpID: "terminal-scored", Status: tasks.StatusDone, Tags: []string{"triage:severity=3"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.Equal(t, 1, diag.ScoredTasks, "only the non-terminal task carrying triage:severity counts")
}

// TestResolveTagValues_AbsentTagIsZero / ValuelessModifierIsOne /
// MalformedValueIsInert exercise resolveTagValues directly (white-box —
// this test file is in package priority).
func TestResolveTagValues_AbsentTagIsZero(t *testing.T) {
	values := resolveTagValues(nil)
	assert.Equal(t, 0.0, values["triage:impact"])
}

func TestResolveTagValues_ValuelessModifierTagIsOne(t *testing.T) {
	values := resolveTagValues([]string{"triage:regression"})
	assert.Equal(t, 1.0, values["triage:regression"])
}

// TestResolveTagValues_ArithmeticLookingValueCannotAlterTheFormula is the
// end-to-end proof (at the level the coordinator's correction asked for)
// that a tag VALUE containing arithmetic-looking text never gets evaluated
// as an expression: a quoted tag value can carry arbitrary literal content
// (tagma's quoting extension), including something that LOOKS like it could
// blow up an unguarded string-substitution bridge. Because
// tagschema.CompileFormula never substitutes tag values into expression
// text — only Eval's Tag closure resolves them, as data — an unparseable
// value simply falls back to 0, exactly like any other malformed number.
func TestResolveTagValues_ArithmeticLookingValueCannotAlterTheFormula(t *testing.T) {
	values := resolveTagValues([]string{`triage:impact="9999*9999"`})
	assert.Equal(t, 0.0, values["triage:impact"], "an unparseable (however arithmetic-looking) value must be inert, never evaluated")
}

func TestResolveTagValues_NumericValueParses(t *testing.T) {
	values := resolveTagValues([]string{"triage:impact=3.5"})
	assert.Equal(t, 3.5, values["triage:impact"])
}

// TestResolveTagValues_CompositeKey_NumericValue proves a value-carrying tag
// registers BOTH the bare numeric entry AND a composite "target=value"
// presence key, for a value that also happens to parse as a number.
func TestResolveTagValues_CompositeKey_NumericValue(t *testing.T) {
	values := resolveTagValues([]string{"triage:severity=3"})
	assert.Equal(t, 3.0, values["triage:severity"])
	assert.Equal(t, 1.0, values["triage:severity=3"])
}

// TestResolveTagValues_CompositeKey_NonNumericValue proves the composite key
// is registered even when the value does NOT parse as a number — this is
// precisely the case a "{{triage:impact=capability}}" formula placeholder
// depends on: an enum-valued tag has no numeric magnitude, but its presence
// (and WHICH value) must still be visible to a formula.
func TestResolveTagValues_CompositeKey_NonNumericValue(t *testing.T) {
	values := resolveTagValues([]string{"triage:impact=capability"})
	assert.Equal(t, 0.0, values["triage:impact"], "non-numeric: the bare numeric entry falls back to 0")
	assert.Equal(t, 1.0, values["triage:impact=capability"], "but the composite presence key is still set")
	_, absent := values["triage:impact=tooling"]
	assert.False(t, absent, "a DIFFERENT enum value's composite key must not be set")
}

// TestCompute_CompositeKeyDrivesDecayEnumBranch is the end-to-end proof that
// {{triage:impact=capability}} — inert before this feature, since the
// composite key didn't exist — now actually selects decay_fn's capability
// branch.
func TestCompute_CompositeKeyDrivesDecayEnumBranch(t *testing.T) {
	schema := defaultTriageSchema(t)
	old := fixedNow.Add(-120 * 24 * time.Hour) // exactly one capability half-life
	all := []tasks.Task{
		{HarpID: "capability", Status: tasks.StatusToDo, Tags: []string{"triage:severity=4", "triage:impact=capability"}, CreatedAt: old},
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{"triage:severity=4"}, CreatedAt: old},
	}
	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// capability branch at one half-life: 0.4 + 0.6*0.5 == 0.7 -> raw = 2.8.
	assert.InDelta(t, 2.8, results["capability"].Raw, 1e-9)
	// plain: no escalation/decay tag present -> decay_fn's final else (1).
	assert.Equal(t, 4.0, results["plain"].Raw)
}

// TestRankNormalize_HigherRawGetsHigherOrEqualPriority is a small property
// check: for any set of raw scores, sorting by raw ascending must yield a
// non-decreasing sequence of priorities, and members tied at the same raw
// value must land on the exact same priority.
func TestRankNormalize_HigherRawGetsHigherOrEqualPriority(t *testing.T) {
	raw := map[string]float64{
		"a": 1, "b": 1, "c": 3, "d": 2, "e": 5,
	}
	population := []string{"a", "b", "c", "d", "e"}
	got := rankNormalize(raw, population)

	assert.Equal(t, got["a"], got["b"], "tied raw values share a percentile")
	assert.Less(t, got["a"], got["d"])
	assert.Less(t, got["d"], got["c"])
	assert.Less(t, got["c"], got["e"])
	assert.Equal(t, Max, got["e"], "the population maximum always reaches the ceiling")
}

func TestRankNormalize_EmptyPopulationYieldsEmptyMap(t *testing.T) {
	got := rankNormalize(map[string]float64{}, nil)
	assert.Empty(t, got)
}
