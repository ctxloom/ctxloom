package isolation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// hostCompanions puts a real executable for each name in one PATH directory
// and returns it.
func hostCompanions(t *testing.T, names ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script companions")
	}
	dir := t.TempDir()
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho "+name+"\n"), 0o755)) //nolint:gosec // an executable fixture
	}
	t.Setenv("PATH", dir)
	withRealCompanionLookPath(t)
	return dir
}

// TestStageCompanions_StagesOnlyRegisteredAndRegistersThemByName: the agent
// image carries the companions this machine REGISTERED — a present but
// unregistered one is not baked in — and its home config registers exactly
// the staged NAMES, never a path, so the in-image ctxloom resolves them on
// the image's own PATH. A container gets no host ~/.ctxloom; this is how the
// registration reaches it.
func TestStageCompanions_StagesOnlyRegisteredAndRegistersThemByName(t *testing.T) {
	dir := hostCompanions(t, "ltk", "taskloom")
	withRegisteredCompanions(t, "ltk", "reprise", "acme")
	warn := captureWarnings(t)

	ctxDir := t.TempDir()
	require.NoError(t, stageCompanions(ctxDir))

	staged := filepath.Join(ctxDir, "companions")
	got, err := os.ReadFile(filepath.Join(staged, "ltk")) //nolint:gosec // a path this test built
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(dir, "ltk")) //nolint:gosec // a path this test built
	require.NoError(t, err)
	assert.Equal(t, want, got, "the staged ltk is the host's ltk")
	info, err := os.Stat(filepath.Join(staged, "ltk"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o755, info.Mode(), "staged companion is 0755 exactly, umask notwithstanding")
	assert.NoFileExists(t, filepath.Join(staged, "taskloom"), "an unregistered companion is never staged")

	home, err := os.ReadFile(paths.ConfigPath(filepath.Join(ctxDir, imageHomeContextDir, paths.AppDirName))) //nolint:gosec // a path this test built
	require.NoError(t, err)
	cfg, err := config.ParseConfig(home)
	require.NoError(t, err)
	assert.Equal(t, []string{"ltk"}, cfg.GetCompanions(), "the image registers exactly what it staged")
	assert.False(t, strings.Contains(string(home), "/"), "the image's registration is names only: %s", home)

	assert.Contains(t, warn.String(), "reprise", "a registered companion the host lacks is reported")
	assert.NotContains(t, warn.String(), "acme", "a companion the image cannot carry is not the image's to report")
}

// TestStageCompanions_NothingStagedStillMakesTheHomeDir: the Containerfile
// copies the staged home unconditionally, so it must exist even empty.
func TestStageCompanions_NothingStagedStillMakesTheHomeDir(t *testing.T) {
	hostCompanions(t, "ltk")
	captureWarnings(t)

	ctxDir := t.TempDir()
	require.NoError(t, stageCompanions(ctxDir))
	assert.DirExists(t, filepath.Join(ctxDir, imageHomeContextDir))
	assert.NoFileExists(t, filepath.Join(ctxDir, "companions", "ltk"), "nothing is registered, so nothing is staged")
	assert.NoFileExists(t, paths.ConfigPath(filepath.Join(ctxDir, imageHomeContextDir, paths.AppDirName)))
}

// TestCompanionVersionKey_CoversOnlyRegistered: the image key digests exactly
// what a build would stage, so registering a companion changes it.
func TestCompanionVersionKey_CoversOnlyRegistered(t *testing.T) {
	withCompanions(t, map[string]string{"ltk": "v2.0.0"})
	only := companionVersionKey()
	withCompanions(t, map[string]string{"ltk": "v2.0.0", "taskloom": "v1.0.0"})
	both := companionVersionKey()
	withRegisteredCompanions(t, "ltk")
	assert.Equal(t, only, companionVersionKey(), "an installed but unregistered companion is not in the key")
	assert.NotEqual(t, only, both)
}

// TestAgentImage_StagedCompanionsLeadPath: inside the container the staged
// companions live in /usr/local/bin, and the image says so in its own PATH
// rather than trusting the base image's default order — a base (a devcontainer,
// a user Containerfile) can put anything ahead of it.
func TestAgentImage_StagedCompanionsLeadPath(t *testing.T) {
	for label, cf := range map[string]string{
		"agent":   string(composeAgentContainerfile("claude-code")),
		"overlay": string(overlayContainerfile("example/base:latest", "")),
	} {
		copyAt := strings.Index(cf, "COPY companions/ /usr/local/bin/")
		envAt := strings.Index(cf, `ENV PATH="/usr/local/bin:${PATH}"`)
		require.GreaterOrEqual(t, copyAt, 0, "%s stages companions", label)
		assert.Greater(t, envAt, copyAt, "%s: /usr/local/bin leads PATH once the companions are staged", label)
		assert.Contains(t, cf, "COPY --chown=1000:1000 "+imageHomeContextDir+"/ /home/ctxloom/",
			"%s: the staged companions' registration reaches the in-image home", label)
	}
}
