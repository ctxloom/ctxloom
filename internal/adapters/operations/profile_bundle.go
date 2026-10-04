package operations

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// localBundleTarget is the LOCAL bundle a profile write lands in: the project
// bundle for a selector-less name, else the bundle a "<bundle>#profiles/<name>"
// ref addresses — which must be local, because a remote bundle's profiles are
// edited at their source.
func localBundleTarget(name string) (string, error) {
	if !strings.Contains(name, "#") {
		return paths.ProjectBundleName, nil
	}
	bundle, _, ok := remote.SplitBundleProfileRef(name)
	if !ok {
		return "", fmt.Errorf("profile %q: a bundle profile is addressed as <bundle>#profiles/<name>", name)
	}
	canon, err := remote.CanonicalBundleRef(bundle)
	if err != nil {
		return "", fmt.Errorf("profile %q: %w", name, err)
	}
	ref, err := remote.ParseReference(canon)
	if err != nil || !ref.IsLocal {
		return "", fmt.Errorf("profile %q is in a remote bundle and read-only; edit it at its source and run 'ctxloom deps pull'", name)
	}
	return ref.Path, nil
}

// bundleProfileName is the name a profile called name is written under in the
// local bundle called bundle: selector-less for the project bundle, the short
// "<bundle>#profiles/<name>" ref for any other.
func bundleProfileName(bundle, name string) string {
	if bundle == "" || bundle == paths.ProjectBundleName {
		return name
	}
	return bundle + refuri.ProfileSelector + name
}

// prepareLocalBundleWrite readies the local bundle a profile write into name
// lands in: the project bundle is created when the project has none yet (it
// is where a project's own profiles live, so its absence is just a project
// with no profiles), and writing into a SIGNED local bundle says that the
// write stales the signature — the bundle is admitted as unsigned until it is
// re-signed. Any other missing local bundle is the writer's error to report.
func prepareLocalBundleWrite(cfg *config.Config, name string) error {
	bundle, err := localBundleTarget(name)
	if err != nil {
		return err
	}
	if bundle == paths.ProjectBundleName && !cfg.LocalBundleExists(bundle) {
		if err := createProjectBundle(cfg); err != nil {
			return err
		}
	}
	if read, err := cfg.BundleLoader().Read(remote.LocalBundleRef(bundle)); err == nil && read.Signature() != bundles.SignatureNone {
		clidiag.Warn("ctxloom", "local bundle %q is signed: writing profile %q into it stales that signature, and the bundle is admitted as unsigned until re-signed (ctxloom bundle sign %s)", bundle, name, bundle)
	}
	return nil
}

// createProjectBundle creates the project's (empty) project bundle through a
// bundle store over the config's filesystem, so the envelope it writes is the
// store's own and lands where the profile loader writes.
func createProjectBundle(cfg *config.Config) error {
	appPaths := cfg.GetAppPaths()
	if len(appPaths) == 0 {
		return fmt.Errorf("no .ctxloom directory configured")
	}
	path := filepath.Join(paths.LocalBundlesPathFor(appPaths[0], paths.LayoutV2), paths.ProjectBundleName, bundles.DirectoryFormManifest)
	if err := bundles.NewFSStore(getFS(cfg.FS()), nil).Save(newCreatedBundle(CreateBundleRequest{}, path)); err != nil {
		return fmt.Errorf("create the %q bundle: %w", paths.ProjectBundleName, err)
	}
	return nil
}

// decodeWritableProfile decodes a profile document a write path is about to
// store, through profiles.Decode — the one decoder every profile item is read
// with, so a document is refused here exactly when it would not load — and
// refuses one that carries nothing at all.
//
// yaml.v3 accepts "", whitespace, a comment-only file and `null` into a
// zero-valued profile with a nil error, so "does it parse?" is not the
// question. The refusal line is profiles.Profile.IsEmptyDocument — the SAME
// line profiles.Loader.Save draws, so the write paths cannot drift apart. A
// labels-only profile is deliberately above that line: it is a normal
// half-authored state, it saves, and the fail-loudly gate says what it will
// not do.
func decodeWritableProfile(data []byte, what string) (*profiles.Profile, error) {
	p, err := profiles.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", what, err)
	}
	if p.IsEmptyDocument() {
		return nil, fmt.Errorf("refusing to write an empty %s: it carries nothing at all — no parents, bundles, fragments, bundle_items, commands, skills, select_tags, hooks, variables, llm, description or tags (an empty, whitespace-only or comment-only document parses cleanly, which is why this has to be checked explicitly)", what)
	}
	return p, nil
}
