package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// canonicalizeUserRef accepts a convenience short-form reference
// ("<remote-alias>/<path>[#<sel>/<item>]") as USER INPUT and expands it to a
// canonical "<url>@bundles/<path>[#<sel>/<item>]" ref via the registry. Anything
// already scheme-qualified (a canonical URL or ctxloom:local) — or an
// unresolvable alias, or a bare unprefixed name — is returned unchanged for the
// downstream parser to accept or reject. The short form is a CLI convenience
// only: this resolved canonical ref is what flows on to the lockfile and the
// on-disk install path, so nothing short is ever stored. Delegates to the shared
// short-name choke (remote.CanonicalizeShortRef) so the widened grammar —
// selector preservation, local-only bare names — lives in one place.
func canonicalizeUserRef(reference string, _ remote.ItemType, registry *remote.Registry) string {
	return remote.CanonicalizeShortRef(reference, registryAliasToURL(registry), nil)
}

// CanonicalizeRemoteRef expands a convenience short-form reference
// ("<alias>/<path>") to its canonical "<url>@<kind>/<path>" form via the
// registry, leaving an already-canonical (or unresolvable) ref unchanged. CLI
// commands that look items up by their canonical lockfile key (`bundle
// hold`/`unhold`) use it so they accept the same short input as install.
// Returns the ref unchanged when no registry is available.
func CanonicalizeRemoteRef(cfg *config.Config, ref string, itemType remote.ItemType) string {
	registry, err := getRegistry(cfg, remote.WithRegistryFS(getFS(nil)))
	if err != nil {
		return ref
	}
	return canonicalizeUserRef(ref, itemType, registry)
}
