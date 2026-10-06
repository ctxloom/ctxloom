package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The artifact store reaches the disk only through the fs it is given.

// statHidingFs can make the FIRST Stat of one path report it absent — the
// window between the store's existence check and its rename, in which a
// concurrent identical upload lands.
type statHidingFs struct {
	afero.Fs
	mu       sync.Mutex
	hideOnce string
}

func (f *statHidingFs) Stat(name string) (os.FileInfo, error) {
	f.mu.Lock()
	hide := name == f.hideOnce
	if hide {
		f.hideOnce = ""
	}
	f.mu.Unlock()
	if hide {
		return nil, os.ErrNotExist
	}
	return f.Fs.Stat(name)
}

func memStore(t *testing.T) (*artifactStore, *statHidingFs) {
	t.Helper()
	fs := &statHidingFs{Fs: afero.NewMemMapFs()}
	st, err := newArtifactStore(fs, "/state")
	require.NoError(t, err)
	return st, fs
}

// agedBlob backdates the blob at path, so a later rename over it — which
// installs the fresh temp file — shows as a changed mtime.
var agedBlob = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func blobAge(t *testing.T, fs afero.Fs, path string) time.Time {
	t.Helper()
	info, err := fs.Stat(path)
	require.NoError(t, err)
	return info.ModTime()
}

func TestArtifactStore_WritesAndReadsThroughItsFs(t *testing.T) {
	st, fs := memStore(t)

	shaHex, n, err := st.writeAtomic(strings.NewReader("in memory"), nil, 0)
	require.NoError(t, err)
	assert.EqualValues(t, len("in memory"), n)
	sum := sha256.Sum256([]byte("in memory"))
	assert.Equal(t, hex.EncodeToString(sum[:]), shaHex)

	got, err := afero.ReadFile(fs, filepath.Join("/state", artifactStoreDirName, shaHex))
	require.NoError(t, err, "the blob lives on the injected fs")
	assert.Equal(t, "in memory", string(got))

	f, err := st.open(shaHex)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "in memory", string(body))

	names, err := afero.ReadDir(fs, st.dir)
	require.NoError(t, err)
	assert.Len(t, names, 1, "no temp file survives a store")
}

func TestArtifactStore_DedupeHitDoesNotRename(t *testing.T) {
	st, fs := memStore(t)

	shaHex, _, err := st.writeAtomic(strings.NewReader("same bytes"), nil, 0)
	require.NoError(t, err)
	require.NoError(t, fs.Chtimes(st.path(shaHex), agedBlob, agedBlob))

	_, _, err = st.writeAtomic(strings.NewReader("same bytes"), nil, 0)
	require.NoError(t, err)
	assert.Equal(t, agedBlob, blobAge(t, fs, st.path(shaHex)).UTC(),
		"a dedupe hit discards its temp; it renames nothing over the stored blob")
	names, err := afero.ReadDir(fs, st.dir)
	require.NoError(t, err)
	assert.Len(t, names, 1, "the dedupe hit's temp is removed")
}

func TestArtifactStore_MismatchStoresNothing(t *testing.T) {
	st, fs := memStore(t)

	_, _, err := st.writeAtomic(strings.NewReader("abc"), []byte("not the hash"), 0)
	require.ErrorIs(t, err, errArtifactSHAMismatch)
	_, _, err = st.writeAtomic(strings.NewReader("abc"), nil, 99)
	require.ErrorIs(t, err, errArtifactSizeMismatch)

	names, err := afero.ReadDir(fs, st.dir)
	require.NoError(t, err)
	assert.Empty(t, names, "a refused upload earns no name and leaves no temp")
}

func TestArtifactStore_EmptyUploadTwiceSucceeds(t *testing.T) {
	st, _ := memStore(t)

	first, _, err := st.writeAtomic(strings.NewReader(""), nil, 0)
	require.NoError(t, err)
	second, _, err := st.writeAtomic(strings.NewReader(""), nil, 0)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// The race the existence check cannot close: an identical EMPTY upload lands
// between this upload's check and its rename. Renaming zero bytes over an
// existing file is exactly what the empty-write guard refuses — and here it
// is correct, because the name is the content hash, so the file being
// replaced is byte-identical.
func TestArtifactStore_EmptyUploadRacingAnIdenticalOneSucceeds(t *testing.T) {
	st, fs := memStore(t)
	emptySum := sha256.Sum256(nil)
	path := st.path(hex.EncodeToString(emptySum[:]))
	testsupport.WriteFile(t, fs, path, nil, 0o600)
	require.NoError(t, fs.Chtimes(path, agedBlob, agedBlob))
	fs.hideOnce = path

	_, _, err := st.writeAtomic(strings.NewReader(""), nil, 0)
	require.NoError(t, err)
	assert.NotEqual(t, agedBlob, blobAge(t, fs, path).UTC(), "the check missed the racer, so the rename ran over it")
}

// A coordinator handed a MemMapFs stores and serves artifacts entirely in
// memory: the upload lands on the injected fs, the download reads it back,
// and an offset still positions the stream.
func TestCoordinator_ArtifactRoundTripOnAnInjectedFs(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	c := newTestCoordinatorWith(t, researcherSpawner(t), func(o *Options) { o.FS = mem })
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("HEADHEADHEAD-TAILTAILTAIL")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/mem", "mem.bin", "application/octet-stream", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, child, "plan/mem", data, receipt)

	blob := filepath.Join(c.stateDir, artifactStoreDirName, hex.EncodeToString(sum[:]))
	onMem, err := afero.ReadFile(mem, blob)
	require.NoError(t, err, "the blob is on the injected fs")
	assert.Equal(t, data, onMem)
	_, err = os.Stat(blob)
	assert.True(t, os.IsNotExist(err), "nothing reached the OS fs")

	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])
	whole, err := drainDownload(t, client, out.Harp, "plan/mem", 0)
	require.NoError(t, err)
	assert.Equal(t, data, whole)
	const off = 13
	tail, err := drainDownload(t, client, out.Harp, "plan/mem", off)
	require.NoError(t, err)
	assert.Equal(t, data[off:], tail)
}
