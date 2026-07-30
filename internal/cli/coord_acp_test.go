package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestACPCoordinatorSatisfiesEngineSessionCoordinator pins U035-F23's contract:
// acpCoordinator implements operations.EngineSessionCoordinator, with its two
// methods split across files (SessionEnv in coord_acp.go, WatchChildren in
// acp_children.go).
//
// The row's stated risk — that a signature change "surfaces at runtime" — is
// OVERSTATED, and measuring it is what shows why: renaming SessionEnv fails the
// build at acp_cmd.go:147 too, because acpCoord is passed straight to
// OpenEngineSession, so the binding was already enforced at compile time. What
// the added `var _ operations.EngineSessionCoordinator = (*acpCoordinator)(nil)`
// declaration buys is locality and durability: the failure names the TYPE and
// the missing method, and it survives a refactor that stops passing the value
// directly (through a factory, an any, or a struct field).
func TestACPCoordinatorSatisfiesEngineSessionCoordinator(t *testing.T) {
	var v any = newACPCoordinator()
	_, ok := v.(operations.EngineSessionCoordinator)
	require.True(t, ok,
		"acpCoordinator is passed as an operations.EngineSessionCoordinator by acp_cmd.go")
}
