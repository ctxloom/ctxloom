package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// Verifies the ref form the code-review profiles use: a CANONICAL
// `<url>@bundles/<name>#fragments/<frag>` cherry-pick against a canonical-keyed
// seeded remote bundle (the same seed mechanism real projects use via the
// lockfile — see config.loadRemoteBundleSeed). It must load exactly the
// cherry-picked fragments — not the whole bundle.
func TestCodeReviewProfile_CanonicalCherryPickResolves(t *testing.T) {
	const canonical = "https://github.com/ctxloom/ctxloom-default@bundles/code-review-aspects"
	const identity = "ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/code-review-aspects"
	seed := map[string]*bundles.Bundle{
		canonical: {
			Version: "1.0.0",
			Fragments: map[string]bundles.BundleFragment{
				"reviewer-base": {Content: "REVIEWER-BASE"},
				"security":      {Content: "SECURITY-LENS"},
				"performance":   {Content: "PERF-LENS"},
			},
		},
	}
	loader := seedLoader(t, seed)

	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), t.TempDir()+"/.ctxloom", map[string]config.Profile{
		"code-review/security": {
			Bundles: []string{
				canonical + "#fragments/reviewer-base",
				canonical + "#fragments/security",
			},
		},
	}, config.Fixture{})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "code-review/security",
		Pipeline: opPipe(cfg, loader),
	})
	require.NoError(t, err)

	assert.ElementsMatch(t,
		[]string{
			identity + "#fragments/reviewer-base",
			identity + "#fragments/security",
			builtinIsolationFragmentRef,
		},
		res.FragmentsLoaded)
	assert.Contains(t, res.Context, "REVIEWER-BASE")
	assert.Contains(t, res.Context, "SECURITY-LENS")
	assert.NotContains(t, res.Context, "PERF-LENS", "cherry-pick must not pull other fragments")
}
