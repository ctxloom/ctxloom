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
	"sort"
	"strconv"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// TypeTarget/ImpactTarget name the triage standard's two config-driven check
// points: TypeTarget's value is checked against a declared enum, ImpactTarget's
// against a declared numeric range. Both also happen to be arity=scalar
// declared by taskloomconfig.DefaultTagSchema, but the cardinality check below
// applies to every scalar-declared target on a task, not just these two.
const (
	TypeTarget   = "triage:type"
	ImpactTarget = "triage:impact"
)

// Violation is one triage-standard violation Lint found on a task.
type Violation struct {
	HarpID string
	Reason string
}

// Lint folds every task in all (any status — even a Done task's tags stay
// part of the historical record, so lint checks them too) and reports
// triage-standard violations, sourced from schema's own enum/range/arity
// facets so the check always matches what THIS project actually declares
// rather than a hardcoded vocabulary:
//
//   - TypeTarget's value isn't a member of schema's declared enum for it
//     (tagschema.EnumFacet) — skipped entirely if no enum is declared;
//   - ImpactTarget's value doesn't parse as a float, or parses but falls
//     outside schema's declared [min,max] (tagschema.RangeFacet) — the
//     parse check always applies (a non-numeric impact is never valid,
//     declared range or not), the range check only when one is declared;
//   - any arity=scalar target (tagschema.IsScalar) carrying more than one
//     DISTINCT value on the same task — should be unreachable after phase
//     2's write-seam collapse, but foreign/legacy data (an older binary, a
//     hand-edited log, a union-merge artifact) might still carry it.
//
// A nil schema reports nothing at all for the enum/range/cardinality checks
// (tagschema.Schema's own nil-receiver-safety means every Get/Enum/Range/
// IsScalar call already degrades to "nothing declared" — Lint doesn't
// duplicate that guard), except the impact-parses-as-a-number check, which
// holds regardless of schema (an unparseable impact is a defect against the
// well-known shape of the standard, not something a schema opts into).
//
// Violations are returned sorted by (HarpID, Reason) for deterministic
// output; a returned error is reserved for a malformed RANGE DECLARATION
// itself (a config defect, distinct from a task-data violation).
func Lint(all []tasks.Task, schema *tagschema.Schema) ([]Violation, error) {
	min, max, hasRange, err := schema.Range(ImpactTarget)
	if err != nil {
		return nil, fmt.Errorf("lint: %w", err)
	}
	enum, hasEnum := schema.Enum(TypeTarget)

	var out []Violation
	for _, t := range all {
		values := groupByTarget(t.Tags)

		if hasEnum {
			for _, v := range values[TypeTarget] {
				if !contains(enum, v) {
					out = append(out, Violation{t.HarpID,
						fmt.Sprintf("%s=%q is not one of the declared enum values %v", TypeTarget, v, enum)})
				}
			}
		}

		for _, v := range values[ImpactTarget] {
			f, perr := strconv.ParseFloat(v, 64)
			if perr != nil {
				out = append(out, Violation{t.HarpID,
					fmt.Sprintf("%s=%q does not parse as a number", ImpactTarget, v)})
				continue
			}
			if hasRange && (f < min || f > max) {
				out = append(out, Violation{t.HarpID,
					fmt.Sprintf("%s=%v is outside the declared range [%v,%v]", ImpactTarget, f, min, max)})
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
	return out, nil
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

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
