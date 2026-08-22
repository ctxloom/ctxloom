// Package priority derives a task's display priority at READ time — it is
// never stored (Task.DerivedPriority is populated on a copy handed back to a
// caller that asked for it; the append-only log never carries a priority
// event). Three ingredients, evaluated per task and then rank-normalized
// across the project:
//
//  1. priority_fn and decay_fn (declared per tag_schema.Schema, mustache
//     form, compiled/evaluated via tagschema.CompileFormula) produce a raw,
//     project-specific score from the task's own tag values plus a handful
//     of taskloom-provided BUILT-INS (age_days, age_factor — see the
//     Builtin* constants; the set is designed to be extended by adding a
//     name to builtinsByFacet (and supplying it from Compute's Eval calls),
//     not by changing the formula bridge). A formula referencing any OTHER
//     name is a compile-time error (checkKnownBuiltins), not a silent 0.
//  2. The `triage:exploited-in-wild` modifier tag, when present, overrides
//     the result to Max regardless of what the formula computed — an
//     actively-exploited issue is never left ranked below the ceiling by a
//     formula quirk.
//  3. Rank-normalization maps every non-terminal task's raw score onto
//     [0,Max] by percentile rank within the project (see rankNormalize),
//     so the display scale means the same thing regardless of what
//     absolute numbers priority_fn happens to produce.
package priority

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// ExploitedInWildTarget is the modifier tag whose presence forces a task's
// display Priority to Max, regardless of what priority_fn computed.
const ExploitedInWildTarget = "triage:exploited-in-wild"

// Max is the ceiling of the derived display priority scale, and the value
// the exploited-in-wild override forces.
const Max = 5.0

// Built-in datum names taskloom fills into every formula evaluation's
// Builtin(...) lookup (see tagschema.CompileFormula's bridge). This is
// deliberately a small, named, EXTENSIBLE set: adding a new one is a new
// constant here, a facet entry in builtinsByFacet, and a value supplied from
// Compute's own decayFn.Eval / priorityFn.Eval calls — no change to the
// formula bridge itself, since Builtin(name) already resolves any name from
// a plain map. checkKnownBuiltins is what keeps that plain map from silently
// swallowing anything NOT added this way.
const (
	// BuiltinAgeDays is a task's age in days: (now - CreatedAt) / 24h.
	// Available to both decay_fn and priority_fn.
	BuiltinAgeDays = "age_days"
	// BuiltinAgeFactor is decay_fn's own result, made available to
	// priority_fn (so priority_fn can apply it). decay_fn itself never sees
	// this — it's what PRODUCES age_factor, not a consumer of it.
	BuiltinAgeFactor = "age_factor"
)

// Result is one task's derived priority — computed at read time.
type Result struct {
	HarpID string

	// Raw is priority_fn's own output (0 if the schema declares no
	// priority_fn at all — see Diagnostics.NoPriorityFn). Computed for every
	// task regardless of status, for completeness/debugging, but only ever
	// feeds Priority for a non-terminal task.
	Raw float64

	// Priority is the rank-normalized [0,Max] display value. Left 0 for a
	// terminal (Done/Archived) task: it never enters the normalization
	// population, and a display priority is meaningless for finished work.
	Priority float64

	// Overridden is true when Priority was forced to Max by the
	// exploited-in-wild override rather than left at its rank-normalized
	// value.
	Overridden bool
}

