// Package layerconfig is the generic layered-configuration primitive shared
// by every binary in the ctxloom family (ctxloom, taskloom, ltk, reprise):
// home settings < project settings < environment < CLI arguments, deep-merged
// key by key rather than the first-hit-wins "one source replaces the other"
// resolution each binary historically implemented on its own.
//
// The primitive is deliberately generic: it knows nothing about YAML files,
// config.yaml, CTXLOOM_ROOT, or any other ctxloom-specific concept. A caller
// decodes each source into a presence-tracked map[string]any (e.g. via
// yaml.Unmarshal into a map, or building one programmatically from os.Environ
// / parsed flags) and hands the ordered result to Merge.
//
// # Bootstrap vs. value layering
//
// This package is STAGE 2 only: "given these already-selected layers, what
// are the effective settings". It has no opinion on STAGE 1 — which
// directories or sources produced those layers in the first place (walking up
// from cwd, an explicit --root, a home-directory fallback). Conflating the
// two is circular: a bootstrap input like CTXLOOM_ROOT selects WHICH project
// layer is read, so it cannot itself be a value sitting "between" layers in
// the merge — it determines what the project layer even is. Callers resolve
// stage 1 first (their own directory-discovery logic) and only then build the
// Layer values this package merges.
//
// # Presence vs. absence
//
// A layer distinguishes "this source did not set the key" (key absent from
// Values) from "this source explicitly set the key to its zero value" (key
// present with false / 0 / "" / nil). Only the former inherits from a lower
// layer; the latter always wins, exactly like a higher-precedence layer
// setting any other value. This is why Merge requires map[string]any input
// instead of a struct: a struct's zero value is indistinguishable from
// "unset" without extra pointer/optional machinery, and losing that
// distinction is the exact trap where a project explicitly disabling a
// feature would silently inherit "enabled" from home.
package layerconfig

import "fmt"

// Layer is one named precedence level of raw settings data. Name is for
// diagnostics only (never consulted by Merge); Values is the decoded,
// presence-tracked document for this level — typically the result of
// unmarshaling a YAML/JSON document into map[string]any. A nil or empty
// Values is a legitimate "this layer contributed nothing" (e.g. an absent
// home config.yaml) and merges as a no-op.
type Layer struct {
	Name   string
	Values map[string]any
}

// Merge deep-merges layers in ASCENDING precedence order: layers[0] is the
// weakest (e.g. home), layers[len-1] the strongest (e.g. CLI arguments).
// Typical call: Merge(home, project, env, cli).
//
// Merge rules, applied key by key, recursively:
//
//   - A key present in a higher layer always wins over the same key in a
//     lower layer, REGARDLESS of its value — including its zero value. This
//     is the explicit-zero-beats-inheritance rule: a project setting
//     `feature: false` beats an inherited `feature: true` from home, because
//     presence — not truthiness — is what Merge consults.
//   - A key present in a lower layer but ABSENT from every higher layer is
//     inherited unchanged.
//   - When the same key holds a map[string]any in BOTH layers, the maps are
//     merged recursively (deep merge) rather than one replacing the other —
//     so a project can override one nested field without restating its
//     siblings.
//   - Any other type — including slices/lists — is replaced wholesale by the
//     higher layer's value, never concatenated or otherwise combined. A
//     project's list REPLACES home's list. This is deliberate: concatenation
//     would make it impossible for a project to shorten or remove an
//     inherited entry without inventing a negation syntax. A project that
//     wants "home's list plus one more" restates the whole list.
//
// Merge never mutates its inputs; every returned map (including nested ones)
// is freshly allocated.
func Merge(layers ...Layer) map[string]any {
	result := map[string]any{}
	for _, layer := range layers {
		result = mergeInto(result, layer.Values)
	}
	return result
}

// mergeInto returns a NEW map holding base's entries overlaid by overlay's,
// per the rules documented on Merge. base and overlay are never mutated.
func mergeInto(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, overlayVal := range overlay {
		baseVal, present := out[k]
		if present {
			if baseMap, ok := asMap(baseVal); ok {
				if overlayMap, ok := asMap(overlayVal); ok {
					out[k] = mergeInto(baseMap, overlayMap)
					continue
				}
			}
		}
		// Either the key is new at this layer, or at least one side isn't a
		// map (scalar, list, or a type mismatch between layers) — the higher
		// layer's value replaces the lower layer's wholesale. This is exactly
		// the list-replace rule (D1): a []any is never a map[string]any, so
		// it always falls through to a plain replace, never a concatenation.
		out[k] = overlayVal
	}
	return out
}

// asMap reports whether v decodes as a nested settings map, accepting both
// map[string]any (yaml.v3's default for a mapping node) and
// map[interface{}]interface{} (yaml.v2 / some hand-built callers), so callers
// on either YAML decoder still get deep-merge instead of a silent whole-value
// replace.
//
// A map[interface{}]interface{} entry keyed by something other than a string
// (an int, a bool — legal YAML, just unusual) is stringified via fmt.Sprint
// rather than aborting the whole conversion. Aborting used to make this
// function return (nil, false) the instant it hit ONE such key, which
// silently degraded mergeInto's deep-merge branch into a whole-value REPLACE
// for the entire map — including every sibling key a lower layer set that the
// higher layer never even mentioned. Stringifying keeps the merge honest: the
// map still merges key-by-key, just with that one key's string form standing
// in for its original (non-string) YAML scalar.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[interface{}]interface{}:
		out := make(map[string]any, len(m))
		for k, val := range m {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}
