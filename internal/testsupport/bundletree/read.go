package bundletree

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// ProjectRead writes b as this project's bundle name and returns the read the
// project reader establishes for it.
func ProjectRead(t testing.TB, name string, b *bundles.Bundle) bundles.BundleRead {
	t.Helper()
	fsys := afero.NewMemMapFs()
	WriteBundle(t, fsys, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), name, b)
	return only(t, bundles.NewProjectReader(fsys, []string{"/bundles"}))
}

// RemoteRead writes b as the pinned tree for the canonical ref
// ("<repo url>@<path>") and returns the read the repofs reader establishes
// for it.
func RemoteRead(t testing.TB, ref string, b *bundles.Bundle) bundles.BundleRead {
	t.Helper()
	url, bundlePath, ok := strings.Cut(ref, "@")
	require.True(t, ok, "bundletree: %q is not a canonical <url>@<path> ref", ref)
	const root = "/pinned"
	fsys := afero.NewMemMapFs()
	WriteBundle(t, fsys, root, path.Base(bundlePath), b)
	tree, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return only(t, bundles.NewRepoFSReader(tree, ref, bundles.WithRepoURL(url)))
}

func only(t testing.TB, r bundles.Reader) bundles.BundleRead {
	t.Helper()
	reads, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1, "bundletree: the fixture must read back as exactly one bundle")
	return reads[0]
}
