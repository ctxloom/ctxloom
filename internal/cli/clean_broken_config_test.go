package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// brokenConfigProject roots an isolated project holding a cache worth
// reclaiming and the config body given — including bodies no loader can make
// sense of.
func brokenConfigProject(t *testing.T, configBody string) string {
	t.Helper()
	dir := t.TempDir()
	cached := filepath.Join(dir, paths.AppDirName, paths.CacheDir, paths.BundlesDir, "demo")
	require.NoError(t, os.MkdirAll(cached, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cached, "bundle.yaml"), []byte("name: demo\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, paths.AppDirName, paths.ConfigFileName+".yaml"), []byte(configBody), 0o644))

	t.Setenv(projectroot.EnvVar, dir)
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	return dir
}

// TestClean_ReachesTheCacheThroughAnUnreadableConfig pins the reason `clean`
// exists as a command rather than an instruction to rm -rf: the cache is what
// you reach for WHEN THE PROJECT IS BROKEN, so clean must not need the project
// to be loadable in order to run.
//
// A command that first parsed config to decide what to delete would refuse
// exactly when it was needed, and the user would be left deleting paths by
// hand — which is how the wrong tree gets removed.
func TestClean_ReachesTheCacheThroughAnUnreadableConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed yaml", ":::not: [valid: yaml"},
		{"empty", ""},
		{"superseded schema version", "version: 5\n"},
		{"not yaml at all", "\x00\x01\x02 binary garbage\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := brokenConfigProject(t, tc.body)

			var out, errOut bytes.Buffer
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&errOut)
			rootCmd.SetArgs([]string{"clean", "--format", "json"})
			t.Cleanup(func() {
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
				rootCmd.SetArgs(nil)
			})

			require.NoError(t, rootCmd.Execute(),
				"clean must run against an unloadable project (stderr: %s)", errOut.String())

			var res operations.CleanResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &res), "clean must still emit its plan")

			// The EFFECT, not the exit code: a clean that resolved the wrong
			// root would exit 0 having found nothing, which is this project's
			// characteristic silent no-op.
			var found bool
			for _, target := range res.Targets {
				if target.Present && target.Bytes > 0 {
					found = true
					assert.Contains(t, target.Path, dir,
						"clean must resolve THIS project's cache, not a path relative to some other cwd")
				}
			}
			assert.True(t, found, "the cache seeded in this project must be found and accounted for")
		})
	}
}

// TestClean_FindsTheProjectRootFromASubdirectory pins the other half, and it is
// the one a mutation can kill: clean resolves THIS PROJECT's root, not whatever
// directory it happened to be invoked from. Resolving relative to the cwd would
// exit 0 having found nothing — the silent no-op that reads as "already clean"
// and sends the user to delete paths by hand.
func TestClean_FindsTheProjectRootFromASubdirectory(t *testing.T) {
	dir := brokenConfigProject(t, ":::not: [valid: yaml")
	sub := filepath.Join(dir, "sub", "deeper")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	testsupport.ChangeDir(t, sub)

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"clean", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.Execute(), "stderr: %s", errOut.String())

	var res operations.CleanResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.Positive(t, res.Bytes,
		"clean run from a subdirectory must still find the project's cache, not the cwd's")
	for _, target := range res.Targets {
		assert.Contains(t, target.Path, dir, "every target must resolve under the project root")
		assert.NotContains(t, target.Path, filepath.Join("sub", "deeper"),
			"a cwd-relative resolution would name the subdirectory")
	}
}
