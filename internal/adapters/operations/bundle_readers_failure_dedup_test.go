package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A pinned bundle whose tree will not open is reported here, once, with the
// remedy for its cause. The catalog must then know it was FOUND-but-unreadable
// rather than absent, or every surface that asks for it by ref (MCP, hooks)
// reports it a second time as a missing bundle with the wrong remedy.
func TestPinnedTreeReaders_FailedTreeIsReportedOnceAndKnownToTheCatalog(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	c := config.NewFixture(config.Fixture{AppPaths: []string{treeBase}})
	lock := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{treeCanonical: treeEntry()}}

	mark := strictness.Checkpoint()
	readers := pinnedTreeReaders(c, lock, map[trust.BundleKey]error{})

	var bundleFindings int
	for _, f := range strictness.Since(mark) {
		if f.Kind == report.KindBundle {
			bundleFindings++
		}
	}
	assert.Equal(t, 1, bundleFindings, "one failed tree is one finding")

	cat := bundles.Resolve(context.Background(), strictness.Sink("ctxloom"), readers...)
	_, err := cat.Read(treeCanonical)
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleUnreadable, "the catalog knows the tree was found and already reported")
	assert.ErrorIs(t, err, ErrTreeNotInstalled, "and carries the cause the report named")
}
