package operations

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// cfgWithDirProfiles builds a config whose profiles are DIRECTORY profiles
// written into the caller's own afero.Fs.
//
// It is the replacement for `config.Fixture{Profiles: ...}`: the inline arm is
// retired, so a profile is a file. The file is written through the SAME fs the
// test drives and SetFS points the loader at it — config.ProfileLoaderOptions
// wires profiles.WithFS from that fs — so a memfs test stays a memfs test and
// never reads the developer's real ~/.ctxloom.
//
// appDir is added to extra.AppPaths, which is what makes the loader look there
// at all. Everything else the caller wants on the fixture is passed through.
func cfgWithDirProfiles(t *testing.T, fs afero.Fs, appDir string, defs map[string]config.Profile, extra config.Fixture) *config.Config {
	t.Helper()
	seed := make(map[string]any, len(defs))
	for name, p := range defs {
		seed[name] = p
	}
	testsupport.WriteDirProfiles(t, fs, appDir, seed)

	found := false
	for _, p := range extra.AppPaths {
		if p == appDir {
			found = true
			break
		}
	}
	if !found {
		extra.AppPaths = append(extra.AppPaths, appDir)
	}
	cfg := config.NewFixture(extra)
	cfg.SetFS(fs)
	cfg.DisableCompanionProbe()
	return cfg
}
