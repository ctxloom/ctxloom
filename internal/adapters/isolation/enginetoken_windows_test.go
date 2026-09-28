//go:build windows

package isolation

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL: the stored credential and its directory
// grant only the current user, and status shows the ACL verdict, not a mode.
// What the DACL tolerates and refuses is the owneronly seam's own test.
func TestStoreEngineCredential_AppliesAnOwnerOnlyDACL(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)

	fileperm.OwnerOnly(t, filepath.Dir(path))
	fileperm.OwnerOnly(t, path)

	st, err := credentialStatus(claude.EngineName, engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "owner-only", st.Protection)
}
