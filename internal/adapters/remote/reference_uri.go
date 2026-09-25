package remote

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// parseCanonicalURIReference parses the canonical ctxloom URI family
// (ctxloom+git / ctxloom+file / ctxloom+local / ctxloom+companion) into the
// Reference this package fetches through.
//
// The URI syntax is refuri's, not a second copy of it. That is the whole point
// of the arm: a reference written in the canonical grammar and a reference
// written in the pre-canonical one must reach the SAME Reference, or the same
// bundle would carry two identities depending on how it was spelled — and two
// identities is two trust keys, where a rejection recorded against one does
// not withhold the other.
//
// The URL it carries is the repository's FETCH location, rendered by the one
// reverse renderer (refuri.Parts.FetchURL), so it reads back — through
// Reference.BundleRef — to exactly the identity it was parsed from.
func parseCanonicalURIReference(ref string) (*Reference, error) {
	p, err := refuri.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid reference %s: %w", ref, err)
	}
	// The bundle path is joined under a repository root (BuildFilePath) and,
	// for filesystem-backed sources, under a directory root — so traversal is
	// rejected at parse time, exactly as the pre-canonical grammar does in
	// parseTypePathVersion. refuri resolves dot segments rather than refusing
	// them, because a URI path legitimately carries them; the read path's
	// containment rule is this package's to enforce.
	if err := validateItemPath(p.Bundle); err != nil {
		return nil, fmt.Errorf("invalid reference %s: %w", ref, err)
	}

	// The "#<kind>/<item>" selector addresses an item WITHIN the bundle, not
	// the bundle's identity, and is dropped here for the reason
	// parseTypePathVersion drops it: keeping it would bake the selector into
	// the lockfile key and send the fetcher looking for a file literally named
	// "<bundle>#<kind>/<item>.yaml".
	out := &Reference{
		ItemType:       ItemTypeBundle,
		Path:           p.Bundle,
		ContentVersion: p.Version,
	}
	switch p.Class {
	case refuri.ClassGit, refuri.ClassFile:
		out.URL = p.FetchURL()
	case refuri.ClassLocal:
		out.IsLocal = true
	case refuri.ClassCompanion:
		out.URL = CompanionSource
		out.IsCompanion = true
	default:
		return nil, fmt.Errorf("reference %s: unhandled source class %q", ref, p.Class)
	}
	return out, nil
}
