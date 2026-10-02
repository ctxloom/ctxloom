package mock

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestContextFile_ClaimsASectionAfterTheUsersText: the mock's context file
// is one a human may also write, so the context is a SECTION claimed after
// the file's own text — written by the static writer, never by the approach.
func TestContextFile_ClaimsASectionAfterTheUsersText(t *testing.T) {
	fs := afero.NewMemMapFs()
	a, ok := New().Root().Surfaces()[present.Context].(engine.ContextApproach)
	require.True(t, ok)
	d, err := a.DeliverContext(present.ProjectOnHost("/p"), present.RootProjectRoot, engine.ContextInputs{Text: []byte("rules")}, fs)
	require.NoError(t, err)
	path := filepath.Join("/p", ContextFileName)
	require.Equal(t, path, d.Presented.HostPath)
	require.Equal(t, map[string][]present.Claim{path: {{Pointer: present.AppendedSection, Value: []byte("rules")}}}, d.Claims)
	exists, err := afero.Exists(fs, path)
	require.NoError(t, err)
	require.False(t, exists, "the approach writes nothing")
}
