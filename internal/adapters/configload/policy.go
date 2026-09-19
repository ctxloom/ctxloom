package configload

import (
	"fmt"
	"sort"

	kmaps "github.com/knadh/koanf/maps"

	"github.com/ctxloom/ctxloom/internal/adapters/configload/layerscope"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
)

// scopePolicy is ctxloom's layer-scope policy, resolved once: an immutable
// table every override-scope question in this package consults.
var scopePolicy = layerscope.DefaultPolicy()

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

// dropEnginelessAgents refuses every `agents:` entry in ONE decoded config
// layer that declares neither an llm nor profiles, removing it from values in
// place so it never reaches the layer merge, and returns one finding per
// refusal naming the key (agents.<name>) and the file it came from.
//
// It runs per LAYER, deliberately: checked on the MERGED map instead, the
// finding could only name the project's path for a declaration that lives in
// home, and the shell would already be in the view Manager.Update saves back
// into the project file — which is how a home-only `help: {}` came to be
// re-serialised into a committed config. The layer-scope check cannot catch
// it: every per-agent FIELD is ScopeShared, so a home agent declaring any
// field is dropped there (koanf prunes the emptied parent too), but a
// verbatim `{}` has no field for that check to see.
//
// An llm alone is a complete binding (the context is the project default's)
// and profiles alone are too (they carry the llm, falling back to the project
// default backend — see agents.Agent), so only the pair-absent case is
// refused. Whether the llm label or a profile actually exists is resolved
// later, by operations.ResolveAgent, exactly as before.
func dropEnginelessAgents(configPath string, values map[string]any) []config.Warning {
	agentsMap, ok := values["agents"].(map[string]any)
	if !ok {
		return nil
	}
	// Sorted so a layer with several offenders reports them in a stable order.
	names := make([]string, 0, len(agentsMap))
	for name := range agentsMap {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []config.Warning
	for _, name := range names {
		body, _ := agentsMap[name].(map[string]any)
		if bindsEngine(body) {
			continue
		}
		delete(agentsMap, name)
		out = append(out, config.Warning{Kind: config.WarnKindEnginelessAgent, Text: fmt.Sprintf(
			"agents.%s in %s declares neither an llm nor profiles, so nothing could resolve an engine for it — an agent bound to nothing is not an agent; the entry is ignored",
			name, configPath)})
	}
	if len(agentsMap) == 0 {
		delete(values, "agents")
	}
	return out
}

// bindsEngine reports whether a raw agent body names an llm or at least one
// profile. A non-string llm or non-list profiles is a schema violation
// reported separately; here it counts as absent, not as a binding.
func bindsEngine(body map[string]any) bool {
	if llm, ok := body["llm"].(string); ok && llm != "" {
		return true
	}
	if profiles, ok := body["profiles"].([]any); ok && len(profiles) > 0 {
		return true
	}
	return false
}
