package isolation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// CONTAINER + LOGIN on macOS: claude's REAL login declaration names the
// Keychain, no directory: the host shares it in place, and a container is
// refused as for any shared store rather than started logged out.
func TestCredentials_ContainerLoginRefusesTheKeychain(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, claudeCredentials(t, engine.AuthLogin))

	_, _, err := relocateOn(t, s, t.TempDir(), hostRelocator{})
	require.NoError(t, err, "the host shares the Keychain in place")

	_, _, err = relocateOn(t, s, t.TempDir(), containerOf)
	require.ErrorIs(t, err, engine.ErrHostOnlyStore)
	require.ErrorIs(t, err, engine.ErrNoCredential)
}
