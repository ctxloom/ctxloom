package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// emptyProfileDocs are the documents yaml.v3 accepts into a zero-valued
// config.Profile without complaint. Verified against yaml.v3: only a bare
// scalar errors — "", whitespace and a comment-only file all unmarshal cleanly.
var emptyProfileDocs = map[string]string{
	"nothing at all":  "",
	"whitespace only": "   \n\t\n",
	"comments only":   "# my profile\n# (todo)\n",
	"explicit null":   "null\n",
}

// TestSetProfileContent_EmptyContentIsRejected pins that `ctxloom profile
// edit` writes the editor's buffer back through SetProfileContent, whose only
// guard is that the YAML parses — and empty, whitespace-only and comment-only
// documents all parse. An editor exited with an emptied buffer therefore
// truncated the profile to zero bytes and reported Status "updated". Data loss
// through the ordinary edit path.
func TestSetProfileContent_EmptyContentIsRejected(t *testing.T) {
	for name, doc := range emptyProfileDocs {
		t.Run(name, func(t *testing.T) {
			cfg, fs := profileTransferFixture(t, "rev", "llm: claude-fast\ndescription: reviewer\n")

			res, err := SetProfileContent(context.Background(), cfg, SetProfileContentRequest{
				Name: "rev", Content: doc, FS: fs,
			})
			require.Error(t, err, "an empty profile document must not overwrite the profile")
			assert.Nil(t, res)

			// The original must be intact — this is the loss the row describes.
			data, readErr := afero.ReadFile(fs, profilePath(cfg, "rev"))
			require.NoError(t, readErr)
			assert.Contains(t, string(data), "reviewer", "the profile must survive a rejected edit")
		})
	}
}

// TestImportProfile_EmptyFileIsRejected pins that the same yaml.v3 hole let
// `ctxloom profile import` accept an empty or comment-only file as a valid
// profile and report Status "imported".
func TestImportProfile_EmptyFileIsRejected(t *testing.T) {
	for name, doc := range emptyProfileDocs {
		t.Run(name, func(t *testing.T) {
			cfg, fs := profileTransferFixture(t, "rev", "llm: claude-fast\n")
			require.NoError(t, afero.WriteFile(fs, "/src/hollow.yaml", []byte(doc), 0o644))

			res, err := ImportProfile(context.Background(), cfg, ImportProfileRequest{
				SourcePath: "/src/hollow.yaml", FS: fs,
			})
			require.Error(t, err, "a profile document carrying nothing must not import")
			assert.Nil(t, res)

			exists, existsErr := afero.Exists(fs, profilePath(cfg, "hollow"))
			require.NoError(t, existsErr)
			assert.False(t, exists, "nothing may be written for a rejected import")
		})
	}
}

// TestExportProfile_EmptySourceIsRejected pins the same hole on the way OUT: a
// zero-byte profile file copied to a staging dir reported "exported", and the
// loss is discovered by whoever receives the artifact.
func TestExportProfile_EmptySourceIsRejected(t *testing.T) {
	cfg, fs := profileTransferFixture(t, "hollow", "# nothing here\n")

	res, err := ExportProfile(context.Background(), cfg, ExportProfileRequest{
		Name: "hollow", DestDir: "/out", FS: fs,
	})
	require.Error(t, err, "exporting a profile that carries nothing must fail")
	assert.Nil(t, res)
}

// A real profile still round-trips through all three — and so does a LABELS-ONLY
// one, which is a normal half-authored state: profiles.Loader.Save draws the
// refusal line at IsEmptyDocument (nothing at all, not even a label) and these
// paths draw the same one. These are the discriminators that keep the checks
// above from rejecting ordinary use.
func TestProfileTransfer_RealProfileStillRoundTrips(t *testing.T) {
	cfg, fs := profileTransferFixture(t, "rev", "llm: claude-fast\ndescription: reviewer\n")

	setRes, err := SetProfileContent(context.Background(), cfg, SetProfileContentRequest{
		Name: "rev", Content: "llm: claude-fast\ndescription: edited\n", FS: fs,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", setRes.Status)

	expRes, err := ExportProfile(context.Background(), cfg, ExportProfileRequest{Name: "rev", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "exported", expRes.Status)

	require.NoError(t, afero.WriteFile(fs, "/src/other.yaml", []byte("description: another\n"), 0o644))
	impRes, err := ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: "/src/other.yaml", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "imported", impRes.Status)
}

// profileTransferFixture builds a config over a MemMapFs holding one local
// profile file with the given body.
func profileTransferFixture(t *testing.T, name, body string) (*config.Config, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/app/.ctxloom"}})
	dir := paths.ProfilesPath(cfg.GetAppPaths()[0])
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	require.NoError(t, afero.WriteFile(fs, profilePath(cfg, name), []byte(body), 0o644))
	return cfg, fs
}

func profilePath(cfg *config.Config, name string) string {
	return paths.ProfilesPath(cfg.GetAppPaths()[0]) + "/" + name + ".yaml"
}
