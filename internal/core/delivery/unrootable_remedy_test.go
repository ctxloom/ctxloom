package delivery_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

var _ report.Remediable = delivery.Unrootable{}

// The remedy travels beside the refusal, not inside its text, and survives a
// %w wrap to the renderer; the refusal stays errors.Is(ErrUnrootable).
func TestUnrootable_RemedyTravelsBesideTheMessage(t *testing.T) {
	const fix = "select a root the cell provides"
	u := delivery.Unrootable{Kind: present.Context, Approach: "file", Needs: present.RootProjectRoot, Fix: fix}
	require.False(t, strings.Contains(u.Error(), fix), "Error() must not splice the remedy: %q", u.Error())
	wrapped := fmt.Errorf("launch: %w", u)
	require.ErrorIs(t, wrapped, delivery.ErrUnrootable)
	got, ok := clifmt.RemedyOf(wrapped)
	require.True(t, ok)
	require.Equal(t, fix, got)
}
