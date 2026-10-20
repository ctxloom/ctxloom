package sessions

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The output dir is recorded ABSOLUTE at mint, under the plain project name,
// and read back from the sidecar by harp — whatever the config says later.
func TestRecordOutputDir_RecordsBaseProjectHarpAndReadsBack(t *testing.T) {
	testsupport.Isolate(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/acme/widget", "claude-code")
	require.NoError(t, err)
	base := t.TempDir()

	dir, err := m.RecordOutputDir(e.HarpName, base)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "widget", e.HarpName), dir)

	got, err := OutputDir(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, dir, got)
	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, dir, found.OutputDir)
}

func TestOutputDir_ASessionThatRecordedNoneIsTheSentinel(t *testing.T) {
	testsupport.Isolate(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	_, err = OutputDir(e.HarpName)
	assert.ErrorIs(t, err, ErrNoOutputDir)
}

func TestMemStoreRecordOutputDir_MatchesTheManager(t *testing.T) {
	mem := NewMemStore()
	e, err := mem.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	dir, err := mem.RecordOutputDir(e.HarpName, "/out")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "widget", e.HarpName), dir)
	_, err = mem.RecordOutputDir("no-such-harp", "/out")
	assert.ErrorIs(t, err, ErrNotFound)
}

// A harp that is no session at all is ErrNotFound, told apart from a session
// that recorded no output dir.
func TestOutputDir_NoSuchSessionIsNotFound(t *testing.T) {
	testsupport.Isolate(t)
	_, err := OutputDir("never-minted-harp")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NotErrorIs(t, err, ErrNoOutputDir)
}
