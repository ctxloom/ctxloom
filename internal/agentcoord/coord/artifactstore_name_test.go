package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U019-F13 — the store's file name is a content hash by contract, so the
// store must ENFORCE that rather than assume it.
//
// artifactStore.open joined its argument onto the store directory with no
// validation at all, so any caller holding a name that is not a hash reached
// outside the store. Today's only production caller (DownloadArtifact) feeds
// it a value read back out of a journal, and the journal is a FILE — the
// wire path cannot produce a bad name (the record's sha is
// hex.EncodeToString output by construction), but a hand-edited, corrupted
// or restored-from-elsewhere journal can.
//
// The guard belongs at the store because the store is what owns the
// invariant: "the name IS the sha256 of the bytes". Anything else is not a
// miss, it is a malformed request.
func TestArtifactStore_OpenRefusesNamesThatAreNotHashes(t *testing.T) {
	root := t.TempDir()
	// A file that exists OUTSIDE the store but inside the temp root: the
	// traversal target has to really be there or the test passes for the
	// wrong reason (ENOENT rather than refusal).
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret"), []byte("not an artifact"), 0o600))
	st, err := newArtifactStore(root)
	require.NoError(t, err)

	// "deadbeef" is hex but not a sha256's 64 nibbles; the uppercase one is
	// 64 nibbles but is not what hex.EncodeToString ever emits, so accepting
	// it would give one blob two names on a case-insensitive filesystem.
	names := []string{
		"../secret",
		"",
		"deadbeef",
		"/etc/passwd",
		"DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			f, oerr := st.open(name)
			require.Error(t, oerr, "a name that is not a lowercase sha256 hex must be refused, not joined onto the store dir")
			if f != nil {
				_ = f.Close()
			}
			assert.ErrorIs(t, oerr, errArtifactBadName)
		})
	}
}

// The ordinary path is untouched: a real content hash still opens.
func TestArtifactStore_OpenAcceptsARealHash(t *testing.T) {
	st, err := newArtifactStore(t.TempDir())
	require.NoError(t, err)
	shaHex, _, err := st.writeAtomic(strings.NewReader("artifact bytes"), nil, 0)
	require.NoError(t, err)

	f, err := st.open(shaHex)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
