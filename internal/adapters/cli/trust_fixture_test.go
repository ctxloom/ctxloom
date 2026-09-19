package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// realGated binds the gate a generation of cfg would carry in production —
// composite.NewTrust over the ports configload builds from the project's
// on-disk signer store, approvals stores and lockfile — for a test that
// writes real review records and expects the CLI to read them. A fixture
// nobody bound holds no gate and withholds everything.
func realGated(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	src, err := configload.New(nil, nil)
	require.NoError(t, err)
	root, records, retraction, err := src.TrustPorts(context.Background(), cfg)
	require.NoError(t, err)
	tr, err := composite.NewTrust(root, records, retraction)
	require.NoError(t, err)
	cfg.BindTrustForTesting(tr)
	return cfg
}

// gatedFixture is config.NewFixture with a gate bound that admits by
// locality and withholds what travelled (compositetest.Trust): a fixture
// that reaches a delivery entry point must state its gate, or the entry
// refuses it (config.ErrTrustUnbound).
func gatedFixture(f config.Fixture) *config.Config {
	cfg := config.NewFixture(f)
	cfg.BindTrustForTesting(compositetest.Trust())
	return cfg
}
