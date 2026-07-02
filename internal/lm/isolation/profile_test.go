package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerProfileFor_Claude pins the claude-code profile: the generic agent
// image (compat with the container-build-claude tagging), a local-build recipe,
// and the .claude overlay set.
func TestContainerProfileFor_Claude(t *testing.T) {
	p := containerProfileFor("claude-code")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.NotEmpty(t, p.containerfile, "claude is locally buildable (embedded Containerfile)")
	assert.Contains(t, p.overlayDirs, ".claude")
	assert.NotContains(t, p.overlayDirs, ".kiro")
}

// TestContainerProfileFor_Kiro pins the kiro profile: its OWN image tag (a kiro
// run in a claude image would fail at engine spawn, worse than degrading), a
// local-build recipe, and the .kiro overlay set.
func TestContainerProfileFor_Kiro(t *testing.T) {
	p := containerProfileFor("kiro")
	assert.Equal(t, "ctxloom-agent-kiro:latest", p.image)
	assert.NotEmpty(t, p.containerfile, "kiro is locally buildable (embedded Containerfile)")
	assert.Contains(t, p.overlayDirs, ".kiro")
	assert.NotContains(t, p.overlayDirs, ".claude", "kiro writes no .claude config")
}

// TestContainerProfileFor_UnknownIsDefault: engines without a profile keep the
// pre-profile semantics — the generic image, claude auth, NO local build (run if
// the image is present, degrade if not).
func TestContainerProfileFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "codex", "mock"} {
		p := containerProfileFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Empty(t, p.containerfile, "backend %q has no local-build recipe", name)
		assert.Contains(t, p.overlayDirs, ".claude", "backend %q", name)
	}
}

// TestNewContainerFor_UsesProfileImage / TestNewContainer_ExplicitImageWins pin
// the two constructors: For resolves the profile's image; the legacy explicit
// image overrides it over the default profile.
func TestNewContainerFor_UsesProfileImage(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "kiro")
	assert.Equal(t, "ctxloom-agent-kiro:latest", c.image)

	explicit := NewContainer(fakeRuntime{name: "docker", available: true}, "custom:tag")
	assert.Equal(t, "custom:tag", explicit.image)
	assert.Empty(t, explicit.profile.containerfile, "an explicit image is never locally built")
}

// TestResolveKiroContainerAuth pins kiro's container auth: KIRO_API_KEY env
// passthrough (headless mode) or nothing — no credential mount until the
// ~/.kiro layout is verified live.
func TestResolveKiroContainerAuth(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "")
	_, ok := resolveKiroContainerAuth("/root")
	assert.False(t, ok, "no KIRO_API_KEY → degrade (never launch an engine stuck at browser login)")

	t.Setenv("KIRO_API_KEY", "kiro-test")
	auth, ok := resolveKiroContainerAuth("/root")
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.env, "KIRO_API_KEY=kiro-test")
	assert.Empty(t, auth.mounts, "kiro env passthrough mounts nothing")
}
