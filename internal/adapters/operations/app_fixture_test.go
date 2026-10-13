package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// regenTestApp creates a project layout (appDir with a bundles dir) and a
// separate workDir, returning both.
func regenTestApp(t *testing.T) (appDir, workDir string) {
	t.Helper()
	tmp := t.TempDir()
	appDir = filepath.Join(tmp, ".ctxloom")
	require.NoError(t, os.MkdirAll(authoredV1(appDir), 0o755))
	workDir = filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	return appDir, workDir
}

func writeRegenBundle(t *testing.T, appDir, name, content string) {
	t.Helper()
	bundletree.WriteOS(t, authoredV1(appDir), name, content)
}

// TestUpdateProfile_ValidationFailureIsRejected pins the validate-then-mutate
// ordering: a bad AddParents validation fails the update up front, before any
// profile write.
func TestUpdateProfile_ValidationFailureIsRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/profiles/base.yaml", "description: base\n", 0o644)
	loader := profiles.NewLoader([]string{"/profiles"}, profiles.WithFS(fs))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/app"}})

	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		AddParents: []string{"missing-parent"},
		Loader:     loader,
	})
	require.Error(t, err)
}

// materializedContext runs the at-rest delivery (Materialize, every kind,
// for the mock engine) into dir and returns the context file it wrote:
// the bytes an agent launched there with ctxloom out of the loop loads.
func materializedContext(t *testing.T, cfg *config.Config, dir string) string {
	t.Helper()
	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{Target: dir, Engines: []string{"mock"}, Explicit: true})
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	b, err := os.ReadFile(filepath.Join(dir, mock.ContextFileName))
	require.NoError(t, err, "the at-rest delivery must actually write a context file")
	return string(b)
}
