package isolation

import (
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Every engine-keyed table in this package holds CANONICAL engine names
// (agent.CanonicalEngineName) on the key side and resolves through the
// repo-wide alias table on the read side. The invariant lives in the data, not
// in any caller: an engine name reaches this package from CLI flags, from
// decoded config, from stored agent definitions and from the backend registry's
// init-time push, and no boundary is common to all of them.
//
// The failure this prevents is silent. These tables gate credential seeding,
// per-agent config homes and container auth; a lookup that MISSES because the
// caller spelled the engine "claude" rather than "claude-code" does not error —
// it seeds nothing, exports no scoped home var, and launches an engine that
// authenticates against nothing while reporting success.
//
// A MISS remains a legitimate answer. An engine with no entry has no seedable
// state, no registered writer, or no vetted container auth, and each read site
// already has a defined behaviour for that (a silent no-op, a ClassIsolation
// finding, or the fail-closed default container spec). Canonicalizing changes
// only WHICH names miss: an unknown name still arrives lowercased and
// unresolved, so it misses exactly as before and is never rounded to a real
// engine.

// assertCanonicalEngineKey panics when key is a spelling the alias table would
// rewrite. Such a key is installed where no lookup can reach it, which is
// indistinguishable from the engine having no entry at all.
func assertCanonicalEngineKey(table, key string) {
	if canonical := agent.CanonicalEngineName(key); canonical != key {
		panic("isolation: " + table + " key " + key + " is not canonical (want " + canonical + ")")
	}
}

// init pins the key side of every engine-keyed table declared as a literal in
// this package. The runtime-populated tables (instanceConfigWriters,
// credentialProjectors) assert in their Register functions instead, and the
// engineContainerSpecFor switch is pinned through the rosters that enumerate
// its arms.
func init() {
	for name := range credentialSeedSpecs {
		assertCanonicalEngineKey("credentialSeedSpecs", name)
	}
	for name := range backendsWithNoGlobalState {
		assertCanonicalEngineKey("backendsWithNoGlobalState", name)
	}
	for _, name := range ContainerAuthEngines() {
		assertCanonicalEngineKey("ContainerAuthEngines", name)
	}
	for _, name := range composableEngines() {
		assertCanonicalEngineKey("composableEngines", name)
	}
}

// credentialSeedSpecFor resolves engine to its credential-seed descriptor
// through the repo-wide alias table. It is the ONLY read path into
// credentialSeedSpecs, so the key side and the read side cannot drift.
//
// ok=false means "this engine has no declared isolation lever", which every
// caller already handles; it never means "this engine is spelled differently".
func credentialSeedSpecFor(engine string) (credentialSeedSpec, bool) {
	spec, ok := credentialSeedSpecs[agent.CanonicalEngineName(engine)]
	return spec, ok
}

// backendHasNoGlobalState reports whether backend is on the
// backendsWithNoGlobalState exemption list, resolved through the alias table so
// an aliased spelling cannot lose the exemption and draw a spurious
// "unregistered backend" isolation finding.
func backendHasNoGlobalState(backend string) bool {
	return backendsWithNoGlobalState[agent.CanonicalEngineName(backend)]
}
