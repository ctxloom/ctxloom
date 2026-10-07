package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// `ctxloom run` never offers to rewrite a config layer the load changed in
// memory: persisting is --write-upgrades' job alone. The fixture gives the
// load something to change (a legacy version key and a profile ref the
// canonicalizer rewrites) and the terminal answers yes to any prompt, so a
// prompt that survived would rewrite the file.
func TestRunLoadConfig_NeverPromptsToRewriteTheConfig(t *testing.T) {
	testsupport.Isolate(t)
	const short = "short-ref"
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	path := paths.ConfigPath(appDir)
	body := fmt.Sprintf("schema_version: %d\nagents:\n  dev:\n    llm: claude-code\n    profiles:\n      - %s\n", config.CurrentConfigVersion, short)
	osfs := afero.NewOsFs()
	testsupport.WriteFileString(t, osfs, path, body, 0o644)

	testApp(t, configload.WithAppDir(appDir), configload.WithProfileRefCanonicalizer(func(_ *config.Config, ref string) string {
		if ref == short {
			return "canonical-ref"
		}
		return ref
	}))
	atTerminal(t, "y\ny\ny\n")

	require.NoError(t, (&runState{}).loadConfig())

	got, err := afero.ReadFile(osfs, path)
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "run must not rewrite the config, consent or not")
	_, err = osfs.Stat(path + schemaver.BackupSuffix)
	assert.ErrorIs(t, err, afero.ErrFileNotFound)
}

// A layer this build refuses is never rewritten, with --write-upgrades or
// without it: write-back persists a migration, and a refusal migrated nothing.
func TestRoot_WriteUpgradesNeverRewritesARefusedLayer(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	resetApp()
	t.Cleanup(resetApp)
	// rootCmd is package-level: the switch it set must not leak to later tests.
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })

	osfs := afero.NewOsFs()
	path := paths.ConfigPath(filepath.Join(root, paths.AppDirName))
	scaffolded, err := afero.ReadFile(osfs, path)
	require.NoError(t, err)
	require.Contains(t, string(scaffolded), schemaver.Key+":", "init must stamp the current key")
	unversioned := strings.Replace(string(scaffolded), schemaver.Key+":", "version:", 1)
	testsupport.WriteFileString(t, osfs, path, unversioned, 0o644)

	for _, args := range [][]string{{"config", "show"}, {"config", "show", "--" + schemaver.WriteUpgradesFlag}} {
		resetApp()
		_, _ = execRootCmd(t, args...)
		got, err := afero.ReadFile(osfs, path)
		require.NoError(t, err)
		assert.Equal(t, unversioned, string(got), "%v must not rewrite a refused layer", args)
		assert.NoFileExists(t, path+schemaver.BackupSuffix)
	}
}