// Diagnostics reports conditions under which Compute produced a ranking that
// EXISTS but is MEANINGLESS — the silent-no-op failure mode this package
// exists to make impossible to miss (a project with no priority_fn declared,
// or one where formula-referenced tags are absent from the data, still gets
// back a fully-populated, plausible-looking Priority for every task; nothing
// about Result itself distinguishes that from a real ranking). A caller that
// asked for `--sort priority` and gets a degenerate Diagnostics back must
// tell the user, not render the numbers as if they meant something — see
// cmd/taskloom's priorityDiagnosticWarning.
//
// The boolean fields name a ranking that is degenerate outright. The two
// counts and TargetCoverage measure how WELL grounded a ranking that is not
// outright degenerate actually is — a ranking can be non-tied, fully
// populated, and still be decided by one of six formula terms because the
// other five are carried by nothing.
type Diagnostics struct {
	// NoPriorityFn is true when schema declares no priority_fn at all: every
	// task's Raw is 0, so rank-normalization's percentile math still runs but
	// produces a ranking with no basis in any task's data.
	NoPriorityFn bool

	// AllTied is true when more than one non-terminal task exists and every
	// one of them has the exact same Raw score (whether that's because
	// NoPriorityFn is also true, or a configured priority_fn just happens to
	// evaluate identically for this project's current data — e.g. every task
	// is missing the tags the formula reads).
	AllTied bool

	// ScoredTasks counts the non-terminal tasks that carry at least one tag
	// whose target is referenced by priority_fn or decay_fn's own formula
	// text — i.e. tasks that could possibly have moved the needle. A low
	// ScoredTasks against a large non-terminal population is the signature
	// of a schema/data mismatch (formulas declared, but the tags they read
	// are never actually applied to tasks).
	//
	// It is only ever meaningful against NonTerminalTasks: "3 scored" is a
	// healthy ranking of 3 active tasks and a broken one of 300.
	ScoredTasks int

	// NonTerminalTasks is the size of the population ScoredTasks is drawn
	// from — every task whose Status is neither Done nor Archived, i.e.
	// exactly the set rank-normalization runs over. It is ScoredTasks's
	// denominator: without it neither this package's own doc rule ("a low
	// ScoredTasks against a large non-terminal population") nor the warning
	// cmd/taskloom renders from it can be evaluated at all, and a caller
	// cannot recompute it without re-implementing this package's terminal-
	// status predicate.
	NonTerminalTasks int

	// TargetCoverage reports, for EVERY tag target any declared
	// priority_fn/decay_fn reads, how many non-terminal tasks actually carry
	// it. ScoredTasks answers "did ANY formula term reach this task"; this
	// answers "did THIS term reach any task", and only the second one can
	// see a formula whose terms are individually dead. A schema can reference
	// six targets, have two of them applied to half the log and the other
	// four applied to nothing, and still report a healthy ScoredTasks — the
	// ranking is then decided entirely by the two, and the four contribute a
	// constant. Every entry is reported, healthy or not: this type carries
	// the MEASUREMENT, and what counts as too thin to be worth warning about
	// is the renderer's policy (see cmd/taskloom's priorityDiagnosticWarning).
	//
	// Entries are sorted worst coverage first (ascending Tasks, then Target)
	// so a renderer that shows only the head shows the most alarming rows.
	// Nil when no formula is declared, or when it references no tag at all.
	TargetCoverage []TargetCoverage
}

// TargetCoverage is one formula-referenced tag target and the number of
// non-terminal tasks carrying it — the per-term half of Diagnostics's
// coverage signal. Tasks counts PRESENCE of the target on a task, valued or
// bare, exactly as a formula's own Tag(...) lookup sees it.
type TargetCoverage struct {
	// Target is the bare "ns:key" a formula placeholder named, stripped of
	// any "=value" qualifier: "{{triage:blocks-release=*}}" reports as
	// "triage:blocks-release".
	Target string

	// Tasks is how many non-terminal tasks carry Target. Zero means the term
	// is inert — it evaluates to the same absent-tag constant for every task
	// in the project and can never move the ranking.
	Tasks int
}

