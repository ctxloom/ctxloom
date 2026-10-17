package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// installParentProject is a project shaped like the one `manage install`
// scaffolds — a local default profile inheriting a REMOTE bundle profile as
// its parent (resources/profiles/default.yaml) — with the parent pointed at
// a local fixture remote instead of github. Returns the project, its
// .ctxloom, the parent's bundle ref and the bare repository behind it.
func installParentProject(t *testing.T) (project, appDir, bundleRef, bare string) {
	t.Helper()
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	bundleRef = url + "@bundles/demo"
	project, appDir = seedProjectReferencing(t, bundleRef)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "default.yaml"),
		"description: seeded default profile\nparents:\n  - "+bundleRef+"#profiles/developer\n", 0o644)
	chdir(t, project)
	return project, appDir, bundleRef, strings.TrimPrefix(url, "file://")
}

// TestManageInstall_PullsTheParentItScaffolds (ruling 4): install leaves the
// default profile's remote parent INSTALLED — pinned, and its bytes on disk —
// so the user's next command works: a STRICT `materialize --yes` delivers
// the parent's content rather than refusing over a parent never pulled.
func TestManageInstall_PullsTheParentItScaffolds(t *testing.T) {
	project, appDir, bundleRef, _ := installParentProject(t)

	var out string
	captureStdout(t, func() {
		var err error
		out, err = runCLIErr(t, "manage", "install", "--print=false", "--no-pull=false", "--format", "text")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "Pulled 1 dependencies", "the text report says what the pull fetched")
	assert.Contains(t, out, operations.MsgNewPinsHeader, "and discloses the pin it created")

	// A rerun fetches nothing new and says nothing about a pull.
	resetApp()
	captureStdout(t, func() {
		var err error
		out, err = runCLIErr(t, "manage", "install", "--print=false", "--no-pull=false", "--format", "text")
		require.NoError(t, err)
	})
	assert.NotContains(t, out, "Pulled ", "nothing was newly fetched")
	assert.NotContains(t, out, operations.MsgNewPinsHeader, "nothing was newly pinned")
	assert.Contains(t, lockedRefs(t, appDir), string(lockKeyOf(t, bundleRef)), "install pins the parent's bundle")
	assert.True(t, cachedBundleBytes(t, appDir), "install fetches the parent's bytes")

	resetApp()
	var err error
	captureStderr(t, func() {
		_, err = runCLIErr(t, "materialize", "--yes", "--backend", "claude-code")
	})
	require.NoError(t, err, "the next command works in strict mode")
	assert.Contains(t, fileText(t, filepath.Join(project, "CLAUDE.md")), pullFixtureMarker, "the parent's content is delivered")
}

// TestManageInstall_AFailedPullWarnsAndKeepsTheInstall: offline (the
// remote unreachable) install still succeeds — the scaffold is kept, as
// root init keeps it — and says what failed, that the dependencies are NOT
// installed, and that `ctxloom deps pull` retries.
func TestManageInstall_AFailedPullWarnsAndKeepsTheInstall(t *testing.T) {
	_, appDir, bundleRef, bare := installParentProject(t)
	require.NoError(t, os.Rename(bare, bare+".unreachable"))

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { _, err = runCLIErr(t, "manage", "install", "--print=false") })
	})
	require.NoError(t, err, "a failed pull does not fail the install")
	assert.FileExists(t, filepath.Join(appDir, "config.yaml"))
	assert.NotContains(t, lockedRefs(t, appDir), string(lockKeyOf(t, bundleRef)))
	assert.Contains(t, stderr, "dependency pull did not complete", "it says what failed")
	assert.Contains(t, stderr, "NOT installed")
	assert.Contains(t, stderr, "ctxloom deps pull", "and how to retry")
}

// TestManageInstall_NoPullSkipsThePullAndSaysSo: --no-pull fetches and pins
// nothing, and says so — in the payload, and in the text report, which names
// the command that installs what it did not.
func TestManageInstall_NoPullSkipsThePullAndSaysSo(t *testing.T) {
	_, appDir, bundleRef, _ := installParentProject(t)
	payload := runCLIJSON(t, "manage", "install", "--print=false", "--no-pull")
	assert.Equal(t, map[string]any{"status": "skipped", "installed": float64(0)}, payload["pull"])
	assert.NotContains(t, lockedRefs(t, appDir), string(lockKeyOf(t, bundleRef)))
	assert.False(t, cachedBundleBytes(t, appDir))

	var text strings.Builder
	installPullState{Status: "skipped"}.render(&text)
	assert.Contains(t, text.String(), "--no-pull")
	assert.Contains(t, text.String(), "ctxloom deps pull")
}

// TestManageInstall_AFailedPullIsInThePayload: a failed pull's JSON says so —
// status failed, with the error — rather than reading as a pull that ran.
func TestManageInstall_AFailedPullIsInThePayload(t *testing.T) {
	_, _, _, bare := installParentProject(t)
	require.NoError(t, os.Rename(bare, bare+".unreachable"))

	var payload map[string]any
	captureStderr(t, func() { payload = runCLIJSON(t, "manage", "install", "--print=false", "--no-pull=false") })
	pull, ok := payload["pull"].(map[string]any)
	require.True(t, ok, "payload: %v", payload)
	assert.Equal(t, "failed", pull["status"])
	assert.NotEmpty(t, pull["error"])
}
