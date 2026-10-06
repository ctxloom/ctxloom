package runner

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// A downloaded artifact is placed through the Home's afero.Fs, so a decorator
// on that fs sees the placement. Driven on a MemMapFs at a destination absent
// from disk: a raw os call would create the directory and file on disk.
func TestHome_DownloadArtifact_PlacesThroughItsFs(t *testing.T) {
	sum := sha256.Sum256([]byte("hello world"))
	h := downloadHome(t, &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{
		headerFrame(sum[:]), chunkFrame("hello "), chunkFrame("world"),
	}})
	mem := afero.NewMemMapFs()
	h.fs = mem
	root := filepath.Join(t.TempDir(), "never-on-disk")
	dest := filepath.Join(root, "nested", "out.txt")

	_, _, err := h.DownloadArtifact(context.Background(), "agent-a", "art-1", dest)
	require.NoError(t, err)

	got, err := afero.ReadFile(mem, dest)
	require.NoError(t, err, "the artifact must be placed in the Home's fs")
	assert.Equal(t, "hello world", string(got))
	info, err := mem.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a placed artifact keeps its owner-only mode")
	entries, err := afero.ReadDir(mem, filepath.Dir(dest))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file may survive beside the placed artifact")

	_, err = os.Stat(root)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
