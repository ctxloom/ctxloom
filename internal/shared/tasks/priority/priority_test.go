package priority

import (
	"math"
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
// priority_fn/decay_fn declarations verbatim (both on target "triage:kind"),
// so these tests exercise the actual shipped formulas, not a simplified
// stand-in.
func defaultTriageSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	return mustSchema(t,
		`tagma.decay_fn:"triage:kind"="{{triage:exposed=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+90) : {{triage:blind-gate=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+30) : {{triage:kind=capability}} > 0 ? 0.4 + 0.6 * 0.5 ** ({{age_days}}/120) : {{triage:kind=chore}} > 0 ? 0.5 + 0.5 * 0.5 ** ({{age_days}}/180) : 1"`,
		`tagma.priority_fn:"triage:kind"="({{triage:kind=defect}} > 0 ? 3 : {{triage:kind=capability}} > 0 ? 2 : 1) * (1 + {{triage:data-loss}} + {{triage:security=*}} + 0.75*{{triage:no-workaround}} + 0.5*{{triage:crashes}} + 0.5*{{triage:regression=*}}) * {{age_factor}} * (1 + {{triage:blocks-release=*}}) / (1 + {{triage:effort}}/2)"`,
	)
}

// TestCompute_KindOnly proves the base weight priority_fn assigns per
// triage:kind value (defect=3, capability=2, chore=1, see the ternary
// chain's own doc) with no other modifiers present — at age_days=0 every
// decay_fn branch's 0.5**(age_days/K) term is 1, so age_factor is 1
// regardless of which branch triage:kind selects, isolating the weight
// itself.
func TestCompute_KindOnly(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "low", Status: tasks.StatusToDo, Tags: []string{`triage:kind=chore`}, CreatedAt: fixedNow},
		{HarpID: "high", Status: tasks.StatusToDo, Tags: []string{`triage:kind=defect`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 1.0, results["low"].Raw, "chore weight, no modifiers")
	assert.Equal(t, 3.0, results["high"].Raw, "defect weight, no modifiers")
	assert.False(t, results["low"].Overridden)
	assert.False(t, results["high"].Overridden)

	// Rank-normalized over {1,3}: low is <= itself only (1/2 of the
	// population) -> 2.5; high is <= both (2/2) -> 5 (the population max
	// always reaches the ceiling).
	assert.Equal(t, 2.5, results["low"].Priority)
	assert.Equal(t, Max, results["high"].Priority)
}

// TestCompute_WithModifiersRaisesRawScore proves defect+data-loss > defect >
// capability > chore at equal effort — the ranking invariant the schema
// revision is built around — by layering the additive risk-modifier factors
// onto a fixed triage:kind weight.
func TestCompute_WithModifiersRaisesRawScore(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "chore", Status: tasks.StatusToDo, Tags: []string{`triage:kind=chore`}, CreatedAt: fixedNow},
		{HarpID: "capability", Status: tasks.StatusToDo, Tags: []string{`triage:kind=capability`}, CreatedAt: fixedNow},
		{HarpID: "defect", Status: tasks.StatusToDo, Tags: []string{`triage:kind=defect`}, CreatedAt: fixedNow},
		{HarpID: "defect-regressed", Status: tasks.StatusToDo, Tags: []string{`triage:kind=defect`, `triage:regression=abc123`}, CreatedAt: fixedNow},
		{HarpID: "defect-data-loss", Status: tasks.StatusToDo, Tags: []string{`triage:kind=defect`, `triage:regression=abc123`, `triage:data-loss`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 1.0, results["chore"].Raw)
	assert.Equal(t, 2.0, results["capability"].Raw)
	assert.Equal(t, 3.0, results["defect"].Raw)
	assert.Equal(t, 4.5, results["defect-regressed"].Raw, "3 * (1 + 0.5)")
	assert.Equal(t, 7.5, results["defect-data-loss"].Raw, "3 * (1 + 1 + 0.5)")

	// The invariant: defect+data-loss > defect > capability > chore, at
	// equal effort (here, effort unset on every task).
	assert.Greater(t, results["defect-data-loss"].Priority, results["defect"].Priority)
	assert.Greater(t, results["defect"].Priority, results["capability"].Priority)
	assert.Greater(t, results["capability"].Priority, results["chore"].Priority)
}

