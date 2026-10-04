package projectid

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

const versionedEntry = "projects:\n  - project_id: seeded-id\n    path: /nowhere/seeded\n"

func seedRegistry(t *testing.T, body string) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	m, err := Open(path)
	require.NoError(t, err)
	return m, path
}

// An index written before it was versioned (no key at all) and a current one
// both load, and reading writes nothing.
func TestRegistry_KeylessAndCurrentLoadAndReadingNeverWrites(t *testing.T) {
	for name, body := range map[string]string{
		"keyless": versionedEntry,
		"current": schemaver.Key + ": " + strconv.Itoa(registryKind.Current()) + "\n" + versionedEntry,
	} {
		t.Run(name, func(t *testing.T) {
			m, path := seedRegistry(t, body)
			e, err := m.ResolveByID("seeded-id")
			require.NoError(t, err)
			require.NotNil(t, e)
			assert.Equal(t, "/nowhere/seeded", e.Path)

			onDisk, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, body, string(onDisk), "a read must not write")
		})
	}
}

func TestRegistry_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	m, _ := seedRegistry(t, schemaver.Key+": "+strconv.Itoa(registryKind.Current()+1)+"\n"+versionedEntry)
	_, err := m.ResolveByID("seeded-id")
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, registryKind.Current()+1, ve.Found)
	assert.Equal(t, registryKind.Current(), ve.Current)
}

func TestRegistry_WriterStampsSchemaVersion(t *testing.T) {
	m, path := seedRegistry(t, versionedEntry)
	_, err := m.Mint(t.TempDir())
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, registryKind.Current(), doc[schemaver.Key])
}

// An index that is not YAML is its parse failure, not a version fault.
func TestRegistry_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	m, _ := seedRegistry(t, "projects: [unterminated\n")
	_, err := m.ResolveByID("seeded-id")
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
