package backends

import (
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
