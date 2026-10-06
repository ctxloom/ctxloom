package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// TestRemoveHooks_UnknownBackendIsRejected pins that `ctxloom manage hooks
// uninstall --backend <typo>` reported Status "removed" listing the typo'd name
// while removing nothing. manageBackendNames passed the name straight through
// unvalidated, and every layer below then treated the unknown backend as a
// permitted no-op: BuildSurfaces returns an EmptySurfaceSet whose SupportedApproaches is nil, so
// Select skips the kind. Zero errors, so the name was appended to `removed`.
//
// The user's actual harness is still installed and they have been told it is
// gone — the worst shape of this defect, because it reads as confirmation.
func TestRemoveHooks_UnknownBackendIsRejected(t *testing.T) {
	res, err := RemoveHooks(context.Background(), engines.Registry(), nil, RemoveHooksRequest{
		Backend: "claude-cod", // one keystroke short of claude-code
		Root:    safefs.NewMem(afero.NewMemMapFs()),
		WorkDir: "/proj",
	})
	require.Error(t, err, "an unknown --backend must fail, not report a removal that did not happen")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "claude-cod")
	assert.Contains(t, err.Error(), "claude-code", "the error must name what IS supported")
}

// A known backend, and the all/empty filters, still work.
func TestRemoveHooks_KnownBackendStillRuns(t *testing.T) {
	for _, backend := range []string{"", "claude-code"} {
		res, err := RemoveHooks(context.Background(), engines.Registry(), nil, RemoveHooksRequest{
			Backend: backend,
			Root:    safefs.NewMem(afero.NewMemMapFs()),
			WorkDir: "/proj",
		})
		require.NoError(t, err, "backend filter %q", backend)
		require.NotNil(t, res)
		assert.Equal(t, "removed", res.Status)
	}
}
