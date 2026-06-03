package profiles

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/upgrade"
)

// profileUpgrades is the canonical, ordered profile schema upgrade pipeline,
// oldest-first. Append an Upgrader here as the profile schema evolves; a
// Pipeline is itself an Upgrader, so the chain composes and the loader runs the
// whole thing over every profile (see loadFile).
//
// It is parameterized by the short remote name the profile was installed from:
// unlike config's pure-document upgrades, qualifying a bare bundle ref needs the
// profile's remote, which lives in the load name, not the document, so the caller
// resolves it and threads it in. Remote-dependent upgrades self-gate on an empty
// remote (a local project profile has none), so the pipeline is always built —
// that keeps future remote-independent upgrades from being skipped for local
// profiles.
func profileUpgrades(remote string) upgrade.Pipeline {
	return upgrade.Pipeline{
		bundleRefQualifyUpgrade{remote: remote},
	}
}

// bundleRefQualifyUpgrade qualifies bare bundle references with the profile's
// remote. Early profiles (pre-namespacing) listed bundles by bare name
// (`core-practices`); installed/seeded bundles are keyed `<remote>/<bundle>`, so
// a bare ref no longer resolves. This rewrites each bare whole-bundle ref to
// `<remote>/<bundle>` so the existing resolver finds it unchanged.
//
// It is idempotent: an already-qualified ref (its bundle name contains `/`) is
// left untouched, so re-running over a current document changes nothing.
type bundleRefQualifyUpgrade struct {
	remote string
}

// Name identifies the upgrade in logs and the rewrite prompt.
func (bundleRefQualifyUpgrade) Name() string { return "qualify bare bundle refs" }

// Apply qualifies bare entries in the top-level `bundles` sequence, returning
// whether it changed anything.
func (u bundleRefQualifyUpgrade) Apply(root *yaml.Node) (changed bool) {
	if u.remote == "" {
		return false
	}
	seq := upgrade.MapValue(root, "bundles")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range seq.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		if qualified, ok := u.qualify(item.Value); ok {
			item.Value = qualified
			changed = true
		}
	}
	return changed
}

// qualify rewrites a single bundle ref, reporting whether it changed. The remote
// prefixes the bundle-name portion only: a cherry-picked ref like
// `bundle:fragments/x` becomes `<remote>/bundle:fragments/x`. A ref whose bundle
// portion already carries a remote (contains `/`) is returned unchanged.
func (u bundleRefQualifyUpgrade) qualify(ref string) (string, bool) {
	bundle := ref
	rest := ""
	// The bundle name may not contain ':' or '#'; everything from the first such
	// separator onward selects items within the bundle and is preserved verbatim.
	if idx := strings.IndexAny(ref, ":#"); idx != -1 {
		bundle = ref[:idx]
		rest = ref[idx:]
	}
	if bundle == "" || strings.Contains(bundle, "/") {
		return ref, false
	}
	return u.remote + "/" + bundle + rest, true
}
