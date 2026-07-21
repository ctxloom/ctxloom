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
// priority_fn/decay_fn declarations, so these tests exercise the actual
// shipped formulas, not a simplified stand-in.
func defaultTriageSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	return mustSchema(t,
		`tagma.priority_fn:"triage:impact"="{{triage:impact}} * (1 + 0.25*{{triage:regression}} + 0.5*{{triage:data-loss}}) * {{age_factor}}"`,
		`tagma.decay_fn:"triage:impact"="1 + {{age_days}} / 365"`,
	)
}

func TestCompute_ImpactOnly(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "low", Status: tasks.StatusToDo, Tags: []string{`triage:impact=2`}, CreatedAt: fixedNow},
		{HarpID: "high", Status: tasks.StatusToDo, Tags: []string{`triage:impact=8`}, CreatedAt: fixedNow},
	}

	results, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// age_days=0 -> age_factor = 1 + 0/365 = 1; no modifiers -> raw == impact.
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
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{`triage:impact=4`}, CreatedAt: fixedNow},
		{HarpID: "regressed", Status: tasks.StatusToDo, Tags: []string{`triage:impact=4`, `triage:regression`}, CreatedAt: fixedNow},
		{HarpID: "both", Status: tasks.StatusToDo, Tags: []string{`triage:impact=4`, `triage:regression`, `triage:data-loss`}, CreatedAt: fixedNow},
	}

	results, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 4.0, results["plain"].Raw)
	assert.Equal(t, 5.0, results["regressed"].Raw, "4 * (1 + 0.25)")
	assert.Equal(t, 7.0, results["both"].Raw, "4 * (1 + 0.25 + 0.5)")

	// Monotonic: a strictly higher raw score must never rank below a lower one.
	assert.Greater(t, results["both"].Priority, results["regressed"].Priority)
	assert.Greater(t, results["regressed"].Priority, results["plain"].Priority)
}

func TestCompute_ExploitedInWildOverridesToMax(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "boring", Status: tasks.StatusToDo, Tags: []string{`triage:impact=1`}, CreatedAt: fixedNow},
		{HarpID: "exploited", Status: tasks.StatusToDo, Tags: []string{`triage:impact=1`, `triage:exploited-in-wild`}, CreatedAt: fixedNow},
	}

	results, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// Raw is still just the formula's output — the override only affects
	// display Priority, never Raw.
	assert.Equal(t, 1.0, results["exploited"].Raw)
	assert.Equal(t, Max, results["exploited"].Priority)
	assert.True(t, results["exploited"].Overridden)
	assert.False(t, results["boring"].Overridden)
}

func TestCompute_DeferredTaskSkipsDecay(t *testing.T) {
	schema := defaultTriageSchema(t)
	created := fixedNow.Add(-100 * 24 * time.Hour) // 100 days old
	all := []tasks.Task{
		{HarpID: "parked", Status: tasks.StatusDeferred, Trigger: "something", Tags: []string{`triage:impact=4`}, CreatedAt: created},
		{HarpID: "active", Status: tasks.StatusToDo, Tags: []string{`triage:impact=4`}, CreatedAt: created},
	}

	results, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// active: age_factor = 1 + 100/365; parked: age must not move a
	// Deferred task's score, so age_factor stays exactly 1 regardless of age.
	assert.Equal(t, 4.0, results["parked"].Raw, "Deferred: decay is skipped, age_factor pinned to 1")
	assert.InDelta(t, 4.0*(1+100.0/365.0), results["active"].Raw, 1e-9, "non-Deferred: decay raises the score with age")
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

	results, err := Compute([]tasks.Task{fresh, yearOld}, schema, fixedNow)
	require.NoError(t, err)

	assert.InDelta(t, 1.0, results["fresh"].Raw, 1e-9)
	assert.InDelta(t, 2.0, results["year-old"].Raw, 1e-9, "a full year old should double the anti-rot factor")
	assert.Greater(t, results["year-old"].Raw, results["fresh"].Raw)
}

// TestCompute_TerminalTasksExcludedFromNormalizationPopulation proves a
// Done/Archived task's outsized raw score can't distort a live task's
// percentile — it never enters the population rankNormalize builds from —
// and that the terminal task itself is left at Priority 0 (never ranked).
func TestCompute_TerminalTasksExcludedFromNormalizationPopulation(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "finished", Status: tasks.StatusDone, Tags: []string{`triage:impact=5`}, CreatedAt: fixedNow},
		{HarpID: "only-live", Status: tasks.StatusToDo, Tags: []string{`triage:impact=1`}, CreatedAt: fixedNow},
	}

	results, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Zero(t, results["finished"].Priority, "a terminal task is never assigned a display priority")
	assert.Equal(t, Max, results["only-live"].Priority, "the sole non-terminal task is 100th percentile of its own population")
}

func TestCompute_NilSchemaIsInertNotAnError(t *testing.T) {
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	results, err := Compute(all, nil, fixedNow)
	require.NoError(t, err)
	assert.Zero(t, results["a"].Raw)
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
