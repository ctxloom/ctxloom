package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// A Config that reaches a delivery entry point without a bound Trust is a
// CONSTRUCTION bug — a generation built outside config.Open, or a fixture
// that never stated its gate — and it is refused at the entry, by sentinel,
// rather than surfacing later as one withheld item per executable. The
// withhold-on-nil in bundles.Decide stays as the last line; this is the
// first.
func TestDeliveryEntryPoints_RefuseAnUnboundConfig(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})
	ctx := context.Background()

	_, err := ApplyHooks(ctx, ApplyHooksRequest{Cfg: cfg, Backend: "claude-code"})
	require.ErrorIs(t, err, config.ErrTrustUnbound, "ApplyHooks")

	_, err = ResolveHooks(ctx, ResolveHooksRequest{Cfg: cfg})
	require.ErrorIs(t, err, config.ErrTrustUnbound, "ResolveHooks")

	_, err = MaterializeProfile(ctx, cfg, MaterializeProfileRequest{Profiles: []string{"p"}, Target: t.TempDir()})
	require.ErrorIs(t, err, config.ErrTrustUnbound, "MaterializeProfile")

	_, err = AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: []string{"p"}})
	require.ErrorIs(t, err, config.ErrTrustUnbound, "AssembleContext")

	_, err = StartInternalOneShot(ctx, cfg, "primary", "", t.TempDir(), "", 0)
	require.ErrorIs(t, err, config.ErrTrustUnbound, "StartInternalOneShot")
}
