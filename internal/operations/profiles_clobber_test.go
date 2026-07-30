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
