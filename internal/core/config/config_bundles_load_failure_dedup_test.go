package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// N bundles that fail to load must read as N findings, not one from the reader
// that failed to read each and another from every surface that then asked for
// it by ref. The reader that recorded the failure already reported it, with the
// remedy for its actual cause; a second "failed to load bundle" finding for the
// same bundle doubles the count and buries that remedy.
func TestResolveBundles_UnreadableBundlesReportOnceEach(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t,
		map[string]string{"broken-a": mcpBundleYAML(), "broken-b": mcpBundleYAML()},
		map[string]string{"alpha": "broken-a", "beta": "broken-b"},
	)
	bundlesDir := paths.LocalBundlesPathFor(cfg.appPaths[0], paths.LayoutV2)
	for _, name := range []string{"broken-a", "broken-b"} {
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, name, bundles.DirectoryFormManifest),
			[]byte("version: \"1.0\"\nmcp: [this is not a map\n"), 0o644))
	}

	mark := strictness.Checkpoint()
	cfg.ResolveBundleMCPServers([]string{"alpha", "beta"})
	cfg.ResolveBundleHooks([]string{"alpha", "beta"})

	var bundleFindings []string
	for _, f := range strictness.Since(mark) {
		if f.Kind == report.KindBundle {
			bundleFindings = append(bundleFindings, f.Text)
		}
	}
	assert.Len(t, bundleFindings, 2, "two unreadable bundles are two findings: %q", bundleFindings)
	for _, text := range bundleFindings {
		assert.NotContains(t, text, "failed to load bundle",
			"the ref-load finding repeats one the reader already raised for this bundle")
	}
}
