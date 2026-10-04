package operations

import (
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const refusalTestPath = "/proj/.ctxloom/cache/refused_advances.yaml"

func refusalBody(versionKey string, version int) string {
	return versionKey + ": " + strconv.Itoa(version) + "\nrefusals:\n  - identity: x\n    kept_sha: a\n"
}

func TestReadRefusalDoc_CurrentAndLegacyKeyedLoadAndReadingNeverWrites(t *testing.T) {
	for _, key := range []string{schemaver.Key, "version"} {
		t.Run(key, func(t *testing.T) {
			fsys := afero.NewMemMapFs()
			body := refusalBody(key, refusalKind.Current())
			testsupport.WriteFileString(t, fsys, refusalTestPath, body, 0o644)

			d, err := readRefusalDoc(fsys, refusalTestPath)
			require.NoError(t, err)
			require.Len(t, d.Refusals, 1)
			assert.Equal(t, "a", d.Refusals[0].KeptSHA)

			onDisk, err := afero.ReadFile(fsys, refusalTestPath)
			require.NoError(t, err)
			assert.Equal(t, body, string(onDisk), "a read must not write")
		})
	}
}

func TestReadRefusalDoc_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	fsys := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fsys, refusalTestPath, refusalBody(schemaver.Key, refusalKind.Current()+1), 0o644)
	_, err := readRefusalDoc(fsys, refusalTestPath)
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, refusalKind.Current()+1, ve.Found)
	assert.Equal(t, refusalKind.Current(), ve.Current)
}

// A cache that is not YAML is its parse failure, not a version fault.
func TestReadRefusalDoc_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	fsys := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fsys, refusalTestPath, "refusals: [unterminated\n", 0o644)
	_, err := readRefusalDoc(fsys, refusalTestPath)
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
