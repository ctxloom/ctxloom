package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ownRecordsDir gives a test that delivers on the real filesystem its own
// ownership record store: the package's tests otherwise share one sandbox
// HOME, and every delivery adds records to the store all later ones open.
func ownRecordsDir(t *testing.T) {
	t.Helper()
	t.Cleanup(paths.SetHomeRecordsDirForTesting(t.TempDir()))
}

// Installing hooks reads the ownership records of the files it delivers,
// never every record in the home store: the store holds one for every file
// ctxloom ever delivered, in every project, and a delivery that decoded them
// all got slower with each one. The forcing record below cannot be decoded
// at all (it is from a newer generation), so a delivery that read it fails.
func TestApplyHooks_ReadsNoRecordOfAnotherProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	apply := func() {
		t.Helper()
		cfg := cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
		}}, config.Fixture{})
		_, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
			Backend: "claude-code", FS: fs, Cfg: cfg, WorkDir: "/project",
		})
		require.NoError(t, err)
	}
	apply()

	dir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	elsewhere := "/another-project/.claude/settings.json"
	testsupport.WriteFileString(t, fs, filepath.Join(dir, confpatch.RecordPrefix(elsewhere)+".claims.yaml"),
		"schema_version: 999\ntarget: "+elsewhere+"\nseq: 1\npaths: {}\n", 0o600)

	apply()
}
