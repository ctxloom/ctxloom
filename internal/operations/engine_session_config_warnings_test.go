package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// brokenConfigProject writes a project whose config.yaml is present but
// malformed, and asserts the fixture is observably hostile FROM config.Load's
// own vantage point before the test relies on it: config.Load must succeed
// (the failure is downgraded to warnings, which is the whole premise) and must
// come back carrying warnings. A fixture that quietly fails to break anything
// is green against broken and fixed code alike.
func brokenConfigProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	appDir := filepath.Join(dir, config.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"),
		[]byte("version: 6\nprofiles: [this is not a mapping\n"), 0o644))

	probe, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err, "a broken config is downgraded to warnings, not a load error")
	require.NotEmpty(t, probe.GetWarnings(),
		"the fixture must be observably broken from config.Load's point of view")
	return dir
}

// An ACP session opens against the config discovered from the CLIENT's cwd,
// not the server process's. config.Load downgrades a present-but-broken config
// file to warnings and returns a nil error, so a loader that ignores those
// warnings hands back a config that looks fine and is not — and the session
// opens on empty context while the editor is told the open succeeded.
//
// The contract this pins is the one strictness and the config warning kinds
// both state: every config warning kind is fatal-class in strict mode, and the
// startup choke owners — `ctxloom run`, `ctxloom mcp` and `ctxloom acp` alike
// — abort on the recorded findings. The ACP opener's own strictness window
// (OpenEngineSession) can only refuse a session over findings something
// actually recorded.
func TestLoadConfigForDir_BrokenConfigIsReportedAndRecorded(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})

	dir := brokenConfigProject(t)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)

	cfg, err := loadConfigForDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.GetWarnings(), "the loader must see the broken config it was handed")

	findings := strictness.Since(mark)
	require.NotEmpty(t, findings,
		"a present-but-broken config must be recorded as a fatal finding, or the ACP session gate has nothing to refuse on")
	for _, f := range findings {
		assert.Contains(t, []strictness.Class{strictness.ClassConfig, strictness.ClassMigration}, f.Class,
			"every config warning kind is fatal-class")
		assert.NotEmpty(t, f.FixIt, "a finding without a fix-it tells the user nothing actionable")
	}
	assert.NotEmpty(t, sink.String(), "the degradation must also reach the diagnostic channel")
}

// The control: an intact project records nothing, so opening an ACP session
// against a healthy config is never refused by this path.
func TestLoadConfigForDir_IntactConfigRecordsNothing(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})

	dir := t.TempDir()
	appDir := filepath.Join(dir, config.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)

	_, err := loadConfigForDir(dir)
	require.NoError(t, err)
	assert.Empty(t, strictness.Since(mark), "an absent/intact config is not a finding")
}

// Every declared config warning kind must map to a fatal class and carry a
// fix-it. This is the invariant the kinds' own doc asserts; without it, adding
// a kind can silently create a degradation that records nothing.
func TestConfigWarningKinds_AllFatalClassWithAFixIt(t *testing.T) {
	kinds := []config.WarningKind{
		config.WarnKindRead,
		config.WarnKindParse,
		config.WarnKindValidate,
		config.WarnKindUnknownKey,
		config.WarnKindMigrationLossy,
	}
	for _, k := range kinds {
		t.Run(string(k), func(t *testing.T) {
			assert.Contains(t, []strictness.Class{strictness.ClassConfig, strictness.ClassMigration},
				k.StrictnessClass())
			assert.NotEmpty(t, k.FixIt())
		})
	}
}