// Compute derives a Result for every task in all (any status). Raw scores
// are rank-normalized to [0,Max] across the NON-TERMINAL subset only — every
// task whose Status isn't tasks.StatusDone or tasks.StatusArchived (see
// rankNormalize) — so a listing full of finished work never drags every
// live task's percentile down with it.
//
// now is INJECTED (never time.Now() internally): callers pin it for
// deterministic tests; production call sites (internal/shared/tasks/
// operations.ComputeTaskPriorities) pass time.Now().
//
// schema may be nil (no tag_schema at all, or the project declared neither
// priority_fn nor decay_fn) — every task then gets Raw=0 and, per the
// rank-normalization rule below, Priority=Max (a raw score of 0 tied
// across the whole population sits at the 100th percentile of itself). This
// mirrors tagschema.Schema's own nil-receiver-safety: a Compute call is
// never a hard error just because a project hasn't configured formulas. The
// returned Diagnostics tells a caller when that's exactly what happened —
// see its doc.
func Compute(all []tasks.Task, schema *tagschema.Schema, now time.Time) (map[string]Result, Diagnostics, error) {
	priorityFn, decayFn, err := compileAll(schema)
	if err != nil {
		return nil, Diagnostics{}, err
	}
	referenced := referencedTagTargets(priorityFn, decayFn)

	raw := make(map[string]float64, len(all))
	overridden := make(map[string]bool, len(all))
	nonTerminal := make([]string, 0, len(all))
	scoredTasks := 0
	carriers := make(map[string]int, len(referenced))

	for _, t := range all {
		tagValues := resolveTagValues(t.Tags)

		// A folded CreatedAt must be a real point in time: a zero value (the
		// repair path re-adding a displaced task without its original Ts)
		// is corrupt data that would otherwise silently pin
		// this task to the extreme of the decay curve — Compute
		// is the only vantage point that can notice it, so it must say so
		// rather than compute a meaningless age.
		if t.CreatedAt.IsZero() {
			return nil, Diagnostics{}, fmt.Errorf("priority: task %s: CreatedAt is unset — cannot compute age", t.HarpID)
		}
		ageDays := now.Sub(t.CreatedAt).Hours() / 24
		if ageDays < 0 {
			// A CreatedAt in the future (clock skew, a hand-edited log, a
			// restored backup) must not drive age negative: the shipped
			// default decay_fn evaluates 1 + age/(age+90), which is exactly
			// -Inf at age == -90.
			ageDays = 0
		}

		// Step: decay. A Deferred task is parked on purpose — age must not
		// move a parked task's score, so decay is skipped entirely and
		// age_factor is the neutral 1, regardless of what decay_fn would
		// otherwise compute.
		ageFactor := 1.0
		if t.Status != tasks.StatusDeferred && decayFn != nil {
			ageFactor, err = decayFn.Eval(
				lookup(tagValues),
				lookup(map[string]float64{BuiltinAgeDays: ageDays}),
			)
			if err != nil {
				return nil, Diagnostics{}, fmt.Errorf("priority: task %s: decay_fn: %w", t.HarpID, err)
			}
		}

		score := 0.0
		if priorityFn != nil {
			score, err = priorityFn.Eval(
				lookup(tagValues),
				lookup(map[string]float64{BuiltinAgeDays: ageDays, BuiltinAgeFactor: ageFactor}),
			)
			if err != nil {
				return nil, Diagnostics{}, fmt.Errorf("priority: task %s: priority_fn: %w", t.HarpID, err)
			}
		}
		if math.IsNaN(score) || math.IsInf(score, 0) {
			// A non-finite score (typically a NaN-valued tag — see
			// resolveTagValues) must not reach rankNormalize:
			// turn what would otherwise be a hang or a garbage priority into
			// an actionable, named error.
			return nil, Diagnostics{}, fmt.Errorf("priority: task %s: priority_fn produced a non-finite score (%v) — check its tags for a value that is not a finite number", t.HarpID, score)
		}
		raw[t.HarpID] = score

		// Presence alone triggers the override, independent of the tag's
		// (usually absent) value — a comma-ok map lookup, not a truthiness
		// check on the resolved float, so a hypothetical `=0`-valued
		// exploited-in-wild tag still overrides. But only for a
		// non-terminal task: Result.Priority's own contract leaves a
		// Done/Archived task's priority at 0, and the override must not
		// contradict that.
		if _, present := tagValues[ExploitedInWildTarget]; present && !isTerminal(t.Status) {
			overridden[t.HarpID] = true
		}
		if !isTerminal(t.Status) {
			nonTerminal = append(nonTerminal, t.HarpID)
			if hasAnyTarget(tagValues, referenced) {
				scoredTasks++
			}
			countCarriers(tagValues, referenced, carriers)
		}
	}

	diag := Diagnostics{
		NoPriorityFn:     priorityFn == nil,
		ScoredTasks:      scoredTasks,
		NonTerminalTasks: len(nonTerminal),
		TargetCoverage:   coverage(referenced, carriers),
	}
	if len(nonTerminal) > 1 {
		first := raw[nonTerminal[0]]
		tied := true
		for _, id := range nonTerminal[1:] {
			if raw[id] != first {
				tied = false
				break
			}
		}
		diag.AllTied = tied
	}

	normalized := rankNormalize(raw, nonTerminal)

	out := make(map[string]Result, len(all))
	for _, t := range all {
		res := Result{HarpID: t.HarpID, Raw: raw[t.HarpID], Priority: normalized[t.HarpID]}
		if overridden[t.HarpID] {
			res.Priority = Max
			res.Overridden = true
		}
		out[t.HarpID] = res
	}
	return out, diag, nil
}

