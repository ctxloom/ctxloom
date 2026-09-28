package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRunInit_FreshProject_WritesNoRuntimeSurfaceIntoTheProject pins the
// ruling (2026-09-21) on init's fresh branch: init sets up ctxloom's CONFIG
// — the skeleton, config.yaml, the git-ignore of private state — and writes
// no engine file into the project. The seeded remote's clone is the fresh
// branch's one network step and is stubbed; the dependency pull is
// suppressed the way a user would (--no-pull), so the project's tree after
// init is the scaffold and nothing else.
func TestRunInit_FreshProject_WritesNoRuntimeSurfaceIntoTheProject(t *testing.T) {
	testsupport.Isolate(t)
	project := t.TempDir()
	chdir(t, project)
	resetApp()
	t.Cleanup(resetApp)
	withInitFlags(t, true)

	origClone := cloneConfiguredRemotesFn
	cloneConfiguredRemotesFn = func(*cobra.Command, string) {}
	t.Cleanup(func() { cloneConfiguredRemotesFn = origClone })

	captureStdout(t, func() {
		require.NoError(t, runInit(initTestCmd(), nil))
	})

	assert.FileExists(t, filepath.Join(project, ".ctxloom", "config.yaml"), "init scaffolds the config")
	assert.FileExists(t, filepath.Join(project, ".ctxloom", ".gitignore"), "and the git-ignore of private state")
	assertNoProjectRuntimeSurface(t, project)
	assert.Equal(t, []string{".ctxloom"}, topLevelEntries(t, project),
		"init must add exactly .ctxloom to the project's top level")
}

// TestLaunchDiscovery_LaunchesTheInterviewInItsOwnSessionHome pins how init
// hands the project to its engine for the setup interview: through the one
// launch trunk `ctxloom run` enters (operations.StartRun → launch.Resolve;
// the runner opens the package), so the interview session is minted under
// ctxloom's sessions dir with its own session home, and every static
// surface the engine reads is routed under THAT home — never under the
// project. The engine launch itself is captured rather than spawned; what
// is asserted is the resolved Launch the static writer consumes.
func TestLaunchDiscovery_LaunchesTheInterviewInItsOwnSessionHome(t *testing.T) {
	testsupport.Isolate(t)
	project := t.TempDir()
	appDir := filepath.Join(project, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	cfg := authPingTestConfig(t)
	deps := testLaunchDeps(t, cfg)

	stubPingHosts(t, &stubRunHost{})

	var captured launch.Launch
	origLaunch := launchEngineWithPromptFn
	launchEngineWithPromptFn = func(_ context.Context, _ launch.Deps, _ string, l launch.Launch) error {
		captured = l
		return nil
	}
	t.Cleanup(func() { launchEngineWithPromptFn = origLaunch })

	origSkip := initSkipLaunch
	initSkipLaunch = false
	t.Cleanup(func() { initSkipLaunch = origSkip })

	captureStdout(t, func() {
		require.NoError(t, launchDiscovery(initTestCmd(), "claude-code", appDir, true))
	})

	require.NotEmpty(t, captured.Identity.Harp, "the interview runs as a minted session")

	// The session home lives under ctxloom's sessions dir for THIS harp and
	// nowhere near the project; the project is the session's workspace.
	sessionDir := filepath.Join(deps.Host.CtxloomHome, paths.SessionsDir, captured.Identity.Harp)
	home := captured.Cell.Paths.Paths().SessionHome.Host
	require.NotEmpty(t, home, "the cell must resolve a session home")
	assert.True(t, strings.HasPrefix(home, sessionDir+string(filepath.Separator)),
		"session home %q must be under the session's own dir %q", home, sessionDir)
	assert.False(t, strings.HasPrefix(home, project+string(filepath.Separator)),
		"session home %q must not be inside the project %q", home, project)
	assert.Equal(t, project, captured.Cell.Workspace, "the interview works in the project")

	// The interview is context-free BY DECLARATION (an internal source: the
	// setup prompt is the whole brief), so the one static surface it carries
	// is the session endpoint's own MCP entry — the way the engine reaches
	// ctxloom at all. It is ROUTED under the session home; the Plan is what
	// the runner's static writer consumes, so this is where the file lands.
	require.Len(t, captured.Plan.Static, 1, "the interview carries exactly the session endpoint: %+v", captured.Plan.Static)
	assert.Equal(t, present.MCP, captured.Plan.Static[0].Kind)
	assert.Equal(t, present.RootSessionHome, captured.Plan.Static[0].Root,
		"the engine's MCP entry must route under the session home, never the project")

	// And the target the static writer is handed is the session's own
	// writer over the cell's roots, not the project writer.
	target := captured.Target(nil)
	assert.Equal(t, delivery.SessionWriter(captured.Identity.Harp), target.Writer)

	// Nothing reached the project tree.
	assertNoProjectRuntimeSurface(t, project)
	assert.Equal(t, []string{".ctxloom"}, topLevelEntries(t, project))
}
