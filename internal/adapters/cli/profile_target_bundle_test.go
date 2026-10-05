package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// resetProfileWriteFlags clears create's and import's flag variables: they are
// process globals pflag never resets between Execute() calls, so a --bundle
// left over from one case would silently retarget the next.
func resetProfileWriteFlags() {
	profileCreateIncludes, profileCreateParents, profileCreateTarget = nil, nil, ""
	profileCreateDescription, profileCreateLLM = "", ""
	profileImportTarget, profileImportForce = "", false
}

// `profile create --bundle <b>` writes the profile into local bundle b — the
// TARGET — while --include names the bundles the profile pulls in. The profile
// is reachable as b's profile and is not the project bundle's.
func TestProfileCreate_BundleFlagNamesTheTargetLocalBundle(t *testing.T) {
	runCLIFixture(t)
	t.Cleanup(resetProfileWriteFlags)
	require.NoError(t, runCLI(t, "bundle", "create", "tools", "-d", "target bundle").err)

	res := runCLI(t, "profile", "create", "probe", "--include", "demo", "--bundle", "tools")
	require.NoError(t, res.err, res.all())
	resetProfileWriteFlags()

	require.NoError(t, runCLI(t, "profile", "show", "tools#profiles/probe").err, "the profile is the target bundle's")
	require.Error(t, runCLI(t, "profile", "show", "probe").err, "and not the project bundle's")
}

// `profile import <file> --bundle <b>` imports into local bundle b.
func TestProfileImport_BundleFlagNamesTheTargetLocalBundle(t *testing.T) {
	dir := runCLIFixture(t)
	t.Cleanup(resetProfileWriteFlags)
	require.NoError(t, runCLI(t, "bundle", "create", "tools", "-d", "target bundle").err)
	src := filepath.Join(dir, "imported.yaml")
	require.NoError(t, os.WriteFile(src, []byte("description: imported\nbundles:\n  - demo\n"), 0o644))

	res := runCLI(t, "profile", "import", src, "--bundle", "tools")
	require.NoError(t, res.err, res.all())
	resetProfileWriteFlags()

	require.NoError(t, runCLI(t, "profile", "show", "tools#profiles/imported").err, "the profile is the target bundle's")
	require.Error(t, runCLI(t, "profile", "show", "imported").err, "and not the project bundle's")
}