// compileAll compiles EVERY declared priority_fn and EVERY declared decay_fn
// across every target in schema — so a formula on any target is
// syntax-checked, never silently skipped just because it isn't the one
// target this package happens to evaluate (see this package's history: a
// formula declared on the wrong target used to compile-check nothing at
// all) — but EVALUATES exactly one of each. Declaring more than one
// priority_fn (or more than one decay_fn), across different targets, is an
// AMBIGUITY error naming every conflicting target — never a silent pick of
// whichever one happened to sort first.
//
// Either return value (or both) comes back nil when schema declares nothing
// for that facet, and Compute treats a nil priorityFn as "raw score 0" and a
// nil decayFn as "no decay" (age_factor stays 1) rather than erroring — an
// absent formula is a valid (if inert) configuration, not a fault. A
// DECLARED formula that fails to compile (bad expr syntax) is still a hard
// error: that's a config defect, not an absent-feature no-op.
func compileAll(schema *tagschema.Schema) (priorityFn, decayFn *tagschema.Formula, err error) {
	if priorityFn, err = compileFacet(schema, tagschema.PriorityFnFacet); err != nil {
		return nil, nil, err
	}
	if decayFn, err = compileFacet(schema, tagschema.DecayFnFacet); err != nil {
		return nil, nil, err
	}
	return priorityFn, decayFn, nil
}

// builtinsByFacet is the FIXED set of builtin names each facet's formulas
// may reference, per Compute's own Eval calls: decay_fn's Eval supplies only
// BuiltinAgeDays (age_factor is what decay_fn PRODUCES, not a consumer of —
// see BuiltinAgeFactor's doc), while priority_fn's Eval supplies both
// BuiltinAgeDays and BuiltinAgeFactor. checkKnownBuiltins uses this to fail
// loud at compile time on any OTHER placeholder name, closing the gap
// lookup's bare map index would otherwise leave open: an unrecognized
// Builtin(...) call used to compile clean and silently resolve to 0 at Eval
// time — indistinguishable from a legitimate zero, and
// capable of zeroing an entire multiplicative term with no signal at all.
var builtinsByFacet = map[string]map[string]bool{
	tagschema.PriorityFnFacet: {BuiltinAgeDays: true, BuiltinAgeFactor: true},
	tagschema.DecayFnFacet:    {BuiltinAgeDays: true},
}

// checkKnownBuiltins fails loud when f's compiled source references a
// Builtin(...) placeholder (a "{{name}}" whose name contains no ":", per
// tagschema.CompileFormula's own Tag-vs-Builtin split) that isn't in
// builtinsByFacet[facet] — a typo'd or made-up name, the same class of
// config defect a bad expr syntax already is.
func checkKnownBuiltins(f *tagschema.Formula, facet, target string) error {
	known := builtinsByFacet[facet]
	for _, name := range tagschema.Placeholders(f.Source()) {
		if strings.Contains(name, ":") {
			continue // a Tag(...) reference, not a Builtin(...) one
		}
		if !known[name] {
			return fmt.Errorf("priority: %s on %s: unknown builtin {{%s}} (known: %s)",
				facet, target, name, strings.Join(sortedBuiltinNames(known), ", "))
		}
	}
	return nil
}

// sortedBuiltinNames returns known's keys sorted, for a deterministic error
// message.
func sortedBuiltinNames(known map[string]bool) []string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// compileFacet compiles EVERY target schema declares under facet (via
// schema.Get(facet, target)), syntax-checking each one regardless of how
// many there are, then returns the compiled Formula for the single declared
// target — or nil if none is declared. More than one declared target for
// facet is a returned ambiguity error naming all of them; it is never
// silently resolved by picking one.
func compileFacet(schema *tagschema.Schema, facet string) (*tagschema.Formula, error) {
	var compiled []*tagschema.Formula
	var declared []string
	for _, target := range schema.Targets(facet) {
		raw, ok := schema.Get(facet, target)
		if !ok {
			continue
		}
		f, err := tagschema.CompileFormula(raw)
		if err != nil {
			return nil, fmt.Errorf("priority: %s on %s: %w", facet, target, err)
		}
		if err := checkKnownBuiltins(f, facet, target); err != nil {
			return nil, err
		}
		compiled = append(compiled, f)
		declared = append(declared, target)
	}
	switch len(declared) {
	case 0:
		return nil, nil
	case 1:
		return compiled[0], nil
	default:
		return nil, fmt.Errorf("priority: more than one %s declared (targets %s) — exactly one is allowed",
			facet, strings.Join(declared, ", "))
	}
}

