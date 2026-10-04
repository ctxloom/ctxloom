package admission_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writtenVersion records one decision and returns the schema_version the
// store wrote — the generation this build reads, learned without restating
// it.
func writtenVersion(t *testing.T) int {
	t.Helper()
	s, fs, path := newTestStore(t)
	_, err := s.Set(testKey{"a", "1"}, true)
	require.NoError(t, err)
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	v, ok := m[schemaver.Key].(int)
	require.True(t, ok, "the store must stamp %s: %s", schemaver.Key, data)
	_, legacy := m["version"]
	assert.False(t, legacy, "the legacy spelling is not written")
	return v
}

func recordBody(versionKey string, version int) string {
	return versionKey + ": " + strconv.Itoa(version) + "\nrecords:\n  - key: {scope: a, fine: \"1\"}\n    approved: true\n"
}

func TestStore_CurrentAndLegacyKeyedFilesLoadAndReadingNeverWrites(t *testing.T) {
	current := writtenVersion(t)
	for _, key := range []string{schemaver.Key, "version"} {
		t.Run(key, func(t *testing.T) {
			s, fs, path := newTestStore(t)
			body := recordBody(key, current)
			testsupport.WriteFileString(t, fs, path, body, 0o600)

			recs, err := s.List()
			require.NoError(t, err)
			require.Len(t, recs, 1)
			assert.True(t, recs[0].Approved)

			onDisk, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Equal(t, body, string(onDisk), "a read must not write")
		})
	}
}

func TestStore_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	current := writtenVersion(t)
	s, fs, path := newTestStore(t)
	testsupport.WriteFileString(t, fs, path, recordBody(schemaver.Key, current+1), 0o600)

	_, err := s.List()
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, current+1, ve.Found)
	assert.Equal(t, current, ve.Current)

	d, derr := s.Decide(context.Background(), testKey{"a", "1"}, nil)
	require.ErrorIs(t, derr, schemaver.ErrNewer)
	assert.False(t, d.Allow, "an unreadable store may hold a denial")
	assert.Equal(t, reasonFault, d.Reason)
}

// A store with no version at all predates the versioned format; there is no
// migration from it, and reading it as empty would re-open closed doors.
func TestStore_KeylessIsRefusedAsTooOld(t *testing.T) {
	s, fs, path := newTestStore(t)
	testsupport.WriteFileString(t, fs, path, "records: []\n", 0o600)
	_, err := s.List()
	require.ErrorIs(t, err, schemaver.ErrTooOld)
}
