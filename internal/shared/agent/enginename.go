package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// engineAliases maps every accepted alternate spelling of an engine name to its
// canonical form. One table for the whole process: engine names are user-facing
// shared vocabulary, typed as --engine at ltk and taskloom as well as ctxloom,
// and two tables drift into one spelling resolving under one binary and
// erroring under the other.
//
// It is POPULATED, never authored here: each engine package declares its own
// spellings, and every binary composes them through its own root —
// internal/lm/backends.Register from each descriptor for ctxloom, the lean
// name root (internal/lm/enginenames) for the binaries that must not link the
// descriptors. Shared code owns the SHAPE of the vocabulary and the lookup;
// the contents come up from the engines. A binary that composes nothing
// resolves no alias, which is the honest answer rather than a stale one.
var (
	engineAliasMu sync.RWMutex
	engineAliases = map[string]string{}
)

// RegisterEngineAliases installs canonical's alternate spellings. Re-registering
// an identical mapping is a no-op, so two composition roots may name one
// engine in one process; an alias already bound to a DIFFERENT engine is
// refused, as is any spelling the shape rules (ValidateEngineAliases) reject —
// and a refused call installs nothing.
func RegisterEngineAliases(canonical string, aliases []string) error {
	if err := ValidateEngineAliases(canonical, aliases); err != nil {
		return err
	}
	engineAliasMu.Lock()
	defer engineAliasMu.Unlock()
	for _, a := range aliases {
		if bound, ok := engineAliases[a]; ok && bound != canonical {
			return fmt.Errorf("engine alias %q already resolves to %q; cannot also resolve to %q", a, bound, canonical)
		}
	}
	for _, a := range aliases {
		engineAliases[a] = canonical
	}
	return nil
}

// ValidateEngineAliases holds the shape every engine's name declaration must
// have to be reachable through CanonicalEngineName: a lowercase canonical name
// and lowercase aliases, none equal to the name, none repeated. One rule,
// applied by the descriptor gate and by registration alike.
func ValidateEngineAliases(canonical string, aliases []string) error {
	if canonical == "" {
		return fmt.Errorf("engine name is empty")
	}
	if canonical != strings.ToLower(canonical) {
		return fmt.Errorf("engine %s: name must be lowercase", canonical)
	}
	seen := map[string]bool{}
	for _, a := range aliases {
		switch {
		case a == canonical:
			return fmt.Errorf("engine %s: alias %q equals the name", canonical, a)
		case a != strings.ToLower(a):
			return fmt.Errorf("engine %s: alias %q must be lowercase", canonical, a)
		case seen[a]:
			return fmt.Errorf("engine %s: alias %q declared twice", canonical, a)
		}
		seen[a] = true
	}
	return nil
}

// ForgetEngineAliases removes every spelling that resolves to canonical —
// the registry's unwind for a test's synthetic engine, so its aliases do not
// linger for later tests to resolve through.
func ForgetEngineAliases(canonical string) {
	engineAliasMu.Lock()
	defer engineAliasMu.Unlock()
	for alias, target := range engineAliases {
		if target == canonical {
			delete(engineAliases, alias)
		}
	}
}

// resetEngineAliasesForTesting empties the table, for a test that composes
// its own fixture engines.
func resetEngineAliasesForTesting() {
	engineAliasMu.Lock()
	defer engineAliasMu.Unlock()
	engineAliases = map[string]string{}
}

// CanonicalEngineName lowercases name and resolves any registered alias. No
// prefix or fuzzy matching: a typo reaches the caller's registry unresolved so
// it can be reported, never rounded to a real engine. An unrecognized name
// comes back lowercased and unchanged — whether a canonical name is actually
// registered is the registry's question, since it knows its own engine set.
func CanonicalEngineName(name string) string {
	want := strings.ToLower(name)
	engineAliasMu.RLock()
	defer engineAliasMu.RUnlock()
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
	engineAliasMu.RLock()
	defer engineAliasMu.RUnlock()
	for alias, target := range engineAliases {
		if target == canonical {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}
