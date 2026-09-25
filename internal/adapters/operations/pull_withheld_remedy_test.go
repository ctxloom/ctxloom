package operations

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// pullRefusal is what a pull of the staged tree returns once mutate has run
// over its installed bytes: the production tree verifier's refusal, wrapped
// the way the puller wraps a verifier error before it pins anything.
func pullRefusal(t *testing.T, mutate func(t *testing.T, fsys afero.Fs)) error {
	t.Helper()
	ctx := context.Background()
	_, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, treeRelease(t, tree), signer))
	mutate(t, fsys)

	names, err := tree.Files(ctx)
	require.NoError(t, err)
	files := map[string]remote.TreeFile{}
	for _, n := range names {
		data, err := tree.ReadFile(ctx, n)
		require.NoError(t, err)
		files[n] = remote.TreeFile{Data: data}
	}
	_, verr := bundles.TreeVerifier(treeTrustRoot("trent@acme.test", pub))(ctx, files, "bundles/"+string(tree.ID()), "abc", "https://github.com/acme/ctx")
	require.Error(t, verr)
	return fmt.Errorf("refusing to install %s at %s: %w", tree.ID(), "abc", verr)
}

// syncFailureFinding syncs one ref through a puller that refuses it with err,
// runs the startup summary over the result, and returns the single finding it
// raised — its fix line is what the user is told to do.
func syncFailureFinding(t *testing.T, err error) report.Finding {
	t.Helper()
	item := syncItem(context.Background(), &syncMockPuller{err: err}, treeCanonical, remote.ItemTypeBundle, treeBase, true, nil, nil)
	require.Equal(t, "failed", item.Status)

	resetStrictness(t)
	WriteAndRecordSyncSummary(io.Discard, &SyncDependenciesResult{Status: "partial", Errors: 1, Failed: []SyncItem{item}})
	found := strictness.All()
	require.Len(t, found, 1)
	return found[0]
}

// A pull that meets a pin signed in the RETIRED manifest format fails exactly
// as before, but `deps pull` keeps the pin — so retrying it cannot help, and the
// fix line must name the command that moves the pin.
func TestSyncSummary_SupersededManifestFormatPointsAtUpgrade(t *testing.T) {
	err := pullRefusal(t, func(t *testing.T, fsys afero.Fs) {
		rewriteManifestMarker(t, fsys, content.DigestVersionMarker)
	})
	require.ErrorIs(t, err, bundles.ErrTreeBundleWithheld, "what is withheld does not change")
	require.ErrorIs(t, err, content.ErrManifestSuperseded, "the pull refusal carries its cause, typed")

	f := syncFailureFinding(t, err)
	assert.Equal(t, report.KindSync, f.Kind)
	assert.Equal(t, remedyWithheldSuperseded, f.Remedy)
}

// Every other refusal keeps the sync fix line it had: a marker this build does
// not know, and bytes edited after signing.
func TestSyncSummary_OtherWithheldCausesKeepTheSyncRemedy(t *testing.T) {
	cases := map[string]func(t *testing.T, fsys afero.Fs){
		"unknown manifest marker": func(t *testing.T, fsys afero.Fs) {
			rewriteManifestMarker(t, fsys, "# ctxloom-bundle-manifest/99")
		},
		"file edited after signing": editInstalledFragment,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			err := pullRefusal(t, mutate)
			require.ErrorIs(t, err, bundles.ErrTreeBundleWithheld)
			assert.NotErrorIs(t, err, content.ErrManifestSuperseded)

			f := syncFailureFinding(t, err)
			assert.Equal(t, report.KindSync, f.Kind)
			assert.Equal(t, remedySyncFailed, f.Remedy)
		})
	}
}
