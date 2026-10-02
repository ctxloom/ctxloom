package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func hostEnvSpec(t *testing.T, axes launch.Axes, eng engine.Engine, home, project string, h agents.HostEnv) Spec {
	t.Helper()
	s, err := NewSpec(axes, eng).Project(project).
		Session(harpA, sessionDir(home, harpA), SessionState{Harp: harpA}).
		Image(ImageConfig{Image: "img"}).Home(agents.HomeModeHost).HostEnv(h).Build()
	require.NoError(t, err)
	return s
}

// The host environment carries the binding's curated env, keeping the
// engine's own home var (on the real home claude reads the human's config
// dir from it); a container has no inherited env to narrow, so its
// Placement inherits all — what is in it is what it was given.
func TestEnvironment_OnlyTheHostCuratesTheEngineEnv(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	withFakeContainerRuntime(t, containerRuntime)
	project := t.TempDir()
	eng := claudeEngine(t)
	h := agents.HostEnv{Curated: true, Passthrough: []string{"GITHUB_TOKEN"}}

	onHost := prepared(t, hostEnvSpec(t, hostAxes, eng, home, project, h)).Placement()
	strictness.Reset()
	inContainer := prepared(t, hostEnvSpec(t, containerAxes, eng, home, project, h)).Placement()

	assert.Equal(t, agents.HostEnv{Curated: true, Passthrough: []string{"GITHUB_TOKEN", claude.ConfigDirEnv}}, onHost.HostEnv)
	assert.Equal(t, agents.HostEnv{}, inContainer.HostEnv)
}

// Undeclared stays undeclared on the host: nothing is narrowed, and the
// home var is not added to a passthrough that does not apply.
func TestEnvironment_AnUndeclaredHostEnvInheritsAll(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	pl := prepared(t, hostEnvSpec(t, hostAxes, claudeEngine(t), home, t.TempDir(), agents.HostEnv{})).Placement()
	assert.Equal(t, agents.HostEnv{}, pl.HostEnv)
}
