package testenv

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
	"github.com/spf13/afero"
)

// Where a test fixture puts an authored bundle, derived from production rather
// than restated.
//
// Both suites author bundles: the acceptance corpus (which imports this
// package) and the integration tests beside it. They therefore need ONE answer
// to "where does an authored bundle live", and it has to be a DERIVED one.
// When the format root moved, every integration fixture that had spelled the
// old root as a string literal wrote to a directory nothing looked in, and the
// suite failed 44 tests at once — while production, the acceptance corpus and
// every other gate stayed green. A literal path in a fixture is a copy of a
// production decision with nothing binding it to the original; nothing below
// spells ".ctxloom", "content", "bundles", a layout segment, or "bundle.yaml".

// BundlesRoot is the repo-relative directory authored bundles live under: the
// project-relative half (".ctxloom/..."), which is also the path a remote repo
// carries them at.
func BundlesRoot() string {
	return paths.RepoBundlesPrefixFor(bundleLayout)
}

// bundleLayout is the layout every authored fixture is written in. It is a
// symbol rather than a literal so that a layout change is one edit in
// production plus zero here.
const bundleLayout = paths.LayoutV2

// LocalBundlesDir is BundlesRoot's absolute counterpart for a fixture that
// builds an app directory itself (an operations-level test handed an AppPaths
// entry) rather than writing project-relative through TestEnvironment.
func LocalBundlesDir(appDir string) string {
	return paths.LocalBundlesPathFor(appDir, bundleLayout)
}

// TreeBundlePath is the repo-relative directory of an authored bundle: the
// directory holding its envelope and item files.
func TreeBundlePath(name string) string {
	return path.Join(BundlesRoot(), name)
}

// TreeBundleManifestPath is the repo-relative path of a bundle's own envelope.
func TreeBundleManifestPath(name string) string {
	return path.Join(TreeBundlePath(name), bundles.DirectoryFormManifest)
}

// TreeBundleItemPath is the repo-relative path of one file INSIDE a tree
// bundle, given that file's path relative to the bundle's own root (e.g.
// "fragments/guidance.md").
func TreeBundleItemPath(name, rel string) string {
	return path.Join(TreeBundlePath(name), filepath.ToSlash(rel))
}

// BundleTreeFiles renders the bundle doc spells as the tree name, returning
// its files keyed by repo-relative path under BundlesRoot — the shape a seeded
// remote takes. The tree is laid out by bundletree (the production store), so
// the files are what an authored bundle actually holds on disk.
func BundleTreeFiles(name, doc string) (map[string]string, error) {
	fsys := afero.NewMemMapFs()
	const root = "/bundles"
	if _, err := bundletree.WriteDoc(fsys, root, name, doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := afero.Walk(fsys, root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := afero.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[path.Join(BundlesRoot(), filepath.ToSlash(rel))] = string(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect the %q tree: %w", name, err)
	}
	return out, nil
}

// WriteBundleTree authors the bundle doc spells as the tree name under
// projectDir's bundles root, through bundletree — the production store for
// everything it can save — so a fixture states its bundle as one readable
// document and the code under test still reads a real tree.
func WriteBundleTree(projectDir, name, doc string) error {
	_, err := bundletree.WriteDoc(afero.NewOsFs(), filepath.Join(projectDir, filepath.FromSlash(BundlesRoot())), name, doc)
	return err
}
