package config

import (
	kmaps "github.com/knadh/koanf/maps"

	"github.com/ctxloom/ctxloom/internal/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
)

// scopePolicy is ctxloom's DefaultPolicy, resolved once — Policy is an
// immutable value built fresh by DefaultPolicy(), so every caller in this
// package shares the identical table rather than each rebuilding it.
var scopePolicy = layerscope.DefaultPolicy()

// dropLayerScopeViolations reports every layerscope violation layer's OWN
// decoded values commit, and removes each one from values in place via
// koanf/maps.Delete — never a bespoke recursive map walker — so the dropped
// key does not survive into the merge that follows. Two call sites, one
// policy: loadConfigLayer calls it beside the existing per-layer schema
// validation, not as a new pass over the merged result — a key allowed at one
// layer but not another must still be caught at ITS OWN layer, exactly like
// an unknown key already is. config_save.go's dropSaveLayerScopeViolations
// calls it a second way, at LayerProject specifically, over what a SAVE is
// about to persist — see that function's own doc for why a value merged in
// from a lower layer must be stopped there too, not just re-discovered (and,
// in FATAL-class strictness, refused on) the next time the file loads.
func dropLayerScopeViolations(layer layerscope.Layer, values map[string]any) []layerscope.Violation {
	violations := scopePolicy.Check(layer, values)
	for _, v := range violations {
		kmaps.Delete(values, v.Path)
	}
	return violations
}

// scopeAllows is ctxloom's confload.Product.ScopeAllows hook: it translates a
// confload.OverrideSource into the layerscope.Layer it corresponds to (env ->
// LayerEnv, flag -> LayerFlag) and asks the SAME policy table the file layers
// are checked against. This is what confload's own doc means by "internal/
// config supplies the concrete scopeAllows" — confload stays free of
// ctxloom's schema; only this function (and layerscope) knows what "agents.*.
// coordinator" means.
func scopeAllows(source confload.OverrideSource, path []string) (bool, string) {
	var layer layerscope.Layer
	switch source {
	case confload.SourceEnv:
		layer = layerscope.LayerEnv
	case confload.SourceFlag:
		layer = layerscope.LayerFlag
	default:
		return true, ""
	}
	rule, ok := scopePolicy.Lookup(path)
	if !ok {
		// No policy opinion on this path -- unknown-key handling is separate
		// machinery (see WarnKindUnknownKey) and this hook only answers scope
		// questions about keys the policy actually names.
		return true, ""
	}
	if rule.Scope.Allows(layer) {
		return true, ""
	}
	why := rule.Scope.Why()
	if rule.Note != "" {
		why += " (" + rule.Note + ")"
	}
	return false, why
}

// agentBindingMergeFunc is ctxloom's koanf.WithMergeFunc seam (wired in via
// confload.Product.MergeFunc / confload.Product.MergeLayers): every config
// key deep-merges across layers exactly as confload.Merge documents, EXCEPT
// "agents" — a build of this tree let a home config silently contribute
// permissions/coordinator/runtime fields into a project's same-named agent,
// producing a binding neither file describes (config-layer-scope design doc,
// "Already wrong #2"). Per-leaf scope alone cannot fix this: `permissions` and
// `profiles` legitimately have DIFFERENT scopes (Shared vs. Shared, but
// `runtime` is Machine), so a leaf-by-leaf deep merge fuses fields from
// different layers into one binding neither author wrote.
//
// Instead, whichever layer NAMES an agent defines that binding ENTIRELY: src
// (the layer being merged in, i.e. the HIGHER-precedence side of this call)
// replaces dest's (the lower layers' accumulated) same-named entry wholesale,
// never fusing it field-by-field. An agent named only by a lower layer is
// left untouched — this is not gap-filling per KEY, it is "this whole agent
// binding came from exactly one place", matching decision 3 of the design
// doc.
//
// Every OTHER key is merged via koanf/maps.Merge — the library's own default
// merge, not a reimplementation — so deep-merge-for-maps,
// replace-for-everything-else, and explicit-zero-beats-inheritance all still
// hold for non-agent keys exactly as confload.Merge documents.
func agentBindingMergeFunc(src, dest map[string]any) error {
	agentsVal, hasAgents := src["agents"]
	if !hasAgents {
		kmaps.Merge(src, dest)
		return nil
	}

	rest := make(map[string]any, len(src))
	for k, v := range src {
		if k == "agents" {
			continue
		}
		rest[k] = v
	}
	kmaps.Merge(rest, dest)

	agentsMap, ok := agentsVal.(map[string]any)
	if !ok {
		// Not a map -- a schema violation caught elsewhere. Replace wholesale,
		// consistent with "anything else replaces".
		dest["agents"] = agentsVal
		return nil
	}
	destAgents, ok := dest["agents"].(map[string]any)
	if !ok {
		destAgents = map[string]any{}
	}
	for name, binding := range agentsMap {
		destAgents[name] = binding
	}
	dest["agents"] = destAgents
	return nil
}
