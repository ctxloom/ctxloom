package backends

import (
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Kind is the named engine's kind — the value the port's methods are asked
// on — by EXACT match on the registered name.
func Kind(name string) (engine.Engine, bool) {
	r, ok := records[name]
	if !ok {
		return nil, false
	}
	return r.kind, true
}

// TranscriptReadersFor is the named engine's transcript readers as the
// transcript adapter consumes them: every Engine.Transcripts value that IS a
// vendorreader adapter. ok is false for an unregistered name or an engine
// with no readers — the two cases every caller treats alike (no vendor
// store to read back).
func TranscriptReadersFor(name string) ([]vendorreader.VersionedAdapter, bool) {
	kind, ok := Kind(name)
	if !ok {
		return nil, false
	}
	var out []vendorreader.VersionedAdapter
	for _, r := range kind.Transcripts() {
		if a, ok := r.(vendorreader.VersionedAdapter); ok {
			out = append(out, a)
		}
	}
	return out, len(out) > 0
}

// CredentialSeedFor is the named engine's credential seed off its Home: what
// seeds a relocated home, absent with the reason when nothing does, the zero
// (undecided) slot for an unregistered name.
func CredentialSeedFor(name string) engine.Declared[engine.CredentialSeed] {
	kind, ok := Kind(name)
	if !ok {
		return engine.Declared[engine.CredentialSeed]{}
	}
	home := kind.Home()
	if !home.Relocates() {
		return engine.Absent[engine.CredentialSeed](name + " relocates no engine home: there is nothing to seed")
	}
	return home.Credentials
}

// NoLegacyHistoryReason is the named engine's declaration that its legacy
// per-engine session scraper was RETIRED (its backend's History() is nil and
// canonical capture is the only transcript source); "" for an engine that
// keeps a legacy leg, and for an unregistered name.
func NoLegacyHistoryReason(name string) string {
	d, ok := lookup(name)
	if !ok {
		return ""
	}
	return d.NoLegacyHistoryReason
}

// RetiredScraperBackendNames lists every registered engine whose legacy
// scraper was retired.
func RetiredScraperBackendNames() []string {
	return ListWhere(func(name string) bool { return NoLegacyHistoryReason(name) != "" })
}
