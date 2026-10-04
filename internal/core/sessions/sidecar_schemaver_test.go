package sessions

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

const versionedSidecar = "project_dir: /proj/a\nbackend: claude-code\noutput_dir: /out/a\nstarted_at: 2026-09-01T10:00:00Z\n"

func readSidecarFile(t *testing.T, root, harp string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, harp, testSidecarName))
	require.NoError(t, err)
	return data
}

// A sidecar written before it was versioned (every one in the wild) and a
// current one both load, through the Manager and through OutputDirOf, and
// neither read writes.
func TestSidecar_KeylessAndCurrentLoadAndReadingNeverWrites(t *testing.T) {
	for name, body := range map[string]string{
		"keyless": versionedSidecar,
		"current": schemaver.Key + ": " + strconv.Itoa(sidecarKind.Current()) + "\n" + versionedSidecar,
	} {
		t.Run(name, func(t *testing.T) {
			m, root := openSidecarRoot(t)
			const harp = "swift-amber-falcon"
			writeSidecar(t, root, harp, body)

			e, err := m.Find(harp)
			require.NoError(t, err)
			require.NotNil(t, e)
			assert.Equal(t, "/proj/a", e.ProjectDir)
			out, ok := OutputDirOf(filepath.Join(root, harp))
			assert.True(t, ok)
			assert.Equal(t, "/out/a", out)

			assert.Equal(t, body, string(readSidecarFile(t, root, harp)), "a read must not write")
		})
	}
}

func TestSidecar_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	m, root := openSidecarRoot(t)
	const harp = "swift-amber-falcon"
	writeSidecar(t, root, harp, schemaver.Key+": "+strconv.Itoa(sidecarKind.Current()+1)+"\n"+versionedSidecar)

	_, err := m.Find(harp)
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, sidecarKind.Current()+1, ve.Found)
	assert.Equal(t, sidecarKind.Current(), ve.Current)

	_, ok := OutputDirOf(filepath.Join(root, harp))
	assert.False(t, ok, "a sidecar this build cannot read records no output dir it can trust")
}

func TestSidecar_WriterStampsSchemaVersion(t *testing.T) {
	m, root := openSidecarRoot(t)
	const harp = "swift-amber-falcon"
	writeSidecar(t, root, harp, versionedSidecar)
	require.NoError(t, m.BindEngine(harp, "another-engine"))

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(readSidecarFile(t, root, harp), &doc))
	assert.Equal(t, sidecarKind.Current(), doc[schemaver.Key])
	assert.Equal(t, "another-engine", doc["backend"])
}

// A sidecar that is not YAML is its parse failure, not a version fault.
func TestSidecar_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	m, root := openSidecarRoot(t)
	const harp = "swift-amber-falcon"
	writeSidecar(t, root, harp, "project_dir: [unterminated\n")

	_, err := m.Find(harp)
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