// referencedTagTargets collects every bare tag TARGET ("ns:key", stripped of
// any "=value" qualifier — e.g. "{{triage:impact=capability}}" contributes
// "triage:impact") referenced by any placeholder in any of formulas' source
// text. A nil formula (compileAll returns nil when a facet declares nothing)
// is skipped.
func referencedTagTargets(formulas ...*tagschema.Formula) map[string]bool {
	out := map[string]bool{}
	for _, f := range formulas {
		if f == nil {
			continue
		}
		for _, name := range tagschema.Placeholders(f.Source()) {
			if !strings.Contains(name, ":") {
				continue // a Builtin(...) reference (e.g. age_days), not a tag
			}
			if i := strings.Index(name, "="); i >= 0 {
				name = name[:i]
			}
			out[name] = true
		}
	}
	return out
}

// hasAnyTarget reports whether tagValues (one task's resolveTagValues
// result) carries a key for any target in targets — presence, not value: a
// bare target key is always present once resolveTagValues sees that tag at
// all, whether it's a modifier, a numeric value, or a non-numeric value (see
// resolveTagValues's composite-key doc).
func hasAnyTarget(tagValues map[string]float64, targets map[string]bool) bool {
	for target := range targets {
		if _, ok := tagValues[target]; ok {
			return true
		}
	}
	return false
}

// countCarriers increments carriers[target] for every target in targets that
// tagValues (one non-terminal task's resolveTagValues result) carries. It is
// hasAnyTarget's per-target counterpart: the same presence test, but tallied
// per term instead of collapsed to a single boolean, which is the difference
// between "this task was scored by something" and "this term scored
// something".
func countCarriers(tagValues map[string]float64, targets map[string]bool, carriers map[string]int) {
	for target := range targets {
		if _, ok := tagValues[target]; ok {
			carriers[target]++
		}
	}
}