// TestCompute_EffortAndBlocksReleaseAdjustRawScore pins the two priority_fn
// factors TestCompute_WithModifiersRaisesRawScore doesn't touch: effort
// divides the score down (a more expensive fix ranks lower, all else equal),
// blocks-release multiplies it up — and specifically with a NON-NUMERIC
// value ("0.7.0"), the exact case Part 1's presence-testing fix exists for:
// before that fix, "{{triage:blocks-release=*}}" could never see a
// non-numeric-valued tag at all, so the release gate silently never doubled
// anything.
func TestCompute_EffortAndBlocksReleaseAdjustRawScore(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{`triage:kind=capability`}, CreatedAt: fixedNow},
		{HarpID: "costly", Status: tasks.StatusToDo, Tags: []string{`triage:kind=capability`, `triage:effort=2`}, CreatedAt: fixedNow},
		{HarpID: "blocker", Status: tasks.StatusToDo, Tags: []string{`triage:kind=capability`, `triage:blocks-release=0.7.0`}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, 2.0, results["plain"].Raw)
	assert.Equal(t, 1.0, results["costly"].Raw, "2 / (1 + 2/2) == 1")
	assert.Equal(t, 4.0, results["blocker"].Raw, "2 * (1 + 1) == 4 -- the release gate doubles the score even though 0.7.0 never parses as a number")
}

func TestCompute_ExploitedInWildOverridesToMax(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "boring", Status: tasks.StatusToDo, Tags: []string{`triage:kind=chore`}, CreatedAt: fixedNow},
		{HarpID: "exploited", Status: tasks.StatusToDo, Tags: []string{`triage:kind=chore`, `triage:exploited-in-wild`}, CreatedAt: fixedNow},
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
// TestCompute_KindAgeCrossover_MaxEscalationCapsAt2x) to prove a Deferred
// task's age never moves its score, while an otherwise-identical
// non-Deferred task's does. triage:exposed is applied BARE (no surface
// named), proving the "=*" universal presence key (Part 1's fix) is what
// drives the escalation branch, not a specific enum value.
func TestCompute_DeferredTaskSkipsDecay(t *testing.T) {
	schema := defaultTriageSchema(t)
	created := fixedNow.Add(-100 * 24 * time.Hour) // 100 days old
	all := []tasks.Task{
		{HarpID: "parked", Status: tasks.StatusDeferred, Trigger: "something", Tags: []string{`triage:kind=defect`, "triage:exposed"}, CreatedAt: created},
		{HarpID: "active", Status: tasks.StatusToDo, Tags: []string{`triage:kind=defect`, "triage:exposed"}, CreatedAt: created},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// active: age_factor = 1 + 100/(100+90); parked: age must not move a
	// Deferred task's score, so age_factor stays exactly 1 regardless of age.
	assert.Equal(t, 3.0, results["parked"].Raw, "Deferred: decay is skipped, age_factor pinned to 1")
	assert.InDelta(t, 3.0*(1+100.0/190.0), results["active"].Raw, 1e-9, "non-Deferred: decay raises the score with age (bare-exposed branch)")
	assert.Greater(t, results["active"].Raw, results["parked"].Raw)
}

func TestCompute_DecayRaisesRawScoreWithAge(t *testing.T) {
	// Isolate decay's effect: priority_fn is just age_factor itself, on a
	// scratch target unrelated to the shipped default vocabulary.
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:score"="{{age_factor}}"`,
		`tagma.decay_fn:"widget:score"="1 + {{age_days}} / 365"`,
	)
	fresh := tasks.Task{HarpID: "fresh", Status: tasks.StatusToDo, CreatedAt: fixedNow}
	yearOld := tasks.Task{HarpID: "year-old", Status: tasks.StatusToDo, CreatedAt: fixedNow.Add(-365 * 24 * time.Hour)}

	results, _, err := Compute([]tasks.Task{fresh, yearOld}, schema, fixedNow)
	require.NoError(t, err)

	assert.InDelta(t, 1.0, results["fresh"].Raw, 1e-9)
	assert.InDelta(t, 2.0, results["year-old"].Raw, 1e-9, "a full year old should double the anti-rot factor")
	assert.Greater(t, results["year-old"].Raw, results["fresh"].Raw)
}

