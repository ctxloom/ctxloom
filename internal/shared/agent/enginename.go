package agent

import (
	"sort"
	"strings"
)

// engineAliases maps every accepted alternate spelling of an engine name to its
// canonical form. One table for the whole repo: engine names are user-facing
// shared vocabulary, typed as --engine at both ltk and taskloom, and two tables
// drift into one spelling resolving under one binary and erroring under the other.
var engineAliases = map[string]string{
	"claudecode": "claude-code",
	"claude":     "claude-code",
}

// CanonicalEngineName lowercases name and resolves any declared alias. No
// prefix or fuzzy matching: a typo reaches the caller's registry unresolved so
// it can be reported, never rounded to a real engine. An unrecognized name
// comes back lowercased and unchanged — whether a canonical name is actually
// registered is the registry's question, since it knows its own engine set.
func CanonicalEngineName(name string) string {
	want := strings.ToLower(name)
	if canonical, ok := engineAliases[want]; ok {
		return canonical
	}
	return want
}

// EngineNameAliases returns every alternate spelling that resolves to
// canonical, sorted, or nil when it has none. It exists so a registry
// refusing an unknown engine name can enumerate what it WOULD have accepted
// without keeping its own copy of the alias table — the copy this table was
// consolidated to eliminate. The canonical name itself is not included: the
// caller already knows it, since it asked about it.
func EngineNameAliases(canonical string) []string {
	var aliases []string
	for alias, target := range engineAliases {
		if target == canonical {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}
