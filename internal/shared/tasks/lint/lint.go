// Package lint implements taskloom's ADVISORY triage-standard check
// (`taskloom lint`): read-time only, never a write gate (see
// internal/shared/tasks/operations's write-seam scalar-collapse for the one
// enforcement that DOES happen at write time). Lint exists because that
// write-seam enforcement is arity-only and only as old as phase 2 — a
// project's foreign or pre-phase-2 log data, or a value simply outside a
// declared enum/range, is never rejected at write time, so a read-time sweep
// is the only way to surface it.
package lint

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// SchemaViolationHarpID is the synthetic HarpID a Violation carries when it
// names a defect in the SCHEMA (a declared formula) rather than in any one
// task's data — see the composite-value enum-reference check below. It sorts
// before every real harp ID's lowercase-word shape, so schema-level
// violations surface first.
const SchemaViolationHarpID = "(schema)"

// Violation is one triage-standard violation Lint found on a task.
type Violation struct {
	HarpID string
	Reason string
}

// Result is Lint's full return value: the violations found, plus how much
// was actually checked. CheckedTargets exists because an empty Violations
// slice is ambiguous by itself — it means EITHER "genuinely clean" OR "this
// project's tag_schema declares no enum/range facets, so nothing was
// checked at all" (U121-F01). A caller positioning this as a CI gate (see
// cmd/taskloom's lint command) must distinguish the two.
type Result struct {
	Violations []Violation
	// CheckedTargets is the number of distinct targets this run examined
	// under a declared enum or range facet. Zero means the schema (nil, or
	// one declaring neither facet) gave Lint nothing to check — a clean
	// Violations slice in that case is not evidence of clean data.
	CheckedTargets int
}

// Lint folds every task in all (any status — even a Done task's tags stay
// part of the historical record, so lint checks them too) and reports
// triage-standard violations, sourced ENTIRELY from schema's own
// enum/range/arity facets so the check always matches what THIS project
// actually declares rather than a hardcoded vocabulary:
//
//   - for every target schema declares an enum for (tagschema.EnumFacet, via
//     Schema.Targets): that target's value on a task isn't a member of the
//     declared enum;
//   - for every target schema declares a range for (tagschema.RangeFacet):
//     that target's value on a task doesn't parse as a float, or parses but
//     falls outside the declared [min,max]. The "must parse as a number"
//     check is now purely a CONSEQUENCE of a declared range on that specific
//     target — there is no longer a blanket "impact must be numeric"
//     check independent of what the schema actually declares (that used to
//     wrongly reject legitimate non-numeric — e.g. ENUM — values on a target
//     that was never meant to be numeric in the first place);
//   - a value-qualified placeholder in a declared priority_fn/decay_fn (e.g.
//     "{{triage:impact=foo}}") whose value isn't a member of that target's
//     OWN declared enum — a schema-level defect (Violation.HarpID ==
//     SchemaViolationHarpID), not a task-data one: without this check, a
//     formula that references an enum value that can never actually occur is
//     one more silent-zero surface;
//   - any arity=scalar target (tagschema.IsScalar) carrying more than one
//     DISTINCT value on the same task — should be unreachable after phase
//     2's write-seam collapse, but foreign/legacy data (an older binary, a
//     hand-edited log, a union-merge artifact) might still carry it.
//
// A nil schema reports NOTHING at all — every one of the checks above is
// schema-driven (tagschema.Schema's own nil-receiver-safety means every
// Targets/Get/Enum/Range/IsScalar call already degrades to "nothing
// declared"), so a project with no tag_schema resolved has nothing to check
// tasks against.
//
// Violations are returned sorted by (HarpID, Reason) for deterministic
// output; a returned error is reserved for a malformed RANGE DECLARATION
// itself (a config defect, distinct from a task-data violation).
func Lint(all []tasks.Task, schema *tagschema.Schema) (Result, error) {
	enums := make(map[string][]string)
	for _, target := range schema.Targets(tagschema.EnumFacet) {
		enum, _, err := schema.Enum(target)
		if err != nil {
			return Result{}, fmt.Errorf("lint: %w", err)
		}
		enums[target] = enum
	}

	type bounds struct{ min, max float64 }
	ranges := make(map[string]bounds)
	for _, target := range schema.Targets(tagschema.RangeFacet) {
		min, max, ok, err := schema.Range(target)
		if err != nil {
			return Result{}, fmt.Errorf("lint: %w", err)
		}
		if ok {
			ranges[target] = bounds{min, max}
		}
	}

	// CheckedTargets counts the distinct enum/range-declared targets this
	// run actually examined, so a caller can tell "genuinely clean" apart
	// from "checked nothing" (U121-F01) — a nil schema, or one declaring
	// neither facet, returns an empty violation list that reads identically
	// to a clean project unless this count is also inspected.
	checkedTargets := make(map[string]struct{}, len(enums)+len(ranges))
	for target := range enums {
		checkedTargets[target] = struct{}{}
	}
	for target := range ranges {
		checkedTargets[target] = struct{}{}
	}

	out := formulaEnumRefViolations(schema, enums)

	for _, t := range all {
		values := groupByTarget(t.Tags)

		for target, enum := range enums {
			for _, v := range values[target] {
				if !slices.Contains(enum, v) {
					out = append(out, Violation{t.HarpID,
						fmt.Sprintf("%s=%q is not one of the declared enum values %v", target, v, enum)})
				}
			}
		}

		for target, b := range ranges {
			for _, v := range values[target] {
				f, perr := strconv.ParseFloat(v, 64)
				// strconv.ParseFloat accepts "NaN"/"Inf"/"-Inf" case-
				// insensitively, and every `<`/`>` comparison against NaN
				// is false — so a NaN value would otherwise pass both
				// branches below silently (U121-F08). Treat non-finite
				// exactly like an unparseable value.
				if perr == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
					perr = fmt.Errorf("%q is not a finite number", v)
				}
				if perr != nil {
					out = append(out, Violation{t.HarpID,
						fmt.Sprintf("%s=%q does not parse as a number", target, v)})
					continue
				}
				if f < b.min || f > b.max {
					out = append(out, Violation{t.HarpID,
						fmt.Sprintf("%s=%v is outside the declared range [%v,%v]", target, f, b.min, b.max)})
				}
			}
		}

		targets := make([]string, 0, len(values))
		for target := range values {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			vs := values[target]
			if schema.IsScalar(target) && len(vs) > 1 {
				out = append(out, Violation{t.HarpID,
					fmt.Sprintf("%s carries %d distinct values %v but is declared arity=scalar", target, len(vs), vs)})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].HarpID != out[j].HarpID {
			return out[i].HarpID < out[j].HarpID
		}
		return out[i].Reason < out[j].Reason
	})
	return Result{Violations: out, CheckedTargets: len(checkedTargets)}, nil
}

