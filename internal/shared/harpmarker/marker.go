// Package harpmarker formats and recovers the harp self-identification marker.
//
// A session emits a greppable marker into its own backend transcript at start:
//
//	<ctxloom name="plump-loose-sash" kind="harp" />
//
// It is a self-closing element carrying point metadata (deliberately distinct
// from the <ctxloom-context>…</ctxloom-context> content wrapper) so read-time
// resolution can answer "which harp owns this transcript?" without depending on
// the session index, the PID registry, the binding, or hook bookkeeping — all of
// which have proven unreliable. kind="harp" namespaces the marker so the same
// element can carry other point metadata later (e.g. kind="resumed-from").
//
// The marker is written through a backend's SessionStart injection path, so it
// lands in the transcript as escaped JSON (Claude wraps hook stdout in an
// attachment, and encoding/json escapes '<' to <). Scan therefore decodes
// nested JSON to recover the marker regardless of how deeply the backend nests
// or escapes it.
package harpmarker

import (
	"encoding/json"
	"regexp"
	"strings"
)

// markerRe matches a self-closing <ctxloom … kind="harp" … /> element. The
// attribute scan stops at the first '>' so it can't run past the element.
var markerRe = regexp.MustCompile(`<ctxloom\b[^>]*\bkind="harp"[^>]*?/?>`)

// nameRe pulls the name attribute out of a matched marker element.
var nameRe = regexp.MustCompile(`\bname="([^"]+)"`)

// unrepresentable are the characters a harp name cannot carry through the
// marker's attribute syntax. harp.Validate is deliberately permissive on
// charset (it rejects only path separators, control characters and edge
// whitespace), so both of these reach Format as legal harp names.
//
// A '"' closes the name attribute early, so the element reads back as a
// DIFFERENT, possibly real harp — the marker misattributes the transcript
// instead of failing. A '>' closes the element before kind="harp" is
// reached, so nothing recognises it at all.
const unrepresentable = `">`

// Format returns the harp marker for the given harp name, or "" when there is
// nothing this package can say about it: an empty name (nothing to identify)
// or a name the marker syntax cannot carry without changing which harp the
// marker names. Whatever Format returns always reads back through Find as the
// harp it was given — a marker that names the wrong harp is worse than no
// marker, because a caller can detect the absent one and not the wrong one.
func Format(harp string) string {
	if harp == "" || strings.ContainsAny(harp, unrepresentable) {
		return ""
	}
	return `<ctxloom name="` + harp + `" kind="harp" />`
}

// Find returns the harp name from the first harp marker in a fully-decoded
// string, or "" if none is present.
func Find(s string) string {
	for _, elem := range markerRe.FindAllString(s, -1) {
		if m := nameRe.FindStringSubmatch(elem); m != nil {
			return m[1]
		}
	}
	return ""
}

// Scan recovers the harp name from one raw transcript line (JSONL), or "" if the
// line carries no harp marker. The marker is typically buried under one or two
// layers of JSON escaping (Claude: attachment.stdout → hook JSON →
// additionalContext). Scan first checks the raw bytes, then walks the decoded
// JSON tree, recursively decoding any string leaf that is itself JSON, so the
// marker is found at whatever nesting depth the backend produced.
func Scan(line []byte) string {
	if h := Find(string(line)); h != "" {
		return h
	}
	var v any
	if err := json.Unmarshal(line, &v); err != nil {
		return ""
	}
	return findInValue(v)
}

// findInValue searches a decoded JSON value for the harp marker, descending into
// objects, arrays, and string leaves that are themselves JSON.
func findInValue(v any) string {
	switch t := v.(type) {
	case string:
		if h := Find(t); h != "" {
			return h
		}
		// A string leaf may itself be JSON (nested hook stdout); decoding it
		// strips one more layer of escaping so a deeper marker surfaces.
		if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			var inner any
			if json.Unmarshal([]byte(t), &inner) == nil {
				return findInValue(inner)
			}
		}
	case map[string]any:
		for _, vv := range t {
			if h := findInValue(vv); h != "" {
				return h
			}
		}
	case []any:
		for _, vv := range t {
			if h := findInValue(vv); h != "" {
				return h
			}
		}
	}
	return ""
}
