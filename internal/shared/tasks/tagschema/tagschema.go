// Package tagschema parses a project's tag-schema DECLARATIONS — strings
// written in tagma's own tag syntax that assert facets over tag keys used on
// tasks — into an in-memory, queryable Schema. Declarations come from
// taskloom's own config.yaml (see internal/taskloom/config's TagSchema
// field), never from a task tag itself: internal/shared/tasks/operations's
// validateTag rejects a user-supplied task tag in the reserved namespace
// this package owns (FacetNamespace and everything under it), so config is
// the only way to populate a Schema.
//
// A declaration looks like:
//
//	tagma.arity:"triage:type"=scalar
//
// Parsed via tagma.ParseTag, that yields Namespace="tagma.arity",
// Key="triage:type" (quoted so the embedded ':' survives as opaque
// content — see tagma's QUOTING extension), Value="scalar". This package
// strips the "tagma." prefix off the namespace to get the FACET ("arity"),
// and stores Key as the opaque TARGET string under that facet: facet ->
// target -> value. The target is never itself re-parsed as a tag — it is
// compared, as a plain string, against the "namespace:key" identity a real
// task tag reconstructs to (see Target).
//
// ArityFacet, whose only legal value is ArityScalar, is enforced at WRITE
// time (internal/shared/tasks/operations's write seam collapses a task's
// tags so at most one survives per scalar target — "last wins").
// PriorityFnFacet/DecayFnFacet and EnumFacet/RangeFacet are read-time-only:
// the former pair is compiled (CompileFormula, this package) and evaluated
// by internal/shared/tasks/priority.Compute; the latter pair is checked by
// internal/shared/tasks/lint.Lint. Neither ever blocks a write — see
// operations's own arity-only enforcement.
package tagschema

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
)

// FacetNamespace is the reserved tag namespace prefix every tag_schema
// declaration's namespace must start with ("tagma", or "tagma.<facet>").
// operations.validateTag rejects a user-supplied task tag in this namespace
// (or any "tagma.*" sub-namespace) for the same reason: declarations are
// config-only, never task-writable.
const FacetNamespace = "tagma"

// Facet name and value constants. ArityFacet/ArityScalar are enforced at
// write time (scalar-collapse). PriorityFnFacet/DecayFnFacet declare the
// mustache-form formulas internal/shared/tasks/priority compiles (via
// CompileFormula, this package) and evaluates read-time. EnumFacet/RangeFacet
// declare the closed value set / numeric range `taskloom lint` validates a
// tag's value against — see Enum/Range below. HideFacet declares DISPLAY-time
// hiding (tagma.hide:<target>=<bool>, e.g. `tagma.hide:"triage:cwe"=true`) —
// see HideFacts below; never enforced at write time, and never affects
// storage, matching, or ranking, only what a listing shows. TypeFacet
// declares a target's client-loadable comparison type (tagma.type:<target>=
// <name>, e.g. `tagma.type:"triage:blocks-release"=semver`) — a query-time-
// only declaration (like Hide) that switches '>' '>=' '<' '<=' matching over
// to a registered tagma.TypeComparator instead of tagma's own numeric
// grammar; see SemverTypeName and internal/shared/tasks, the only current
// registrant.
const (
	ArityFacet      = "arity"
	ArityScalar     = "scalar"
	PriorityFnFacet = "priority_fn"
	DecayFnFacet    = "decay_fn"
	EnumFacet       = "enum"
	RangeFacet      = "range"
	HideFacet       = "hide"
	TypeFacet       = "type"
)

// SemverTypeName is the tagma.type value naming taskloom's semver
// TypeComparator (tagma SPEC.md §9, "client-loadable type comparison"):
// internal/shared/tasks registers a TypeComparator under this exact name, so
// a declaration `tagma.type:<target>=semver` (e.g. DefaultTagSchema's
// `triage:blocks-release`) switches that target's relational-operator
// matching ('>' '>=' '<' '<=') from tagma's own numeric grammar (which
// rejects a two-dot value like "0.7.0" outright) to real SemVer 2.0.0
// precedence.
const SemverTypeName = "semver"

