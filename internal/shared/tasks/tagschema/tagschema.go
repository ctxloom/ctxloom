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
// Phase 2 defines and enforces exactly one facet, ArityFacet, whose only
// legal value is ArityScalar (internal/shared/tasks/operations's write seam
// collapses a task's tags so at most one survives per scalar target — "last
// wins"). PriorityFnFacet and DecayFnFacet are parsed and stored like any
// other facet (Schema.Get can retrieve them) but are not evaluated until a
// later phase — see the design doc this package's tests cite.
package tagschema

import (
	"fmt"
	"strings"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
)

// FacetNamespace is the reserved tag namespace prefix every tag_schema
// declaration's namespace must start with ("tagma", or "tagma.<facet>").
// operations.validateTag rejects a user-supplied task tag in this namespace
// (or any "tagma.*" sub-namespace) for the same reason: declarations are
// config-only, never task-writable.
const FacetNamespace = "tagma"

// Facet name and value constants. ArityFacet/ArityScalar are enforced in
// phase 2; PriorityFnFacet/DecayFnFacet are recognized (parsed and stored)
// but their mustache-equation values are inert until a later phase.
const (
	ArityFacet      = "arity"
	ArityScalar     = "scalar"
	PriorityFnFacet = "priority_fn"
	DecayFnFacet    = "decay_fn"
)

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

// IsScalar reports whether target is declared arity=scalar — at most one
// tag with that (namespace, key) identity per task.
func (s *Schema) IsScalar(target string) bool {
	v, ok := s.Get(ArityFacet, target)
	return ok && v == ArityScalar
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
