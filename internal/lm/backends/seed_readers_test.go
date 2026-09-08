package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedOption presents authored bundle VALUES as what they are — project
// bundles on a filesystem — and hands the config's loader an extra reader over
// them.
//
// There is deliberately no exported way to hand a loader a finished bundle with
// a provenance attached: a constructor that took one would let any caller mint
// local-context content. So a test supplies BYTES and lets the project reader
// establish the facts, which is also what a real project does.
func seedOption(t *testing.T, seed map[string]*bundles.Bundle) config.BundleLoaderOption {
	t.Helper()
	fsys := afero.NewMemMapFs()
	// seedRoot is the SEARCH root the reader is given; it expands that into the
	// format root beneath it and never reads the root itself, so the bytes go
	// there rather than at seedRoot directly.
	const seedRoot = "/seed"
	bundlesRoot := paths.BundlesLayoutRoot(seedRoot, paths.LayoutV2)
	for name, b := range seed {
		data, err := yaml.Marshal(b)
		require.NoError(t, err)
		testsupport.WriteFile(t, fsys, filepath.Join(bundlesRoot, name+".yaml"), data, 0o644)
	}
	return config.WithExtraBundleReaders(bundles.NewProjectReader(fsys, []string{seedRoot}))
}
