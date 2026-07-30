package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// TestCreateProfile_DoesNotClobberAnUnparseableProfile asserts the PAYLOAD, not
// the exit status: creating a profile whose name is already taken by a file
// that fails to parse must leave that file's bytes exactly as they were.
// CreateProfile decides a name is free from Store.Exists, which used to answer
// by attempting a full Load — so a YAML syntax error read as "absent", Save
// wrote a brand-new profile over the top, and the user's file was gone with a
// success message (U091-F06).
func TestCreateProfile_DoesNotClobberAnUnparseableProfile(t *testing.T) {
	const authored = "bundles: [unclosed\ndescription: hand written\n"

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/app/profiles", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/app/profiles/mine.yaml", []byte(authored), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/app"}})
	cfg.SetFS(fs)
	loader := profiles.NewLoader([]string{"/app/profiles"}, profiles.WithFS(fs))

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:    "mine",
		Bundles: []string{"go-development"},
		Loader:  loader,
	})
	require.Error(t, err, "creating over an existing (if broken) profile must be refused")

	after, readErr := afero.ReadFile(fs, "/app/profiles/mine.yaml")
	require.NoError(t, readErr)
	assert.Equal(t, authored, string(after), "the authored bytes must survive untouched")
}

// TestProfileLoader_HonoursTheInjectedFilesystem pins that the profile loader
// operations build reads the SAME filesystem the config was given. It resolved
// its directories through cfg.FS() but then constructed a loader with no
// WithFS, so the loader defaulted to the OS filesystem: with an injected
// filesystem the directories were discovered and then found empty, and
// `profile list` reported zero profiles with no error at all (U091-F22).
func TestProfileLoader_HonoursTheInjectedFilesystem(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/app/profiles", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/app/profiles/alpha.yaml", []byte("bundles:\n  - go-development\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/app"}})
	cfg.SetFS(fs)

	list, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{})
	require.NoError(t, err)

	names := make([]string, 0, len(list.Profiles))
	for _, p := range list.Profiles {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"alpha"}, names, "the profile written to the injected filesystem must be listed")
}
