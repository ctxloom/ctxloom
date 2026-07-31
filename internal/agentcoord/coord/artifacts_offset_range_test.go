package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// TestDownloadArtifact_OffsetPastEndIsRefused pins the arm the resumable
// download never checked: an offset at or beyond the stored size.
//
// os.File.Seek past EOF SUCCEEDS, so the request used to be answered with a
// header claiming the artifact's full size_bytes and whole-file sha256,
// followed by zero chunk frames and a clean end of stream. That is this
// project's characteristic failure exactly: a successful-looking response
// carrying no bytes. A receiver that trusted the header would place an empty
// file; the one shipped client happens to hash what it received and fail
// afterwards, which turns a server-side range error into an unexplained
// integrity failure at the caller.
//
// The assertion is on the STATUS, not on the byte count — "zero bytes and no
// error" is precisely the outcome being ruled out, so counting bytes would
// pass against the defect if the stream were ever made to close differently.
func TestDownloadArtifact_OffsetPastEndIsRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("twenty-five bytes of body")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/range", "range.bin", "application/octet-stream", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, child, "plan/range", data, receipt)

	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	t.Run("beyond the end", func(t *testing.T) {
		body, derr := drainDownload(t, client, out.Harp, "plan/range", uint64(len(data))+100)
		require.Error(t, derr, "an unsatisfiable range must be refused, not answered with zero bytes (got %d)", len(body))
		assert.Equal(t, codes.InvalidArgument, statusCode(derr))
	})

	t.Run("exactly at the end", func(t *testing.T) {
		// The boundary is unsatisfiable too: offset == size names the byte
		// after the last one, so there is nothing to send.
		body, derr := drainDownload(t, client, out.Harp, "plan/range", uint64(len(data)))
		require.Error(t, derr, "an offset at the end must be refused, not answered with zero bytes (got %d)", len(body))
		assert.Equal(t, codes.InvalidArgument, statusCode(derr))
	})

	t.Run("last byte still streams", func(t *testing.T) {
		// The guard must not eat the legitimate final-byte resume.
		body, derr := drainDownload(t, client, out.Harp, "plan/range", uint64(len(data))-1)
		require.NoError(t, derr)
		assert.Equal(t, data[len(data)-1:], body)
	})
}
