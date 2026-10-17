package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// brokenParentProfile is matProfile with a parent that does not exist.
const brokenParentProfile = `description: "materialize fixture"
parents:
  - ctxloom+local:project#profiles/absent-parent
bundles:
  - matctx
hooks:
  unified:
    session_start:
      - type: command
        command: echo team-guardrail
`

// brokenParentProject is matProject whose default profile names a parent
// that does not exist.
func brokenParentProject(t *testing.T) string {
	t.Helper()
	root := matProject(t)
	testsupport.WriteFileString(t, afero.NewOsFs(),
		filepath.Join(bundletree.ProjectProfilesDir(t, filepath.Join(root, ".ctxloom")), "default.yaml"), brokenParentProfile, 0o644)
	return root
}

// exitCode is err's process exit status, 0 when it carries none.
func exitCode(err error) int {
	var st clifmt.ExitStatus
	if errors.As(err, &st) {
		return st.Code
	}
	return 0
}

// TestMaterializeCmd_AFatalFindingWritesNothing is the bug this fixes:
// `materialize --yes` with an unresolvable parent wrote CLAUDE.md (without
// the parent's content), the settings, the commands and the ownership
// records, and only THEN aborted over the finding. In strict mode it must
// refuse before writing anything — no engine file, no record, no lock — and
// exit on the finding, naming it.
func TestMaterializeCmd_AFatalFindingWritesNothing(t *testing.T) {
	root := brokenParentProject(t)
	before := snapshot(t, root)

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLIErr(t, "materialize", "--yes", "--backend", "claude-code")
	})
	require.Error(t, err)
	assert.Equal(t, exitCodeFatalFindings, exitCode(err), "got %v", err)
	assert.Contains(t, stderr, "aborting startup")
	assert.Contains(t, stderr, "absent-parent", "the abort names the finding")
	assert.Equal(t, before, snapshot(t, root), "nothing written: no engine file, no record, no lock")
	assert.NoFileExists(t, filepath.Join(root, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))

	// An explicit target that does not exist yet is not even created.
	target := filepath.Join(t.TempDir(), "out")
	captureStderr(t, func() {
		_, err = runCLIErr(t, "materialize", "--target", target, "--backend", "claude-code")
	})
	assert.Equal(t, exitCodeFatalFindings, exitCode(err), "got %v", err)
	assert.NoDirExists(t, target)
}

// TestMaterializeCmd_DegradedStillWritesAndWarns: --degraded keeps today's
// behaviour — the delivery is written without the missing parent, and the
// finding is streamed as a warning with its fix.
func TestMaterializeCmd_DegradedStillWritesAndWarns(t *testing.T) {
	root := brokenParentProject(t)
	// The warning line is printed once per process: forget an earlier
	// test's, so this one's is seen.
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLIErr(t, "materialize", "--yes", "--degraded", "--backend", "claude-code")
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "warning", "the finding is still streamed")
	assert.Contains(t, stderr, "absent-parent")
	assert.Contains(t, fileText(t, filepath.Join(root, "CLAUDE.md")), "MAT-CONTEXT-MARK")
	assert.Contains(t, fileText(t, filepath.Join(root, ".claude", "settings.json")), "team-guardrail")
}

// TestDepsPullCmd_AFatalFindingRefusesTheRefresh (ruling 2): `deps pull
// --yes` installs the closure, then refreshes the project's engine files
// through the same gate — so a fatal finding refuses the refresh before it
// writes anything, and the pull exits on it. The pull itself still lands.
func TestDepsPullCmd_AFatalFindingRefusesTheRefresh(t *testing.T) {
	testsupport.Isolate(t)
	url := seedBundleRemote(t)
	ref := url + "@bundles/demo"
	project, appDir := seedProjectReferencing(t, ref)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "default.yaml"),
		"description: seeded default profile\n"+
			"parents:\n  - ctxloom+local:project#profiles/absent-parent\n"+
			"bundles:\n  - "+ref+"\n"+
			"hooks:\n  unified:\n    session_start:\n      - type: command\n        command: echo team-guardrail\n", 0o644)
	chdir(t, project)
	before := snapshot(t, project)

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLIErr(t, "deps", "pull", "--yes")
	})
	require.Error(t, err)
	assert.Equal(t, exitCodeFatalFindings, exitCode(err), "got %v", err)
	assert.Contains(t, stderr, "aborting project refresh")
	assert.Contains(t, stderr, "absent-parent")
	assert.Equal(t, before, snapshot(t, project), "the refresh wrote nothing: no engine file, no record, no lock")
	assert.Contains(t, lockedRefs(t, appDir), string(lockKeyOf(t, ref)), "the pull itself still landed")

	// --degraded: the refresh writes as it did before, and the pull succeeds.
	resetApp()
	captureStderr(t, func() {
		_, err = runCLIErr(t, "deps", "pull", "--yes", "--degraded")
	})
	require.NoError(t, err)
	assert.Contains(t, fileText(t, filepath.Join(project, ".claude", "settings.json")), "team-guardrail")
}

// fileText is path's content, failing the test when it cannot be read.
func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
