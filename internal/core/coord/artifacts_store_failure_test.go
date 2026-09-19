package coord

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// When the STORE fails, the caller must be told what the store
// said.
//
// UploadArtifact bridges the Recv loop onto writeAtomic through an io.Pipe.
// If writeAtomic fails (temp-file create, a full disk mid-write, fsync), the
// handler closes the read half to unwind, and that close is exactly what the
// chunk goroutine then reports: io.ErrClosedPipe. Preferring the chunk error
// unconditionally therefore answered a store failure with "upload: io:
// read/write on closed pipe" — a message about our own unwinding, naming
// neither the failure nor anything an operator can act on.
//
// A real chunk-shape or transport error still wins, because that one closed
// the pipe with ITSELF as the reason and the store failure is downstream of
// it; only the pipe-close artifact yields.
func TestArtifactUpload_StoreFailureReportsTheStoreError(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	// Removing the store directory makes os.CreateTemp fail with ENOENT for
	// every caller including root — a chmod-based denial is a no-op when the
	// suite runs as root, and this must fail the same way everywhere.
	require.NoError(t, os.RemoveAll(c.artifacts.dir))

	data := []byte("bytes that will never reach a blob")
	sum := sha256.Sum256(data)
	_, err := uploadRaw(t, client, out.RunID, "plan/rollout", data, sum[:], 0)
	require.Error(t, err)
	assert.Equal(t, codes.Internal, statusCode(err))
	assert.Contains(t, err.Error(), "artifact store",
		"the store's own failure is the cause and must be what the caller is told")
	assert.NotContains(t, err.Error(), "closed pipe",
		"io.ErrClosedPipe is this handler unwinding itself, never a cause worth reporting")
}

// The other half of the same precedence rule: a genuine chunk-shape
// violation stays authoritative even though writeAtomic also fails (it is
// reading a pipe that was closed WITH that error), so the yield above is
// scoped to the artifact and cannot swallow a real client fault.
func TestArtifactUpload_ChunkShapeErrorStillWins(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	stream, err := client.UploadArtifact(context.Background())
	require.NoError(t, err)
	data := []byte("some content here")
	sum := sha256.Sum256(data)
	require.NoError(t, stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Header{Header: &agentcoordpb.ArtifactUploadHeader{
		RunId:      out.RunID,
		ArtifactId: "plan/out-of-order",
		Name:       "plan",
		MediaType:  "application/octet-stream",
		SizeBytes:  uint64(len(data)),
		Sha256:     sum[:],
	}}}))
	// Offset 7 where 0 is expected: the shape violation the goroutine is
	// there to catch.
	_ = stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Chunk{Chunk: &agentcoordpb.ArtifactChunk{
		Offset: 7,
		Data:   data,
	}}})
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
	assert.Contains(t, err.Error(), "out-of-order chunk")
}
