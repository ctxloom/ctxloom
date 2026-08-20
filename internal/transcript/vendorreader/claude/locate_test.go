package claude

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

func seedStore(t *testing.T) (afero.Fs, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	home := "/home/u"
	w := func(projDir, sess, cwd string) {
		p := filepath.Join(home, ".claude", "projects", projDir, sess+".jsonl")
		require.NoError(t, fs.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, afero.WriteFile(fs, p,
			[]byte(`{"type":"user","sessionId":"`+sess+`","cwd":"`+cwd+`"}`+"\n"), 0o644))
	}
	w("-proj-alpha", "aaa", "/proj/alpha")
	w("-proj-alpha", "bbb", "/proj/alpha")
	w("-proj-beta", "ccc", "/proj/beta")
	return fs, home
}

// TestDiscover_FindsEverySessionAndBindsByRecordedCwd pins both halves: the
// store is enumerated, and each session's project comes from the `cwd` the
// transcript RECORDS rather than from the directory name that encodes it.
func TestDiscover_FindsEverySessionAndBindsByRecordedCwd(t *testing.T) {
	fs, home := seedStore(t)
	l := Locator{FS: fs, Home: home}

	all, err := l.Discover("")
	require.NoError(t, err)
	require.Len(t, all, 3, "every transcript in the store is found when no project is named")

	alpha, err := l.Discover("/proj/alpha")
	require.NoError(t, err)
	require.Len(t, alpha, 2, "filtering by project selects on the RECORDED cwd")
	for _, s := range alpha {
		assert.Equal(t, "/proj/alpha", s.WorkDir)
		assert.Equal(t, "claude-code", s.Engine)
		assert.NotEmpty(t, s.SessionID)
	}

	// The control: a project the store does not hold yields nothing, so the
	// filter above is not passing by matching everything.
	none, err := l.Discover("/proj/gamma")
	require.NoError(t, err)
	assert.Empty(t, none, "a project with no sessions is empty, and that is not an error")
}

// An ABSENT store is legitimately empty — the engine is not installed. This
// must NOT be a refusal, or every machine without claude reports a fault.
func TestDiscover_AbsentStoreIsEmptyNotAnError(t *testing.T) {
	out, err := Locator{FS: afero.NewMemMapFs(), Home: "/nobody"}.Discover("")
	require.NoError(t, err)
	assert.Empty(t, out)
}

// TestDiscover_UnrecognizedStoreRefusesRatherThanReportingZero is the contract
// the retired scrapers broke: kiro's store moved to a SQLite blob and its
// scraper reported NO sessions rather than an error. A store present but not
// in the expected shape must REFUSE, because "you have none" and "I no longer
// understand this" are different answers.
func TestDiscover_UnrecognizedStoreRefusesRatherThanReportingZero(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/home/u"
	root := filepath.Join(home, ".claude", "projects")
	require.NoError(t, fs.MkdirAll(root, 0o755))
	// The shape a storage change produces: content, but not per-project dirs.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(root, "sessions.db"), []byte("SQLite format 3\x00"), 0o644))

	out, err := Locator{FS: fs, Home: home}.Discover("")
	require.Error(t, err, "a store that is present but unrecognised must refuse, not report zero")
	assert.Empty(t, out)
	var unrecognized *vendorreader.UnrecognizedStoreError
	require.ErrorAs(t, err, &unrecognized)
	assert.Contains(t, err.Error(), root, "the message names the store it could not read")
	assert.Contains(t, err.Error(), "refusing to report zero sessions")
}

// TestArch_ClaudeStoreRel_MatchesEngineSpec binds this package's copy of the
// store path to the one isolation declares for container mounts. Two spellings
// of one fact drift; the mount path is exercised by container runs, so it is
// the one that stays honest, and this gate is what keeps the Locator with it.
func TestArch_ClaudeStoreRel_MatchesEngineSpec(t *testing.T) {
	assert.Equal(t, filepath.FromSlash(isolation.ContainerTranscriptStoreRelFor("claude-code")),
		filepath.FromSlash(StoreRel),
		"the Locator's store path must match the one container mounts use")
}
