package remote

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/refuri"
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
// The Reference produced carries TWO addresses, and they answer different
// questions. Reference.CanonicalString renders the canonical URI — the
// identity a trust record and a signature preimage key on. Reference.LockKey
// renders the pre-canonical spelling — the FETCH address, which is what a
// lockfile entry and a fetch diagnostic name, because those address where the
// bytes come from rather than what the bundle is.
//
// The two spellings of one git bundle converge because ctxloom+git carries no
// transport choice and NormalizeURL already folds git@ to https, so host and
// repo path are the same URL either way. That convergence is what makes
// migrating an authored ref a spelling change rather than a key migration: a
// decision recorded under one spelling still governs content addressed by the
// other.
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
	case refuri.ClassGit:
		// https, not ssh: ctxloom+git names a repository by host and path and
		// says nothing about transport, and NormalizeURL already folds git@
		// to https, so https is the single spelling a git repository's
		// identity resolves to.
		out.URL = "https://" + p.Host + p.RepoPath
	case refuri.ClassFile:
		out.URL = "file://" + p.RepoPath
	case refuri.ClassLocal:
		out.IsLocal = true
	case refuri.ClassCompanion:
		out.URL = CompanionSource
		out.IsCompanion = true
	case refuri.ClassBuiltin:
		// A builtin bundle is embedded in this binary: it has no repository to
		// fetch from and no source token in this grammar, so Reference cannot
		// represent one. Refused rather than mapped onto ClassLocal, which is
		// the only near-fit and the wrong one — builtin and local are
		// deliberately distinct trust sources so a project-authored bundle can
		// never collide with an embedded one of the same name, and collapsing
		// them here would hand a builtin the local auto-allow under a name the
		// project chose.
		return nil, fmt.Errorf("reference %s addresses a bundle embedded in the binary, "+
			"which is not fetched through a source — builtin content is resolved by name, never by reference", ref)
	default:
		return nil, fmt.Errorf("reference %s: unhandled source class %q", ref, p.Class)
	}
	return out, nil
}
