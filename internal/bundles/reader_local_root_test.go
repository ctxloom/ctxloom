package bundles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A bundles LAYOUT ROOT is not a bundle, whatever it happens to contain.
//
// This is the other edge of the same boundary, and it only became dangerous
// once the walk learned to STOP at a bundle: a manifest sitting at the root
// made the root itself answer as a bundle, the skip then abandoned the entire
// walk, and every real bundle beside it vanished from the listing — replaced by
// one entry named ".", which resolves to nothing. A listing that silently drops
// every bundle is the worst possible reading of a malformed layout.
//
// The assertion is the surviving NAME, not a count: both readings return one
// bundle.
func TestLocalWalk_ManifestAtTheLayoutRootDoesNotHideRealBundles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	root := paths.BundlesLayoutRoot("/bundles", paths.LayoutV1)
	testsupport.WriteFileString(t, fsys,
		filepath.Join(root, paths.BundleManifestName), "version: \"9.9.9\"\n", 0o644)
	testsupport.WriteFileString(t, fsys,
		filepath.Join(root, "kit.yaml"), "version: \"1.0\"\n", 0o644)

	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)

	names := make([]string, 0, len(reads))
	for _, r := range reads {
		names = append(names, r.Bundle.Name)
	}
	require.Equal(t, []string{"kit"}, names,
		"the walked root is the parent bundles sit under; treating it as one buries every bundle beneath it")
}
