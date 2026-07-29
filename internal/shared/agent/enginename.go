package agent

import "strings"

// engineAliases maps every accepted alternate spelling of an engine name to
// its canonical form. It is deliberately ONE table for the whole repo: the
// engine names are user-facing shared vocabulary — the same --engine value is
// typed at ltk and at taskloom — and two independently-maintained tables is
// how one spelling came to resolve under one binary and error under the other.
var engineAliases = map[string]string{
	"claudecode":      "claude-code",
	"claude":          "claude-code",
	"agy":             "antigravity",
	"antigravity-cli": "antigravity",
}

// CanonicalEngineName folds name to the canonical engine name it spells:
// lowercased, with any declared alias resolved. It performs no prefix matching
// and no fuzzy matching — a typo must reach the caller's registry unresolved so
// it can be reported, never silently rounded to a real engine.
//
// An unrecognized name is returned lowercased and otherwise unchanged;
// deciding whether a canonical name is actually registered belongs to the
// registry, which knows its own engine set.
func CanonicalEngineName(name string) string {
	want := strings.ToLower(name)
	if canonical, ok := engineAliases[want]; ok {
		return canonical
	}
	return want
}
