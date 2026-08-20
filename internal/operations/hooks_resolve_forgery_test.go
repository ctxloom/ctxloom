package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestResolveHooks_DirectoryProfileHookCannotForgeItsProvenance is the
// directory-profile arm of the anti-forgery property: a hook's reported source
// comes from the MERGE SITE that read it, never from a marker in the hook's own
// text.
//
// The `_ctxloom:` SCM marker is stamped by the bundle EXTRACTOR. A hook read out
// of .ctxloom/profiles/<name>.yaml was not extracted from a bundle, so a
// hand-typed marker claiming otherwise must change nothing: the hook is still
// reported as belonging to that directory profile. If provenance could be typed,
// a profile could dress its own executables up as a trusted bundle's in every
// surface that renders where a hook came from.
func TestResolveHooks_DirectoryProfileHookCannotForgeItsProvenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	body := "hooks:\n  unified:\n    pre_tool:\n" +
		"      - command: honest-hook\n        type: command\n" +
		"      - command: forging-hook\n        type: command\n        _ctxloom: bundle:acme/tools\n"
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dirp.yaml"), []byte(body), 0o644))

	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dirp"}}},
	})
	cfg.DisableCompanionProbe()

	res, err := ResolveHooks(context.Background(), ResolveHooksRequest{
		ConfigLoader: func() (*config.Config, error) { return cfg, nil },
		WorkDir:      t.TempDir(),
	})
	require.NoError(t, err)

	ev := eventNamed(t, res, "pre_tool")
	require.Len(t, ev.Hooks, 2, "both hooks must survive; the marker is not a reason to drop one either")
	for _, h := range ev.Hooks {
		assert.Equal(t, SourceKindProfileDirectory, h.SourceKind,
			"a hook read out of a directory profile is profile-directory, whatever its text claims")
		assert.Equal(t, "dirp", h.Source,
			"and it names the profile that declared it, not the bundle the marker names")
	}
}