// TestCompute_KindAgeCrossover_MaxEscalationCapsAt2x locks the INVARIANT the
// shipped default decay_fn's exposed/blind-gate branches are designed
// around: 1 + age_days/(age_days+K) asymptotically approaches (but never
// reaches) 2 as age_days grows without bound, so escalation is capped at
// exactly 2x a task's bare triage:kind weight, however old it gets. That cap
// produces an exact crossover point: an ancient LOW-weight (chore=1) exposed
// task (ceiling ~2.0) must NEVER outrank a merely fresh HIGH-weight
// (defect=3) task (raw exactly 3.0, no escalation tag), but an ancient
// MID-weight (capability=2) EXPOSED task (ceiling ~4.0) DOES exceed it.
func TestCompute_KindAgeCrossover_MaxEscalationCapsAt2x(t *testing.T) {
	schema := defaultTriageSchema(t)
	// Asymptotically old (bounded by time.Duration's int64-nanosecond range,
	// ~292 years, so this stays well inside it): age_days/(age_days+90) is
	// within 0.001 of 1 at this magnitude, without needing a literal
	// infinite age_days.
	ancient := fixedNow.Add(-100_000 * 24 * time.Hour)

	all := []tasks.Task{
		{HarpID: "ancient-chore-exposed", Status: tasks.StatusToDo, Tags: []string{"triage:kind=chore", "triage:exposed"}, CreatedAt: ancient},
		{HarpID: "ancient-capability-exposed", Status: tasks.StatusToDo, Tags: []string{"triage:kind=capability", "triage:exposed"}, CreatedAt: ancient},
		{HarpID: "fresh-defect", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
	}

	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	assert.InDelta(t, 2.0, results["ancient-chore-exposed"].Raw, 0.01, "chore weight 1 at the ~2x escalation ceiling")
	assert.InDelta(t, 4.0, results["ancient-capability-exposed"].Raw, 0.01, "capability weight 2 at the ~2x escalation ceiling")
	assert.Equal(t, 3.0, results["fresh-defect"].Raw, "fresh, no escalation tag: decay_fn's final else branch is the literal 1")

	assert.Less(t, results["ancient-chore-exposed"].Raw, results["fresh-defect"].Raw,
		"an ancient LOW-weight task must never outrank a fresh HIGH-weight one once escalation is capped at 2x")
	assert.Greater(t, results["ancient-capability-exposed"].Raw, results["fresh-defect"].Raw,
		"but a high enough weight DOES cross a fresh higher-weight task once escalated")
}

// TestCompute_TerminalTasksExcludedFromNormalizationPopulation proves a
// Done/Archived task's outsized raw score can't distort a live task's
// percentile — it never enters the population rankNormalize builds from —
// and that the terminal task itself is left at Priority 0 (never ranked).
func TestCompute_TerminalTasksExcludedFromNormalizationPopulation(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "finished", Status: tasks.StatusDone, Tags: []string{`triage:kind=defect`}, CreatedAt: fixedNow},
		{HarpID: "only-live", Status: tasks.StatusToDo, Tags: []string{`triage:kind=chore`}, CreatedAt: fixedNow},
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

// TestCompileAll_UnknownBuiltinIsAnError (inane-scuba) proves a typo'd
// builtin placeholder — one with no ":" (so it compiles to a Builtin(...)
// call, not a Tag(...) one) that names something OTHER than the fixed known
// set (age_days, age_factor) — fails loud at compile time, exactly like a
// syntax error does (TestCompileAll_FormulaOnAnyTargetIsSyntaxChecked),
// instead of silently resolving to 0 via lookup's bare map index and quietly
// zeroing the whole formula term it appears in.
func TestCompileAll_UnknownBuiltinIsAnError(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"widget:foo"="{{widget:foo}} * {{typo}}"`,
	)
	_, _, err := Compute([]tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}, schema, fixedNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "typo")
	assert.Contains(t, err.Error(), "widget:foo")
}

// TestCompileAll_UnknownBuiltin_DecayFnRejectsAgeFactor proves the known-set
// check is FACET-SPECIFIC: age_factor is decay_fn's own OUTPUT, never an
// input available to it (see Compute's decayFn.Eval call, which supplies
// only age_days) — a decay_fn formula referencing {{age_factor}} is the same
// class of error as a genuine typo, not a legitimate builtin this facet
// merely doesn't happen to use.
func TestCompileAll_UnknownBuiltin_DecayFnRejectsAgeFactor(t *testing.T) {
	schema := mustSchema(t,
		`tagma.decay_fn:"widget:foo"="{{age_factor}}"`,
	)
	_, _, err := Compute([]tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}, schema, fixedNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "age_factor")
}

// TestCompileAll_KnownBuiltinsStillCompile is the negative control: the
// real, documented builtins (age_days for both facets, age_factor for
// priority_fn) must NOT be rejected by the new check.
func TestCompileAll_KnownBuiltinsStillCompile(t *testing.T) {
	schema := mustSchema(t,
		`tagma.decay_fn:"widget:foo"="1 + {{age_days}}/100"`,
		`tagma.priority_fn:"widget:foo"="{{widget:foo}} * {{age_factor}} + {{age_days}}"`,
	)
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	_, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
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
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:kind=chore"}, CreatedAt: fixedNow}}
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
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:kind=chore"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
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
// references — here "triage:kind" (read by priority_fn) — so a task
// carrying only unrelated tags doesn't count as "scored".
func TestCompute_Diagnostics_ScoredTasks(t *testing.T) {
	schema := defaultTriageSchema(t)
	all := []tasks.Task{
		{HarpID: "scored", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
		{HarpID: "unrelated-tag", Status: tasks.StatusToDo, Tags: []string{"urgent"}, CreatedAt: fixedNow},
		{HarpID: "no-tags", Status: tasks.StatusToDo, CreatedAt: fixedNow},
		{HarpID: "terminal-scored", Status: tasks.StatusDone, Tags: []string{"triage:kind=defect"}, CreatedAt: fixedNow},
	}
	_, diag, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.Equal(t, 1, diag.ScoredTasks, "only the non-terminal task carrying triage:kind counts")
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

// TestResolveTagValues_UniversalPresenceKey_BareTag proves the load-bearing
// "or valueless" half of the "target=*" composite key: a BARE modifier tag
// (no value at all, e.g. "triage:exposed" with no surface named) still sets
// "target=*" -> 1.0. Without this, "{{triage:exposed=*}}" -- a universal
// presence test meant to catch a bare tag too -- would silently see nothing
// and never escalate, exactly the silent-no-op class this key exists to
// close.
func TestResolveTagValues_UniversalPresenceKey_BareTag(t *testing.T) {
	values := resolveTagValues([]string{"triage:exposed"})
	assert.Equal(t, 1.0, values["triage:exposed=*"], "a bare (valueless) tag must still set the universal presence key")
	assert.Equal(t, 1.0, values["triage:exposed"], "and the ordinary bare-target modifier key")
}

// TestResolveTagValues_UniversalPresenceKey_ValuedTag proves the "target=*"
// key is also set for a value-carrying tag, independent of whether that
// value parses as a number -- e.g. "triage:blocks-release=0.7.0" (a
// non-numeric, version-shaped value) must still make
// "{{triage:blocks-release=*}}" true.
func TestResolveTagValues_UniversalPresenceKey_ValuedTag(t *testing.T) {
	values := resolveTagValues([]string{"triage:blocks-release=0.7.0"})
	assert.Equal(t, 1.0, values["triage:blocks-release=*"], "a valued (even non-numeric-valued) tag must set the universal presence key")
	assert.Equal(t, 1.0, values["triage:blocks-release=0.7.0"], "and its own specific composite value key")
	assert.Equal(t, 0.0, values["triage:blocks-release"], "the bare numeric entry still falls back to 0 for a non-numeric value")
}

// TestResolveTagValues_UniversalPresenceKey_AbsentTag proves the negative:
// a target that was never applied at all sets no "target=*" entry (lookup's
// zero-value fallback already makes an absent key read as 0 for any
// Tag(...) call).
func TestResolveTagValues_UniversalPresenceKey_AbsentTag(t *testing.T) {
	values := resolveTagValues([]string{"triage:kind=defect"})
	_, present := values["triage:exposed=*"]
	assert.False(t, present, "a target never applied to the task must not set a universal presence key")
}

// TestCompute_CompositeKeyDrivesDecayEnumBranch is the end-to-end proof that
// {{triage:kind=capability}} — inert before the composite-key feature landed
// — actually selects decay_fn's capability branch.
func TestCompute_CompositeKeyDrivesDecayEnumBranch(t *testing.T) {
	schema := defaultTriageSchema(t)
	old := fixedNow.Add(-120 * 24 * time.Hour) // exactly one capability half-life
	all := []tasks.Task{
		{HarpID: "capability", Status: tasks.StatusToDo, Tags: []string{"triage:kind=capability"}, CreatedAt: old},
		{HarpID: "plain", Status: tasks.StatusToDo, Tags: []string{"triage:kind=defect"}, CreatedAt: old},
	}
	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	// capability branch at one half-life: weight 2 * (0.4 + 0.6*0.5) == 1.4.
	assert.InDelta(t, 1.4, results["capability"].Raw, 1e-9)
	// plain (defect): no escalation/decay tag present -> decay_fn's final
	// else (1) -> raw == defect's bare weight, 3.
	assert.Equal(t, 3.0, results["plain"].Raw)
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

// TestRankNormalize_NaNRawDoesNotHang pins the fix: a
// stored tag value of the literal string "NaN" survives both the write-time
// validator and the lint sweep (both use ParseFloat then a `<`/`>` bounds
// test, and every comparison against NaN is false) and reaches priority as a
// real math.NaN() raw score. rankNormalize's tie-grouping loop advances by
// finding the run of members equal to raw[sorted[i]] — but NaN != NaN, so a
// NaN member is never equal to itself and `j` never moves past `i`,
// spinning forever. Run it on a goroutine with a hard deadline: the
// characteristic failure mode here is not a wrong answer, it is that
// rankNormalize NEVER RETURNS.
func TestRankNormalize_NaNRawDoesNotHang(t *testing.T) {
	raw := map[string]float64{
		"a": 1, "b": math.NaN(), "c": 3,
	}
	population := []string{"a", "b", "c"}

	done := make(chan map[string]float64, 1)
	go func() {
		done <- rankNormalize(raw, population)
	}()

	select {
	case got := <-done:
		assert.Len(t, got, 3, "every population member must still receive a priority")
		for _, id := range population {
			assert.False(t, math.IsNaN(got[id]), "a NaN raw score must not propagate into the displayed priority for %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rankNormalize did not return within 2s — NaN raw score hung the tie-grouping loop")
	}
}

// TestCompute_NaNTagValueDoesNotHang is the flow-level twin of the test
// above: a task carrying a tag whose value literally parses as NaN (a legal
// tagma bare token) must not hang `taskloom list --sort priority` end to
// end via Compute.
func TestCompute_NaNTagValueDoesNotHang(t *testing.T) {
	schema := mustSchema(t, `tagma.priority_fn:"triage:impact"="{{triage:impact}}"`)
	all := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{"triage:impact=NaN"}, CreatedAt: fixedNow},
		{HarpID: "b", Status: tasks.StatusToDo, Tags: []string{"triage:impact=2"}, CreatedAt: fixedNow},
	}

	type result struct {
		results map[string]Result
		err     error
	}
	done := make(chan result, 1)
	go func() {
		results, _, err := Compute(all, schema, fixedNow)
		done <- result{results, err}
	}()

	select {
	case r := <-done:
		// Either outcome is acceptable as long as Compute returns: a named
		// error is the fix this row recommends, but the hang is the defect
		// under test.
		if r.err == nil {
			for id, res := range r.results {
				assert.False(t, math.IsNaN(res.Priority), "task %s must not carry a NaN priority", id)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Compute did not return within 2s given a NaN-valued tag")
	}
}

// TestCompute_ExploitedInWildOverride_NotAppliedToTerminalTask pins the fix:
// Result.Priority's own doc says a terminal (Done/Archived) task
// is left at 0 because it never enters the normalization population — but
// the exploited-in-wild override was applied unconditionally, before the
// terminal check, forcing a closed task's Priority to Max.
func TestCompute_ExploitedInWildOverride_NotAppliedToTerminalTask(t *testing.T) {
	schema := mustSchema(t, `tagma.priority_fn:"triage:kind"="1"`)
	all := []tasks.Task{
		{HarpID: "closed", Status: tasks.StatusDone, Tags: []string{"triage:exploited-in-wild"}, CreatedAt: fixedNow},
		{HarpID: "open", Status: tasks.StatusToDo, CreatedAt: fixedNow},
	}
	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)

	closed := results["closed"]
	assert.Equal(t, 0.0, closed.Priority, "a terminal task's priority must stay 0 per Result.Priority's documented contract")
	assert.False(t, closed.Overridden, "the exploited-in-wild override must not fire for a terminal task")
}

// TestCompute_ZeroCreatedAtIsAnError pins the first half of the fix: a
// task whose CreatedAt folded to the zero time.Time (e.g. log.go's repair()
// re-add, which drops Ts) must not silently pin to the
// decay curve's extreme; Compute has the only vantage point that can
// notice the corrupt input, and must say so.
func TestCompute_ZeroCreatedAtIsAnError(t *testing.T) {
	schema := mustSchema(t, `tagma.decay_fn:"triage:kind"="{{age_days}}"`)
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: time.Time{}}}
	_, _, err := Compute(all, schema, fixedNow)
	require.Error(t, err, "a zero CreatedAt is corrupt data and Compute must reject it rather than silently pinning age to ~739000 days")
}

// TestCompute_FutureCreatedAtClampsAgeToZero pins the second half of
// the fix: clock skew or a hand-edited log can produce a CreatedAt in the
// future. The shipped default decay_fn evaluates 1 + age/(age+90), which is
// -Inf at age == -90 exactly — age must clamp at 0 rather than go negative.
func TestCompute_FutureCreatedAtClampsAgeToZero(t *testing.T) {
	schema := mustSchema(t, `tagma.decay_fn:"triage:kind"="{{age_days}}"`, `tagma.priority_fn:"triage:kind"="{{age_factor}}"`)
	future := fixedNow.Add(90 * 24 * time.Hour)
	all := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: future}}
	results, _, err := Compute(all, schema, fixedNow)
	require.NoError(t, err)
	assert.Equal(t, 0.0, results["a"].Raw, "a future CreatedAt must clamp age_days at 0, not go negative")
}