// Schema is the parsed form of a tag_schema declaration list: facet name ->
// target string -> declared value.
type Schema struct {
	facets map[string]map[string]string
}

// Parse parses every declaration in decls (in order; a later declaration for
// the same facet+target overwrites an earlier one, "last wins" — mirroring
// a real config file where a later line is the more specific intent) into a
// Schema. A malformed declaration — one tagma.ParseTag rejects, one with no
// namespace, one whose namespace isn't "tagma" or "tagma.<facet>", or one
// with no value — is a returned error naming the offending declaration:
// fail loud, never silently drop a schema entry (a dropped arity=scalar
// declaration would silently stop collapsing a task's tags).
func Parse(decls []string) (*Schema, error) {
	s := &Schema{facets: map[string]map[string]string{}}
	for _, decl := range decls {
		if err := s.add(decl); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Schema) add(decl string) error {
	trimmed := strings.TrimSpace(decl)
	tag, err := tagma.ParseTag(trimmed)
	if err != nil {
		return fmt.Errorf("tag_schema: invalid declaration %q: %w", decl, err)
	}
	if tag.Namespace == nil {
		return fmt.Errorf("tag_schema: declaration %q has no namespace (expected %q or %q.<facet>)",
			decl, FacetNamespace, FacetNamespace)
	}
	ns := *tag.Namespace
	prefix := FacetNamespace + "."
	if ns == FacetNamespace || !strings.HasPrefix(ns, prefix) {
		return fmt.Errorf("tag_schema: declaration %q's namespace %q must be %q.<facet> (e.g. %q.%s)",
			decl, ns, FacetNamespace, FacetNamespace, ArityFacet)
	}
	facet := strings.TrimPrefix(ns, prefix)
	if tag.Value == nil {
		return fmt.Errorf("tag_schema: declaration %q has no value (expected %s:%q=<value>)", decl, ns, tag.Key)
	}
	m := s.facets[facet]
	if m == nil {
		m = map[string]string{}
		s.facets[facet] = m
	}
	m[tag.Key] = *tag.Value
	return nil
}

// Get returns the declared value for (facet, target) and whether it was
// declared at all. A nil Schema behaves exactly like an empty one — no
// facet is ever declared — so a caller never needs a separate nil check
// (mirroring schema.ConfigValidator.KnownPath's nil-receiver-safety
// elsewhere in this codebase's config loading).
func (s *Schema) Get(facet, target string) (string, bool) {
	if s == nil {
		return "", false
	}
	m := s.facets[facet]
	if m == nil {
		return "", false
	}
	v, ok := m[target]
	return v, ok
}

// Targets returns every target declared for facet, sorted — e.g.
// Targets(PriorityFnFacet) names every target a priority_fn is declared
// against, however many there are. A caller that expects at most one
// (internal/shared/tasks/priority's compileAll for priority_fn/decay_fn) is
// responsible for treating more than one as an ambiguity; this accessor just
// reports what's declared, same as Get. Nil-receiver-safe like every other
// accessor on Schema: a nil Schema declares nothing, so Targets returns nil
// rather than panicking.
func (s *Schema) Targets(facet string) []string {
	if s == nil {
		return nil
	}
	m := s.facets[facet]
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for target := range m {
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}

// IsScalar reports whether target is declared arity=scalar — at most one
// tag with that (namespace, key) identity per task.
func (s *Schema) IsScalar(target string) bool {
	v, ok := s.Get(ArityFacet, target)
	return ok && v == ArityScalar
}

// PriorityFn returns target's declared priority_fn formula (mustache form,
// e.g. `"{{triage:impact}} * {{age_factor}}"`) and whether one was declared.
// Compile it with CompileFormula before evaluating.
func (s *Schema) PriorityFn(target string) (string, bool) {
	return s.Get(PriorityFnFacet, target)
}

// DecayFn returns target's declared decay_fn formula (mustache form) and
// whether one was declared. Compile it with CompileFormula before
// evaluating.
func (s *Schema) DecayFn(target string) (string, bool) {
	return s.Get(DecayFnFacet, target)
}

// Type returns target's declared tagma.type name (SPEC.md §9) — e.g.
// SemverTypeName — and whether one was declared. This is the read side of a
// `tagma.type:<target>=<name>` declaration; the write side (bridging every
// declared type into a tagma.Index's config so its query-time typeConfig
// scan finds it, and registering the actual TypeComparator) lives in
// internal/shared/tasks, this package's only current TypeFacet consumer.
func (s *Schema) Type(target string) (string, bool) {
	return s.Get(TypeFacet, target)
}

// Enum returns target's declared closed enum values — a facet="enum"
// declaration whose raw value is a comma-separated list, e.g.
// `tagma.enum:"triage:type"="correctness,security,..."` — and whether one
// was declared. Each returned value is trimmed; empty entries (e.g. a
// trailing comma) are dropped rather than surfacing as a spurious "" member.
func (s *Schema) Enum(target string) ([]string, bool) {
	v, ok := s.Get(EnumFacet, target)
	if !ok {
		return nil, false
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out, true
}

// Range returns target's declared numeric [min,max] range — a facet="range"
// declaration whose raw value is "min,max", e.g.
// `tagma.range:"triage:impact"="0,5"` — and whether one was declared. A
// declared value that isn't exactly two comma-separated floats is a returned
// error naming the bad declaration (fail loud: never silently skip range
// validation over a typo in config).
func (s *Schema) Range(target string) (min, max float64, ok bool, err error) {
	v, has := s.Get(RangeFacet, target)
	if !has {
		return 0, 0, false, nil
	}
	parts := strings.Split(v, ",")
	if len(parts) != 2 {
		return 0, 0, true, fmt.Errorf("tag_schema: range declaration for %q must be \"min,max\", got %q", target, v)
	}
	min, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, true, fmt.Errorf("tag_schema: range declaration for %q has invalid min %q: %w", target, parts[0], err)
	}
	max, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, true, fmt.Errorf("tag_schema: range declaration for %q has invalid max %q: %w", target, parts[1], err)
	}
	return min, max, true, nil
}

// HideFacts returns every declared tagma.hide:<target>=<bool> fact in s, as
// tagma.HideFact values ready for tagma.HideConfigFromPatterns — the
// display-time hide config a caller filters a task's tags through (see
// cmd/taskloom's hideConfigFor/visibleTags). A declared value other than
// "true"/"false" is skipped (mirrors tagma's own hideFact decoding: an
// uninterpretable value configures nothing rather than erroring, since it
// only ever narrows what's hidden, never what's stored). A nil Schema
// returns nil, same nil-receiver-safety as Get. Order is unspecified (map
// iteration) — HideConfigFromPatterns's resolution doesn't depend on fact
// order (a target with both a true and a false fact on record resolves
// hidden regardless).
func (s *Schema) HideFacts() []tagma.HideFact {
	if s == nil {
		return nil
	}
	m := s.facets[HideFacet]
	if len(m) == 0 {
		return nil
	}
	facts := make([]tagma.HideFact, 0, len(m))
	for target, v := range m {
		switch v {
		case "true":
			facts = append(facts, tagma.HideFact{Target: target, Hide: true})
		case "false":
			facts = append(facts, tagma.HideFact{Target: target, Hide: false})
		default:
			continue
		}
	}
	return facts
}

// Target reconstructs the "namespace:key" (or bare "key", if t carries no
// namespace) identity string a stored task tag's parsed form corresponds
// to — the same literal shape a tag_schema declaration's (quoted) target
// key is written in, e.g. "triage:type". Two tags with the same Target
// share the (namespace, key) pair a scalar declaration constrains, however
// their values differ.
func Target(t tagma.Tag) string {
	if t.Namespace == nil {
		return t.Key
	}
	return *t.Namespace + ":" + t.Key
}
