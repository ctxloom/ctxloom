package operations

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// weak-hurt is the release-blocker claim that the unreadable-approvals-store
// fail-closed fix (a24180b/81951ae) left two production surfaces uncovered:
// the LISTING path (`ctxloom review`, PendingReview's per-item walk) and the
// STAMPING path (`ctxloom fragment/command/bundle list`'s trust column,
// TrustStamper — literally the "list-JSON stamping" section of trust.go).
//
// Both tests below build records EXACTLY the way each production surface
// does (no WithStampRecords/PendingReviewRequest.Records injection — the
// nil-default construction from cfg+fs, matching NewTrustStamper's and
// PendingReview's own zero-config call sites in internal/cli), then corrupt
// the on-disk store the same way the acceptance suite's GAP D scenario does
// (a plain file where a directory should be) and assert the payload: a
// listed/stamped item must show DENIED, never silently trusted.
//
// REFUTED on v0.7.0-pre1 @ f314574: both paths already deny-all through the
// same EffectiveTrust cascade contentGate.allow uses (readableRecords is
// satisfied by countersignRecords regardless of which of the three
// production call sites boxes it into EffectiveTrustRequest.Records — see
// trust.go's unconditional `records.(readableRecords)` check). These tests
// are kept as permanent regression coverage: NewTrustStamper's and
// PendingReview's own doc comments still claim "no single file whose
// corruption can deny an entire listing", which is stale relative to this
// behavior and could mislead a future edit into believing these paths need
// no protection.
func TestWeakHurt_TrustStamper_UnreadableStore_ListingPath(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(t.TempDir(), ".ctxloom")
	approvalsDir := filepath.Join(projectDir, "approvals")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))
	wrapped := denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectDir}})
	loader := bundles.NewLoader(nil, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		"demo": {Fragments: map[string]bundles.BundleFragment{"localfrag": {Content: "local body"}}},
	}))

	// NO WithStampRecords injection: production shape, records built from cfg+fs
	// exactly as internal/cli/item_helpers.go's `operations.NewTrustStamper(cfg)`
	// does for `ctxloom fragment/command/bundle list`.
	stamper := NewTrustStamper(cfg, WithStampLoader(loader), WithStampFS(wrapped))

	res := stamper.ForRef("demo#fragments/localfrag")
	t.Logf("ForRef result: trusted=%v source=%s state=%s", res.Trusted(), res.Source, res.State())
	assert.False(t, res.Trusted(), "an unreadable approvals store must deny even a local item on the STAMPING (list-JSON) path")
	assert.Equal(t, trust.SourcePending, res.Source, "must resolve fail-closed pending, never a silently-trusted local exemption")
}

// TestWeakHurt_PendingReview_UnreadableStore_ListingPath is
// TestWeakHurt_TrustStamper_UnreadableStore_ListingPath's sibling for the
// OTHER named surface: PendingReview's own item walk (the `ctxloom review`
// porcelain). A rejected item must still show up (denied, not silently
// dropped from the list) once the store proves unreadable — the walk must
// never quietly report "nothing pending" while hiding a rejection it can no
// longer see.
func TestWeakHurt_PendingReview_UnreadableStore_ListingPath(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(t.TempDir(), ".ctxloom")
	approvalsDir := filepath.Join(projectDir, "approvals")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))
	wrapped := denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectDir}})
	loader := bundles.NewLoader(nil, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		"https://github.com/acme/repo@bundles/tooling": {
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "solid body"}},
		},
	}))

	// NO UserStore/ProjectStore/Root injection: production shape, matching
	// `ctxloom review`'s own zero-config PendingReview(cfg, PendingReviewRequest{}) call.
	res, err := PendingReview(cfg, PendingReviewRequest{Loader: loader, FS: wrapped})
	require.NoError(t, err, "PendingReview itself must never hard-error on a corrupted store — it degrades to an all-pending listing")
	require.NotNil(t, res)
	require.Equal(t, 1, res.Total, "the untrusted remote fragment must still be LISTED as pending, never silently dropped because the store is unreadable")
	require.Len(t, res.Bundles, 1)
	require.Len(t, res.Bundles[0].Items, 1)
	assert.Equal(t, "https://github.com/acme/repo@bundles/tooling#fragments/solid", res.Bundles[0].Items[0].Ref)
}