// formulaEnumRefViolations scans every declared priority_fn/decay_fn formula
// (across every target — see tagschema.Schema.Targets) for a value-qualified
// tag placeholder ("{{ns:key=value}}") and flags one whose value is not a
// member of ns:key's own declared enum, if any. A target with no declared
// enum is skipped (nothing to check the reference against). Violations are
// tagged with SchemaViolationHarpID: this is a defect in the SCHEMA itself,
// not any one task's data.
func formulaEnumRefViolations(schema *tagschema.Schema, enums map[string][]string) []Violation {
	var out []Violation
	for _, facet := range []string{tagschema.PriorityFnFacet, tagschema.DecayFnFacet} {
		for _, target := range schema.Targets(facet) {
			raw, ok := schema.Get(facet, target)
			if !ok {
				continue
			}
			for _, name := range tagschema.Placeholders(raw) {
				eq := strings.Index(name, "=")
				if eq < 0 || !strings.Contains(name[:eq], ":") {
					continue // a builtin, or a bare (non-value-qualified) tag reference
				}
				refTarget, refValue := name[:eq], name[eq+1:]
				if refValue == "*" {
					continue // universal presence test (see internal/shared/tasks/priority's
					// resolveTagValues "target=*" composite key) -- reserved syntax, not an
					// enum-member reference. "*" can never be a real task value in the first
					// place: tagma's bare-token charset already rejects it, so this can never
					// collide with a legitimate enum member.
				}
				enum, hasEnum := enums[refTarget]
				if !hasEnum || slices.Contains(enum, refValue) {
					continue
				}
				out = append(out, Violation{SchemaViolationHarpID, fmt.Sprintf(
					"%s on %s references {{%s}}, but %s=%q is not one of %s's declared enum values %v",
					facet, target, name, refTarget, refValue, refTarget, enum)})
			}
		}
	}
	return out
}

// groupByTarget groups a task's stored tag strings by their "ns:key" target,
// collecting each value-carrying tag's distinct value (a valueless/modifier
// tag carries nothing to check or collapse, so it's skipped here — it can
// never violate an enum/range/cardinality rule).
func groupByTarget(tagStrings []string) map[string][]string {
	out := map[string][]string{}
	for _, raw := range tagStrings {
		t, err := tagma.ParseTag(raw)
		if err != nil {
			continue
		}
		if t.Value == nil {
			continue
		}
		target := tagschema.Target(t)
		out[target] = append(out[target], *t.Value)
	}
	return out
}
