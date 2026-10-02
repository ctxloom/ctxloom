package composite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
)

// TestAssemble_CarriesTheSourcesToCarryForward: the sources whose content is
// unknown this time travel with the package, through its encoding, to every
// delivery of it — a run's as much as an install's.
func TestAssemble_CarriesTheSourcesToCarryForward(t *testing.T) {
	cat := corpus(t)
	withheld := []string{"bundle:ctxloom+companion:taskloom"}
	pkg, err := composite.Assemble(context.Background(), cat, selectAlpha(t, cat), compositetest.Trust(), composite.Options{CarryForward: withheld})
	require.NoError(t, err)
	require.Equal(t, withheld, pkg.CarryForward)

	enc, err := composite.Encode(pkg)
	require.NoError(t, err)
	got, err := composite.Decode(enc)
	require.NoError(t, err)
	require.Equal(t, withheld, got.CarryForward)
}
