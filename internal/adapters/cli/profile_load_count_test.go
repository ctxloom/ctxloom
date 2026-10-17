package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// profileReadCounter is the OS filesystem with every open of one profile
// item's file counted. A profile reaches the loader only as a bundle item
// decoded from that file, so the count is the number of times the profile
// was LOADED — whatever the loader then does with it in memory — and it
// keeps meaning that whether or not the load has anything to warn about.
type profileReadCounter struct {
	afero.Fs
	suffix string
	opens  atomic.Int64
}

func newProfileReadCounter(profile string) *profileReadCounter {
	return &profileReadCounter{
		Fs:     afero.NewOsFs(),
		suffix: string(filepath.Separator) + filepath.Join(paths.ProjectBundleName, paths.ProfilesDir, profile+".yaml"),
	}
}

func (c *profileReadCounter) note(name string) {
	if strings.HasSuffix(filepath.Clean(name), c.suffix) {
		c.opens.Add(1)
	}
}

func (c *profileReadCounter) Open(name string) (afero.File, error) {
	c.note(name)
	return c.Fs.Open(name)
}

func (c *profileReadCounter) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	c.note(name)
	return c.Fs.OpenFile(name, flag, perm)
}

// directCmd readies cmd for its RunE to be called directly. The root's
// PersistentPreRun composes the process's App afresh, so a command driven
// through rootCmd.Execute never sees the counting filesystem testApp
// installed; calling RunE is the path App documents for a fixture.
func directCmd(t *testing.T, cmd *cobra.Command) *cobra.Command {
	t.Helper()
	var out bytes.Buffer
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	return cmd
}

// TestMaterialize_LoadsTheDefaultProfileOnce pins that one `ctxloom
// materialize` for one engine — the at-rest path, which assembles that
// engine's package — reads the project's default profile ONCE. Every
// consumer in the command resolves the profile from the generation's catalog
// in memory; a consumer that re-reads the bundles instead shows up here as a
// second open.
func TestMaterialize_LoadsTheDefaultProfileOnce(t *testing.T) {
	testsupport.ProjectDir(t)

	_, err := runCLIErr(t, "manage", "install", "--print=false", "--no-pull", "--engine", "claude-code")
	require.NoError(t, err, "scaffold the project the materialize delivers into")

	counter := newProfileReadCounter(operations.SeedProfileName)
	testApp(t, configload.WithRoot(safefs.NewMem(counter)))

	saved := matFlags
	t.Cleanup(func() { matFlags = saved })
	// Every kind but context: the scaffolded seed profile assembles none.
	matFlags = materializeFlags{yes: true, surfaces: []string{"mcp", "settings", "hooks", "commands", "skills"}}
	require.NoError(t, runMaterialize(directCmd(t, materializeCmd), nil))
	assert.Equal(t, int64(1), counter.opens.Load(), "one command, one load of the default profile")
}

// TestManageInstall_LoadsTheDefaultProfileAtMostOnce pins the same property
// for the scaffolding command: it writes the default profile and reloads the
// configuration once, and nothing in it may read that profile back more than
// once.
func TestManageInstall_LoadsTheDefaultProfileAtMostOnce(t *testing.T) {
	testsupport.ProjectDir(t)

	counter := newProfileReadCounter(operations.SeedProfileName)
	testApp(t, configload.WithRoot(safefs.NewMem(counter)))

	prevPrint, prevNoPull := manageInstallPrint, manageInstallNoPull
	// --no-pull: the dependency pull install ends with is `deps pull`'s, whose
	// own loads are not this command's scaffolding (and would reach the
	// network for the seed profile's parent).
	manageInstallPrint, manageInstallNoPull = false, true
	t.Cleanup(func() { manageInstallPrint, manageInstallNoPull = prevPrint, prevNoPull })
	require.NoError(t, runManageInstall(directCmd(t, manageInstallCmd), nil))
	assert.LessOrEqual(t, counter.opens.Load(), int64(1), "one command, at most one load of the default profile")
}