// coverage turns the referenced-target set and its carrier tally into
// Diagnostics.TargetCoverage: one entry per REFERENCED target — including
// every target with no carrier at all, which is exactly the row a caller
// needs and the one a map built only from observed tags would omit — sorted
// worst coverage first (ascending count, then target name for determinism).
// Returns nil for an empty target set so an unconfigured project reports no
// coverage rather than an empty-but-present slice.
func coverage(targets map[string]bool, carriers map[string]int) []TargetCoverage {
	if len(targets) == 0 {
		return nil
	}
	out := make([]TargetCoverage, 0, len(targets))
	for target := range targets {
		out = append(out, TargetCoverage{Target: target, Tasks: carriers[target]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tasks != out[j].Tasks {
			return out[i].Tasks < out[j].Tasks
		}
		return out[i].Target < out[j].Target
	})
	return out
}

// lookup adapts a plain map into the func(string) float64 shape
// tagschema.Formula.Eval expects for both its tag and builtin resolvers — a
// Go map already returns the zero value (0) for a missing key, which is
// exactly "absent tag" / "unset builtin" for both call sites above.
func lookup(m map[string]float64) func(string) float64 {
	return func(key string) float64 { return m[key] }
}

// resolveTagValues builds the "ns:key" -> value map a formula's Tag(...)
// calls read from, for one task's current (sorted) tag set:
//
//   - a value-carrying tag (parsed via tagma, e.g. "triage:impact=3")
//     contributes its value parsed as float64 under the bare target — 0 when
//     unparseable. A malformed tag value never fails a priority computation;
//     `taskloom lint` (internal/shared/tasks/lint) is what flags it as a
//     violation.
//   - EVERY value-carrying tag ALSO contributes a composite presence key,
//     "target=value" -> 1.0 — in BOTH the numeric and non-numeric case. This
//     is what makes a "{{triage:impact=capability}}" placeholder (a
//     non-numeric, ENUM-valued tag) resolvable at all: without this entry
//     the bare "triage:impact" key alone can't tell a formula which of
//     several possible enum values is actually present, and a non-numeric
//     value would otherwise resolve to a silent, indistinguishable 0 exactly
//     like an absent tag — this composite key is what breaks that tie.
//   - EVERY tag whose target is present AT ALL — valued OR valueless —
//     ALSO contributes a universal presence key, "target=*" -> 1.0. This is
//     what makes a "{{triage:blocks-release=*}}" (or a bare
//     "{{triage:exposed=*}}", with no specific value named) placeholder a
//     working presence test regardless of whether the tag happens to carry a
//     value at all: a formula that only cares WHETHER a target was applied —
//     not which value — doesn't have to enumerate every possible value (or
//     guess whether the tag is ever used bare) to test for it. Without this
//     key a bare "triage:exposed" (no surface named) is invisible to a
//     "{{triage:exposed=*}}" test — it would silently fail to escalate,
//     exactly the class of bug this key exists to close.
//   - a valueless tag (a plain modifier, e.g. "triage:regression")
//     contributes 1.0 under the bare target only (plus the universal
//     presence key above) — presence itself is the signal, there's no value
//     to key a "target=value" composite entry on.
//   - an absent target has no map entry at all, which lookup's zero-value
//     fallback already turns into 0 for any Tag(...) call that asks for it.
//
// Tags are folded in the task's stored (sorted) order, so a duplicate
// target's LAST value wins — mirroring tagschema.Parse's own "last
// declaration wins" convention. This should be unreachable for an
// arity=scalar target post phase-2 write-seam collapse; `taskloom lint`
// flags it as a violation if it somehow still occurs (foreign/legacy data).
func resolveTagValues(tagStrings []string) map[string]float64 {
	out := make(map[string]float64, len(tagStrings))
	for _, raw := range tagStrings {
		t, err := tagma.ParseTag(raw)
		if err != nil {
			continue
		}
		target := tagschema.Target(t)
		if strings.Contains(target, "=") {
			// The three key kinds below share ONE flat namespace, which is
			// unambiguous only while a target cannot contain '='. tagma's
			// quoting extension lets it (`ns:"impact=3"=7` has Key
			// "impact=3"), and such a target's bare entry lands on exactly
			// the slot `ns:impact=3` occupies as a composite presence key —
			// so a formula's presence test would read that tag's arbitrary
			// value instead of 1.0-or-absent. No placeholder can name such a
			// target unambiguously anyway (any `{{a=b}}` reads as the
			// composite form), so it contributes nothing rather than
			// redefining a key that IS referenceable.
			continue
		}
		out[target+"=*"] = 1.0
		if t.Value == nil {
			out[target] = 1.0
			continue
		}
		out[target+"="+*t.Value] = 1.0
		f, err := strconv.ParseFloat(*t.Value, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			// "NaN"/"Inf"/"-Inf" are legal tagma bare tokens and parse
			// cleanly as floats, but a non-finite value is never usable
			// arithmetically — treat it exactly like an
			// unparseable value rather than letting it reach a formula.
			out[target] = 0
			continue
		}
		out[target] = f
	}
	return out
}

// isTerminal reports whether status is one of the completed statuses. The
// store owns that taxonomy (tasks.Statuses's Terminal flag is derived from
// the same predicate), so this defers to it rather than re-deciding it:
// a status added there is classified here without a second edit.
func isTerminal(status string) bool {
	return tasks.IsTerminalStatus(status)
}

// rankNormalize maps every id in population to a percentile-based [0,Max]
// priority: priority(id) = Max * (count of population members whose raw
// score is <= raw[id]) / len(population).
//
// This definition makes ties trivially share a percentile — a tied group's
// "count <= v" is identical for every member by construction, no special
// casing needed — and guarantees the population's maximum raw score (shared
// or not) always reaches exactly Max, since every member's raw score is <=
// the maximum. ids outside population (terminal tasks) are simply absent
// from the returned map — Compute leaves their Priority at the zero value.
func rankNormalize(raw map[string]float64, population []string) map[string]float64 {
	n := len(population)
	out := make(map[string]float64, n)
	if n == 0 {
		return out
	}
	sorted := append([]string(nil), population...)
	sort.Slice(sorted, func(i, j int) bool { return raw[sorted[i]] < raw[sorted[j]] })

	i := 0
	for i < n {
		j := i
		v := raw[sorted[i]]
		for j < n && raw[sorted[j]] == v {
			j++
		}
		// A NaN raw score (e.g. from a stored tag value that parsed as the
		// literal "NaN") is never equal to itself, so the scan above
		// cannot advance j past i on it — without this, the outer loop
		// would spin forever. Treat a lone non-advancing member
		// as its own group of one so the loop always makes progress.
		if j == i {
			j++
		}
		p := Max * float64(j) / float64(n)
		for k := i; k < j; k++ {
			out[sorted[k]] = p
		}
		i = j
	}
	return out
}
