package operations

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// registryAliasToURL adapts a remotes registry to the alias→URL resolver the
// short-name canonicalizer (remote.CanonicalizeShortRef) needs. Returns nil when
// no registry is available, which disables remote resolution (refs stay as
// authored — fault tolerant).
//
// This is only ever invoked for a ref already shaped "<alias>/<path>" with no
// local file at that name (remote.CanonicalizeShortRef's own gate) — so a
// failure here is never "an ordinary local bundle name", it is specifically
// "this alias is not one the registry knows". That failure used to
// return "" (leave the ref as authored) with NO signal, at all three call
// sites — including the canonicalize-ON-STORE sites (CreateProfile/
// UpdateProfile/SetAgent), where a typo'd or unregistered alias was then
// persisted into profile.yaml/agents verbatim and only surfaced much later, at
// load, in a different process. The on-disk format is unchanged (still "" ->
// caller keeps the ref as authored, fault tolerant) — only the silence is
// fixed.
func registryAliasToURL(registry *remote.Registry) func(string) string {
	if registry == nil {
		return nil
	}
	return func(alias string) string {
		rem, err := registry.Get(alias)
		if err != nil {
			clidiag.Warn("ctxloom", "%q looks like a remote alias but is not a registered remote: %v (keeping the ref as authored)", alias, err)
			return ""
		}
		if rem == nil || rem.URL == "" {
			clidiag.Warn("ctxloom", "%q resolved to a remote with no URL configured (keeping the ref as authored)", alias)
			return ""
		}
		return rem.URL
	}
}

// aliasToURLResolver builds the alias→URL resolver from the project's remotes
// registry, or nil when no registry is available. It is the bridge the
// canonicalize-on-store sites (CreateProfile/UpdateProfile/SetAgent) use to
// persist canonical identity rather than a machine-local alias.
func aliasToURLResolver(cfg *config.Config) func(string) string {
	registry, err := getRegistry(cfg, remote.WithRegistryFS(getFS(nil)))
	if err != nil {
		return nil
	}
	return registryAliasToURL(registry)
}

// canonicalizeBundleRefs canonicalizes every per-remote short bundle ref
// ("<alias>/<bundle>[#<sel>]") in refs to canonical URL form. Bare names
// (decision A: no "<alias>/" prefix) and already-canonical refs pass through
// unchanged. Returns the input slice unchanged when there is nothing to resolve.
func canonicalizeBundleRefs(refs []string, aliasToURL func(string) string) []string {
	return mapRefs(refs, func(ref string) string {
		return remote.CanonicalizeShortRef(ref, aliasToURL, nil)
	})
}

// canonicalizeProfileRefs canonicalizes every per-remote short bundle-profile ref
// ("<alias>/<bundle>#profiles/<name>") in refs to canonical URL form. A ref
// without a "#profiles/" selector is a local profile name and is left unchanged
// (a selector-less "team/dev" stays the local subdir profile it names). Used for
// profile parents and agent profile lists.
func canonicalizeProfileRefs(refs []string, aliasToURL func(string) string) []string {
	return mapRefs(refs, func(ref string) string {
		return remote.CanonicalizeProfileShortRef(ref, aliasToURL)
	})
}

// mapRefs applies fn to each ref, returning the input slice unchanged when empty
// (nil in, nil out — no allocation for the common no-refs case).
func mapRefs(refs []string, fn func(string) string) []string {
	if len(refs) == 0 {
		return refs
	}
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = fn(ref)
	}
	return out
}

// canonicalizeBundleArg canonicalizes a bundle NAME argument as typed at a READ
// site (bundle show/view/export) written in the per-remote short form
// "<alias>/<bundle>[#<item>]", so the seeded loader resolves the pinned remote
// bundle by its canonical key. Local-file-wins (decision E): a bundle whose local
// file (found across dirs on fs) is spelled "<alias>/<bundle>" keeps resolving
// locally. A bare name, an already-canonical ref, or an unknown alias is returned
// unchanged. Nothing is persisted here — this is input resolution only. dirs are
// the caller's bundle search dirs (passed explicitly so the local-file check
// matches the caller's own loader, injected-fs and all).
func canonicalizeBundleArg(cfg *config.Config, name string, dirs []string, fs afero.Fs) string {
	base, selector := remote.SplitItemPath(name)
	var aliasToURL func(string) string
	if registry, err := getRegistry(cfg, remote.WithRegistryFS(getFS(fs))); err == nil {
		aliasToURL = registryAliasToURL(registry)
	}
	localExists := func(b string) bool {
		_, ferr := bundles.NewLoader(dirs, bundles.WithFS(getFS(fs))).Find(b)
		return ferr == nil
	}
	return remote.CanonicalizeShortRef(base, aliasToURL, localExists) + selector
}
