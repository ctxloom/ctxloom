package backends

import (
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// The accessors in this file hand a descriptor's DECLARED slots to the
// packages that keep a per-engine view over them, as the agent.Declared value
// itself rather than a (value, ok) pair: the view is a derived function over
// this registry, and "this engine declared none, because ..." has to travel
// with it, or a consumer's miss reads exactly like a forgotten entry. An
// unregistered name returns the zero Declared — undecided — so a caller that
// asks about a name nobody registered gets neither a value nor a reason, and
// can tell that apart from a declared absence.

// TranscriptReadersFor returns the named engine's declaration of the
// version-scoped adapters that read its own transcript store.
func TranscriptReadersFor(name string) agent.Declared[[]vendorreader.VersionedAdapter] {
	d, ok := lookup(name)
	if !ok {
		return agent.Declared[[]vendorreader.VersionedAdapter]{}
	}
	return d.TranscriptReaders
}

// CredentialSeedFor returns the named engine's declaration of the host
// credential material that seeds an isolated home — flattened through Home:
// an engine with no relocatable home has, by the same declaration, nothing
// to seed, and the reason it gives for the one is the reason for the other.
func CredentialSeedFor(name string) agent.Declared[agent.CredentialSeed] {
	d, ok := lookup(name)
	if !ok {
		return agent.Declared[agent.CredentialSeed]{}
	}
	return credentialSeedOf(d)
}

// credentialSeedOf is CredentialSeedFor on a descriptor in hand — what
// Register pushes to isolation before the descriptor is reachable by name.
func credentialSeedOf(d *engine.Descriptor) agent.Declared[agent.CredentialSeed] {
	home, ok := d.Home.Get()
	if !ok {
		return agent.Absent[agent.CredentialSeed](d.Home.AbsentReason())
	}
	return home.Credentials
}

// NoLegacyHistoryReason returns why the named engine's legacy session
// scraper was retired, or "" when it keeps a legacy leg (or is not
// registered — an unknown name is handed the default, never a retirement).
func NoLegacyHistoryReason(name string) string {
	d, ok := lookup(name)
	if !ok {
		return ""
	}
	return d.NoLegacyHistoryReason
}

// RetiredScraperBackendNames lists the registered engines whose legacy
// scraper was retired, sorted — a view over the descriptors, for a gate that
// wants the set rather than one answer.
func RetiredScraperBackendNames() []string {
	return ListWhere(func(name string) bool { return NoLegacyHistoryReason(name) != "" })
}

// ContainerFor returns the named engine's declaration of how a containerized
// run of it is built and authenticated.
func ContainerFor(name string) agent.Declared[agent.EngineContainer] {
	d, ok := lookup(name)
	if !ok {
		return agent.Declared[agent.EngineContainer]{}
	}
	return d.Container
}
