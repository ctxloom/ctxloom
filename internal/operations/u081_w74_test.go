package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// ---------------------------------------------------------------------------
// U081-F14 — the bundle read/export/move trio must canonicalize a short bundle
// name against the SAME filesystem they load it from.
// ---------------------------------------------------------------------------

const w74RemoteURL = "https://github.com/example/personal"

// w74ShortNameFS seeds an in-memory project that has BOTH a configured remote
// alias "personal" AND (optionally) a local bundle spelled "personal/tool", so
// decision E — a local file of the same spelling wins over the remote alias —
// is actually exercisable rather than vacuously true.
func w74ShortNameFS(t *testing.T, withLocalFile bool) (afero.Fs, *config.Config, []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(appDir, "remotes.yaml"),
		[]byte("default: personal\nremotes:\n  personal:\n    url: "+w74RemoteURL+"\n    version: v1\n"), 0644))

	bdir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(filepath.Join(bdir, "personal"), 0755))
	if withLocalFile {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "personal", "tool.yaml"),
			[]byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))
	}
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}}), []string{bdir}
}

// TestCanonicalizeBundleArg_ResolvesAgainstTheInjectedFS proves the filesystem
// argument is LOAD-BEARING, which is the whole reason the call sites must hand
// it the filesystem they themselves use. Both of canonicalizeBundleArg's inputs
// — the remote registry and the local-file existence probe — are read through
// that argument, so an operation that resolves the name on one filesystem and
// loads the bundle on another is asking two different questions.
func TestCanonicalizeBundleArg_ResolvesAgainstTheInjectedFS(t *testing.T) {
	fs, cfg, dirs := w74ShortNameFS(t, false)

	// With the project's filesystem: the alias is a known remote and no local
	// file claims the spelling, so the short ref canonicalizes to the remote.
	assert.Equal(t, w74RemoteURL+"@bundles/tool",
		canonicalizeBundleArg(cfg, "personal/tool", dirs, fs),
		"the injected filesystem must be the one the registry is read from")

	// Without it, the very same call cannot see the remotes.yaml that lives
	// only in the injected filesystem, and the ref is left as authored. This
	// difference is what makes the assertion above a pin rather than a tautology.
	assert.Equal(t, "personal/tool",
		canonicalizeBundleArg(cfg, "personal/tool", dirs, nil))

	// And with a local file of the same spelling present, decision E keeps the
	// ref local — again only visible through the injected filesystem.
	fsLocal, cfgLocal, dirsLocal := w74ShortNameFS(t, true)
	assert.Equal(t, "personal/tool",
		canonicalizeBundleArg(cfgLocal, "personal/tool", dirsLocal, fsLocal))
}

// TestShortNameLocalWins_ReadAndExport is the SHARED behaviour the U081-F14
// cleanup must preserve at the public seam: `bundle view` and `bundle export`
// both accept a per-remote short name whose spelling is claimed by a local
// file, and both serve that local file (decision E). The two call sites spelled
// their filesystem argument differently; collapsing them onto the defaulted one
// must not move this.
func TestShortNameLocalWins_ReadAndExport(t *testing.T) {
	fs, cfg, _ := w74ShortNameFS(t, true)

	read, err := ReadBundle(context.Background(), cfg, ReadBundleRequest{Name: "personal/tool", FS: fs})
	require.NoError(t, err)
	assert.Contains(t, string(read.Raw), "content: hi", "the local file's bytes, not a remote ref")

	exp, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "personal/tool", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "tool.yaml"), exp.Dest)
	got, err := afero.ReadFile(fs, exp.Dest)
	require.NoError(t, err)
	assert.Contains(t, string(got), "content: hi")
}
