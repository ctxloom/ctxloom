package fsstatic

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const versionedTarget = "/proj/.mcp.json"

func claimsRecordPath() string {
	return filepath.Join(claimsDir, confpatch.RecordPrefix(versionedTarget)+claimsSuffix)
}

// writeClaimsRecord puts a hand-written record for versionedTarget on fs and
// returns its body.
func writeClaimsRecord(t *testing.T, fs afero.Fs, versionKey string, version int) string {
	t.Helper()
	body := versionKey + ": " + strconv.Itoa(version) + "\ntarget: " + versionedTarget +
		"\nseq: 1\npaths:\n  /mcpServers/ctxloom:\n    - writer: project\n      seq: 1\n      value: {command: ctxloom}\n"
	testsupport.WriteFileString(t, fs, claimsRecordPath(), body, 0o600)
	return body
}

// Both read paths — one target's record (Paths) and every record (Writers) —
// read the current key and the legacy `claims` key, and neither writes.
func TestClaimsRecord_CurrentAndLegacyKeyedLoadAndReadingNeverWrites(t *testing.T) {
	for _, key := range []string{schemaver.Key, "claims"} {
		t.Run(key, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			body := writeClaimsRecord(t, fs, key, claimsKind.Current())
			c := newRecords(t, fs)

			states, err := c.Paths(fs, versionedTarget)
			require.NoError(t, err)
			require.Len(t, states, 1)
			writers, err := c.Writers()
			require.NoError(t, err)
			assert.Equal(t, []delivery.Writer{project}, writers)

			assert.Equal(t, body, read(t, fs, claimsRecordPath()), "a read must not write")
		})
	}
}

func TestClaimsRecord_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeClaimsRecord(t, fs, schemaver.Key, claimsKind.Current()+1)
	c := newRecords(t, fs)

	_, err := c.Paths(fs, versionedTarget)
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, claimsKind.Current()+1, ve.Found)
	assert.Equal(t, claimsKind.Current(), ve.Current)

	_, err = c.Writers()
	require.ErrorIs(t, err, schemaver.ErrNewer, "the every-record walk must refuse it too, not read it blind")
}

func TestClaimsRecord_WriterStampsSchemaVersion(t *testing.T) {
	fs := withUserFile(t)
	c := newRecords(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))

	var m map[string]any
	require.NoError(t, yamlv3.Unmarshal([]byte(read(t, fs, filepath.Join(claimsDir, confpatch.RecordPrefix(mcpTarget)+claimsSuffix))), &m))
	assert.Equal(t, claimsKind.Current(), m[schemaver.Key])
	_, legacy := m["claims"]
	assert.False(t, legacy, "the legacy spelling is not written")
}
