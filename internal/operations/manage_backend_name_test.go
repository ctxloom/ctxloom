package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoveHooks_UnknownBackendIsRejected pins that `ctxloom manage hooks
// uninstall --backend <typo>` reported Status "removed" listing the typo'd name
// while removing nothing. manageBackendNames passed the name straight through
// unvalidated, and every layer below then treated the unknown backend as a
// permitted no-op: RemoveSettings returns nil when there is no settings writer,
// BuildSurfaces returns an EmptySurfaceSet whose SupportedApproaches is nil, so
// Select skips the kind. Zero errors, so the name was appended to `removed`.
//
// The user's actual harness is still installed and they have been told it is
// gone — the worst shape of this defect, because it reads as confirmation.
func TestRemoveHooks_UnknownBackendIsRejected(t *testing.T) {
	res, err := RemoveHooks(context.Background(), nil, RemoveHooksRequest{
		Backend: "claude-cod", // one keystroke short of claude-code
		FS:      afero.NewMemMapFs(),
		WorkDir: "/proj",
	})
	require.Error(t, err, "an unknown --backend must fail, not report a removal that did not happen")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "claude-cod")
	assert.Contains(t, err.Error(), "claude-code", "the error must name what IS supported")
}

// A known backend, and the all/empty filters, still work.
func TestRemoveHooks_KnownBackendStillRuns(t *testing.T) {
	for _, backend := range []string{"", "all", "claude-code"} {
		res, err := RemoveHooks(context.Background(), nil, RemoveHooksRequest{
			Backend: backend,
			FS:      afero.NewMemMapFs(),
			WorkDir: "/proj",
		})
		require.NoError(t, err, "backend filter %q", backend)
		require.NotNil(t, res)
		assert.Equal(t, "removed", res.Status)
	}
}
