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
//     line to computeBuiltins, not by changing the formula bridge).
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
	"regexp"
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
// deliberately a small, named, EXTENSIBLE set: adding a new one is a single
// line in computeBuiltins plus a doc line here — no change to the formula
// bridge itself, since Builtin(name) already resolves any name from a plain
// map.
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
	ScoredTasks int
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

	for _, t := range all {
		tagValues := resolveTagValues(t.Tags)
		ageDays := now.Sub(t.CreatedAt).Hours() / 24

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
		raw[t.HarpID] = score

		// Presence alone triggers the override, independent of the tag's
		// (usually absent) value — a comma-ok map lookup, not a truthiness
		// check on the resolved float, so a hypothetical `=0`-valued
		// exploited-in-wild tag still overrides.
		if _, present := tagValues[ExploitedInWildTarget]; present {
			overridden[t.HarpID] = true
		}
		if !isTerminal(t.Status) {
			nonTerminal = append(nonTerminal, t.HarpID)
			if hasAnyTarget(tagValues, referenced) {
				scoredTasks++
			}
		}
	}

	diag := Diagnostics{NoPriorityFn: priorityFn == nil, ScoredTasks: scoredTasks}
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
	if priorityFn, err = compileFacet(schema, tagschema.PriorityFnFacet, schema.PriorityFn); err != nil {
		return nil, nil, err
	}
	if decayFn, err = compileFacet(schema, tagschema.DecayFnFacet, schema.DecayFn); err != nil {
		return nil, nil, err
	}
	return priorityFn, decayFn, nil
}

// compileFacet compiles EVERY target schema declares under facet (via
// getter — schema.PriorityFn or schema.DecayFn), syntax-checking each one
// regardless of how many there are, then returns the compiled Formula for
// the single declared target — or nil if none is declared. More than one
// declared target for facet is a returned ambiguity error naming all of
// them; it is never silently resolved by picking one.
func compileFacet(schema *tagschema.Schema, facet string, getter func(string) (string, bool)) (*tagschema.Formula, error) {
	var compiled []*tagschema.Formula
	var declared []string
	for _, target := range schema.Targets(facet) {
		raw, ok := getter(target)
		if !ok {
			continue
		}
		f, err := tagschema.CompileFormula(raw)
		if err != nil {
			return nil, fmt.Errorf("priority: %s on %s: %w", facet, target, err)
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

// formulaTagPlaceholderPattern mirrors tagschema.CompileFormula's own
// "{{...}}" placeholder syntax awareness, duplicated here rather than
// exported from tagschema: this is a diagnostics-only concern (which bare
// tag TARGETS a compiled formula's source text references, used only to
// size Diagnostics.ScoredTasks), not part of the compile/eval bridge itself.
var formulaTagPlaceholderPattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*}}`)

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
		for _, m := range formulaTagPlaceholderPattern.FindAllStringSubmatch(f.Source(), -1) {
			name := m[1]
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
		out[target+"=*"] = 1.0
		if t.Value == nil {
			out[target] = 1.0
			continue
		}
		out[target+"="+*t.Value] = 1.0
		f, err := strconv.ParseFloat(*t.Value, 64)
		if err != nil {
			out[target] = 0
			continue
		}
		out[target] = f
	}
	return out
}

// isTerminal reports whether status is one of the completed statuses
// (Done/Archived) — the tasks package's own statusIsDone predicate isn't
// exported, so this mirrors it against the two exported status constants
// rather than importing an internal helper.
func isTerminal(status string) bool {
	return status == tasks.StatusDone || status == tasks.StatusArchived
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
		p := Max * float64(j) / float64(n)
		for k := i; k < j; k++ {
			out[sorted[k]] = p
		}
		i = j
	}
	return out
}
