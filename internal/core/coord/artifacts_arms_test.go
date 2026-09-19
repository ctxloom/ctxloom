package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Characterization: the guard arms of UploadArtifact and
// DownloadArtifact that no test reached, pinned BEFORE those two functions
// are split (both were over the project's CCN-10 gate). A pure complexity
// reduction cannot be red by definition, so the honest discriminator is
// coverage of every arm being moved, green before and after.
//
// One arm is deliberately not covered: the 64 MiB total-size cap in the
// chunk loop. Reaching it means actually pushing 64 MiB through a real gRPC
// stream, which buys a branch at a cost the suite should not pay on every
// run; the DECLARED-size cap right above it is covered instead, and both
// arms return the same shape.

func TestUploadArtifact_RejectsAnOversizedChunk(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	data := make([]byte, artifactChunkCap+1)
	sum := sha256.Sum256(data)
	_, err := uploadRaw(t, client, out.RunID, "big/chunk", data, sum[:], len(data))
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
	assert.Contains(t, err.Error(), "exceeds")
}

func TestUploadArtifact_RejectsADeclaredSizeOverTheCap(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	stream, err := client.UploadArtifact(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Header{Header: &agentcoordpb.ArtifactUploadHeader{
		RunId:      out.RunID,
		ArtifactId: "big/declared",
		Name:       "big",
		MediaType:  "application/octet-stream",
		SizeBytes:  artifactUploadSizeCap + 1,
	}}}))
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
	assert.Contains(t, err.Error(), "cap")
}

func TestUploadArtifact_RejectsAChunkBeforeTheHeader(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	stream, err := client.UploadArtifact(context.Background())
	require.NoError(t, err)
	_ = stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Chunk{Chunk: &agentcoordpb.ArtifactChunk{Data: []byte("no header first")}}})
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
	assert.Contains(t, err.Error(), "must be header")
}

func TestDownloadArtifact_RequiresBothIdentifiers(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	for _, tc := range []struct {
		name, agentID, artifactID, want string
	}{
		{name: "no agent_id", agentID: "", artifactID: "plan/x", want: "agent_id is required"},
		{name: "no artifact_id", agentID: out.Harp, artifactID: "", want: "artifact_id is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := drainDownload(t, client, tc.agentID, tc.artifactID, 0)
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, statusCode(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestDownloadArtifact_UnknownArtifactIsNotFound(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	_, err := drainDownload(t, client, out.Harp, "plan/never-produced", 0)
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, statusCode(err))
}

// The server's resumable-download arm. The shipped client never sets offset,
// so this is the only thing exercising the seek.
func TestDownloadArtifact_OffsetStreamsTheTail(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("HEADHEADHEAD-TAILTAILTAIL")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/seek", "seek.bin", "application/octet-stream", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, child, "plan/seek", data, receipt)

	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	const off = 13
	got, err := drainDownload(t, client, out.Harp, "plan/seek", off)
	require.NoError(t, err)
	assert.Equal(t, data[off:], got, "an offset download streams the tail from that byte")

	whole, err := drainDownload(t, client, out.Harp, "plan/seek", 0)
	require.NoError(t, err)
	assert.Equal(t, data, whole)
}

// drainDownload runs a raw DownloadArtifact to completion, returning the
// concatenated chunk bytes. Home's client cannot express an offset, and the
// error arms below need the status rather than Home's wrapping.
func drainDownload(t *testing.T, client agentcoordpb.ArtifactTransferServiceClient, agentID, artifactID string, offset uint64) ([]byte, error) {
	t.Helper()
	stream, err := client.DownloadArtifact(context.Background(), &agentcoordpb.ArtifactDownloadRequest{
		AgentId:    agentID,
		ArtifactId: artifactID,
		Offset:     offset,
	})
	if err != nil {
		return nil, err
	}
	var body []byte
	for {
		frame, rerr := stream.Recv()
		if rerr == io.EOF {
			return body, nil
		}
		if rerr != nil {
			return nil, rerr
		}
		if chunk := frame.GetChunk(); chunk != nil {
			body = append(body, chunk.GetData()...)
		}
	}
}
