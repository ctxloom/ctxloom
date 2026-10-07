package configload

import (
	"fmt"
	"slices"
	"sort"

	kmaps "github.com/knadh/koanf/maps"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// layerMergeFunc is ctxloom's koanf.WithMergeFunc seam (wired in via
// confload.Product.MergeFunc / confload.Product.MergeLayers): every config
// key deep-merges across layers exactly as confload.Merge documents, EXCEPT
// the keys in layerKeyMerges, each of which carries its own rule.
//
// Every OTHER key is merged via koanf/maps.Merge — the library's own default
// merge, not a reimplementation — so deep-merge-for-maps,
// replace-for-everything-else, and explicit-zero-beats-inheritance all still
// hold for them exactly as confload.Merge documents.
func layerMergeFunc(src, dest map[string]any) error {
	rest := make(map[string]any, len(src))
	for k, v := range src {
		if merge, special := layerKeyMerges[k]; special {
			dest[k] = merge(v, dest[k])
			continue
		}
		rest[k] = v
	}
	kmaps.Merge(rest, dest)
	return nil
}

// layerKeyMerges are the keys whose layer merge is not koanf's default. Each
// func takes the higher layer's value (src) and the lower layers' accumulated
// one (dest, nil when absent) and returns the merged value.
var layerKeyMerges = map[string]func(src, dest any) any{
	"agents":     mergeAgentBindings,
	"companions": mergeCompanionNames,
}

// mergeAgentBindings: a build of this tree let a home config silently
// contribute permissions/coordinator/runtime fields into a project's
// same-named agent — a leaf-by-leaf deep merge fuses fields from different
// layers into one binding neither author wrote. Instead, whichever layer
// NAMES an agent defines that binding ENTIRELY: src replaces dest's
// same-named entry wholesale. An agent named only by a lower layer is left
// untouched — this is not gap-filling per KEY, it is "this whole agent
// binding came from exactly one place", matching decision 3 of the design
// doc.
func mergeAgentBindings(src, dest any) any {
	agentsMap, ok := src.(map[string]any)
	if !ok {
		// Not a map -- a schema violation caught elsewhere. Replace wholesale,
		// consistent with "anything else replaces".
		return src
	}
	destAgents, ok := dest.(map[string]any)
	if !ok {
		destAgents = map[string]any{}
	}
	for name, binding := range agentsMap {
		destAgents[name] = binding
	}
	return destAgents
}

// mergeCompanionNames: a project may ADD companion registrations, never
// replace home's (owner ruling) — the companions a machine registered keep
// running in every project, and a project list, even an empty one, cannot
// unregister them. The result is dest's names in their order, then each name
// only src adds, in src's order: deduplicated and stable.
func mergeCompanionNames(src, dest any) any {
	srcNames, ok := src.([]any)
	if !ok {
		// Not a list -- a schema violation caught elsewhere. Replace.
		return src
	}
	destNames, _ := dest.([]any)
	out := make([]any, 0, len(destNames)+len(srcNames))
	for _, n := range append(slices.Clone(destNames), srcNames...) {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// dropEnginelessAgents refuses every `agents:` entry in ONE decoded config
// layer that declares neither an llm nor profiles, removing it from values in
// place so it never reaches the layer merge, and returns one finding per
// refusal naming the key (agents.<name>) and the file it came from.
//
// It runs per LAYER, deliberately: checked on the MERGED map instead, the
// finding could only name the project's path for a declaration that lives in
// home, and the shell would already be in the view Owner.Update saves back
// into the project file — which is how a home-only `help: {}` came to be
// re-serialised into a committed config.
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
