package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appPathFunc names one member of the appPath family — the exported
// `func(appPath string) string` compositions that hang off a caller-supplied
// project app dir.
type appPathFunc struct {
	name string
	fn   func(string) string
	leaf string // what an empty appPath collapses to
}

// appPathFamily is every member of the family, enumerated so a new one added
// without considering empty input shows up as an omission here rather than as
// a silent write into whatever directory the process happens to be sitting in.
func appPathFamily() []appPathFunc {
	return []appPathFunc{
		{"CachePath", CachePath, "cache"},
		{"ConfigPath", ConfigPath, "config.yaml"},
		{"RemotesPath", RemotesPath, "remotes.yaml"},
		{"ApprovalsPath", ApprovalsPath, "approvals"},
		{"AllowedSignersPath", AllowedSignersPath, "allowed_signers"},
		{"DistrustedSignersPath", DistrustedSignersPath, "distrusted_signers"},
		{"LockPath", LockPath, "lock.yaml"},
		{"ProfilesPath", ProfilesPath, "profiles"},
		{"AgentsPath", AgentsPath, "agents"},
		{"CacheBundlesPath", CacheBundlesPath, filepath.Join("cache", "bundles")},
		{"LocalPath", LocalPath, "content"},
		{"LocalBundlesPath", LocalBundlesPath, filepath.Join("content", "bundles")},
		{"ReposCachePath", ReposCachePath, filepath.Join("cache", "repos")},
		{"TrustObjectsPath", TrustObjectsPath, filepath.Join("state", "trust", "objects")},
		{"LegacyTrustObjectsPath", LegacyTrustObjectsPath, filepath.Join("cache", "trust", "objects")},
	}
}

// TestAppPathFamily_EmptyAppPathIsRelative is a CHARACTERIZATION pin: it
// records, rather than corrects, what this family does with an empty appPath.
//
// Every member is a bare filepath.Join over its argument, so an empty appPath
// yields a RELATIVE path — `ConfigPath("")` is "config.yaml", not an error and
// not an absolute path. Read against a process working directory it therefore
// names a file in whatever directory ctxloom was launched from.
//
// This behaviour is currently harmless only because every production caller
// forecloses it: config.findAppDir and cli.resolveAppDir cannot return an empty
// string, and the three callers that accept one substitute AppDirName
// themselves (operations.getBaseDir, remote.NewLockfileManager,
// cli.projectConfigPath). That defence is duplicated at each of those sites and
// owned by none of them. The pin exists so that if this package ever takes the
// default over — or starts erroring — the change is a deliberate, visible one
// rather than a silent shift in where 51 call sites write.
func TestAppPathFamily_EmptyAppPathIsRelative(t *testing.T) {
	for _, f := range appPathFamily() {
		t.Run(f.name, func(t *testing.T) {
			got := f.fn("")
			assert.Equal(t, f.leaf, got,
				"%s(\"\") collapses to a bare relative leaf; it neither errors nor anchors at AppDirName", f.name)
			assert.False(t, filepath.IsAbs(got),
				"%s(\"\") is resolved against the process working directory, not a project root", f.name)
		})
	}
}

// TestAppPathFamily_NonEmptyAppPathAnchors is the other half of the
// characterization: with a real app dir every member composes underneath it.
// Asserting this alongside the empty case is what makes the empty case legible
// as a degenerate input rather than as the family's normal shape.
func TestAppPathFamily_NonEmptyAppPathAnchors(t *testing.T) {
	const appDir = "/project/.ctxloom"
	for _, f := range appPathFamily() {
		t.Run(f.name, func(t *testing.T) {
			got := f.fn(appDir)
			require.True(t, filepath.IsAbs(got), "%s should anchor under an absolute app dir", f.name)
			assert.Equal(t, filepath.Join(appDir, f.leaf), got,
				"%s must compose its leaf directly under the supplied app dir", f.name)
		})
	}
}
