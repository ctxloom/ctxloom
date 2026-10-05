package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// `profile create --bundle <b>` writes the profile into local bundle b — the
// TARGET — while --include names the bundles the profile pulls in. The profile
// is reachable as b's profile and is not the project bundle's.
func TestProfileCreate_BundleFlagNamesTheTargetLocalBundle(t *testing.T) {
	runCLIFixture(t)
	t.Cleanup(func() { resetFlags(t, rootCmd) })
	require.NoError(t, runCLI(t, "bundle", "create", "tools", "-d", "target bundle").err)

	res := runCLI(t, "profile", "create", "probe", "--include", "demo", "--bundle", "tools")
	require.NoError(t, res.err, res.all())
	resetFlags(t, rootCmd)

	require.NoError(t, runCLI(t, "profile", "show", "tools#profiles/probe").err, "the profile is the target bundle's")
	require.Error(t, runCLI(t, "profile", "show", "probe").err, "and not the project bundle's")
}

// `profile import <file> --bundle <b>` imports into local bundle b.
func TestProfileImport_BundleFlagNamesTheTargetLocalBundle(t *testing.T) {
	dir := runCLIFixture(t)
	t.Cleanup(func() { resetFlags(t, rootCmd) })
	require.NoError(t, runCLI(t, "bundle", "create", "tools", "-d", "target bundle").err)
	src := filepath.Join(dir, "imported.yaml")
	require.NoError(t, os.WriteFile(src, []byte("description: imported\nbundles:\n  - demo\n"), 0o644))

	res := runCLI(t, "profile", "import", src, "--bundle", "tools")
	require.NoError(t, res.err, res.all())
	resetFlags(t, rootCmd)

	require.NoError(t, runCLI(t, "profile", "show", "tools#profiles/imported").err, "the profile is the target bundle's")
	require.Error(t, runCLI(t, "profile", "show", "imported").err, "and not the project bundle's")
}
