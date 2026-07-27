package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// E1e — artifact transfer, hermetic: a LIVE coordinator (real gRPC
// listeners, durable stores) driven through Home (the runner side) and raw
// ArtifactTransferService clients (for shapes Home's clean API cannot
// misuse on purpose — corruption injection, forged ownership). Mirrors
// runchannel_test.go's conformance style.

// dialArtifactClient opens a raw ArtifactTransferServiceClient against c's
// live loopback listener under token — used where a test needs to send
// something Home's API would never construct (a forged header, a corrupt
// chunk).
func dialArtifactClient(t *testing.T, c *Coordinator, token string) agentcoordpb.ArtifactTransferServiceClient {
	t.Helper()
	target, err := grpcTarget(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return agentcoordpb.NewArtifactTransferServiceClient(conn)
}

// uploadRaw drives UploadArtifact by hand (caller-controlled chunk sizes and
// declared hash) — the corruption-injection and chunk-shape tests need this;
// everything else goes through Home.UploadArtifact.
func uploadRaw(t *testing.T, client agentcoordpb.ArtifactTransferServiceClient, runID, artifactID string, data []byte, declaredSHA []byte, chunkSize int) (*agentcoordpb.ArtifactReceipt, error) {
	t.Helper()
	stream, err := client.UploadArtifact(context.Background())
	require.NoError(t, err)
	// A gRPC client-stream Send can legitimately return io.EOF when the
	// server has ALREADY closed the stream before this Send lands — e.g. the
	// auth interceptor rejecting a consumer credential's UploadArtifact
	// under load, server-close winning the race against the client's first
	// frame (flaky-agentcoord S3, hoary-amigo: a require.NoError here was
	// asserting on a race outcome, not a real client bug). The authoritative
	// status always rides CloseAndRecv, never a Send error — so a header
	// Send failure here is tolerated, not fatal: fall straight through to
	// CloseAndRecv to surface the real rejection reason.
	if serr := stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Header{Header: &agentcoordpb.ArtifactUploadHeader{
		RunId:      runID,
		ArtifactId: artifactID,
		Name:       artifactID,
		MediaType:  "application/octet-stream",
		SizeBytes:  uint64(len(data)),
		Sha256:     declaredSHA,
	}}}); serr != nil {
		return stream.CloseAndRecv()
	}
	if chunkSize <= 0 {
		chunkSize = len(data)
		if chunkSize == 0 {
			chunkSize = 1
		}
	}
	for off := 0; off < len(data); off += chunkSize {
		end := min(off+chunkSize, len(data))
		if err := stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Chunk{Chunk: &agentcoordpb.ArtifactChunk{
			Offset: uint64(off),
			Data:   data[off:end],
		}}}); err != nil {
			// Send can fail once the server has already aborted the stream
			// (e.g. an earlier chunk tripped a cap); CloseAndRecv surfaces
			// the real reason.
			break
		}
	}
	return stream.CloseAndRecv()
}

// reportArtifact journals the manifest fact for an already-uploaded
// artifact (the produce path's second half — mcp_runner.go's reportHandler
// does both steps together; this test drives them explicitly).
func reportArtifact(t *testing.T, home *Home, artifactID string, data []byte, receipt *agentcoordpb.ArtifactReceipt) {
	t.Helper()
	sum := sha256.Sum256(data)
	err := home.Report(context.Background(), nil, []*agentcoordpb.ArtifactProduced{{
		ArtifactId: artifactID,
		Kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_OTHER,
		Name:       artifactID,
		MediaType:  "application/octet-stream",
		SizeBytes:  uint64(len(data)),
		Sha256:     sum[:],
		Content:    &agentcoordpb.ArtifactProduced_UploadId{UploadId: receipt.GetUploadId()},
	}})
	require.NoError(t, err)
}

func statusCode(err error) codes.Code {
	st, ok := status.FromError(err)
	if !ok {
		return codes.Unknown
	}
	return st.Code()
}

// TestArtifactTransfer_UploadDownloadRoundTrip is the motivating scenario's
// mechanism, hermetic: upload, journal the manifest, fetch it back to a
// fresh path — bytes and hash match exactly.
func TestArtifactTransfer_UploadDownloadRoundTrip(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("the plan: do the thing, then verify it")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/rollout", "rollout.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), receipt.GetUploadId())
	assert.Equal(t, sum[:], receipt.GetSha256())
	assert.EqualValues(t, len(data), receipt.GetSizeBytes())
	reportArtifact(t, child, "plan/rollout", data, receipt)

	dest := filepath.Join(t.TempDir(), "fetched.plan.md")
	owner := ownerHome(t, c)
	shaHex, size, err := owner.DownloadArtifact(context.Background(), out.Harp, "plan/rollout", dest)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), shaHex)
	assert.EqualValues(t, len(data), size)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, data, got, "downloaded bytes must match the uploaded content exactly")
}

// TestArtifactTransfer_ChunkingAcrossBoundary: an artifact bigger than the
// 1 MiB chunk size round-trips intact through Home's REAL chunking (not the
// test's raw helper) — proves multi-chunk reassembly on both the upload and
// download sides.
func TestArtifactTransfer_ChunkingAcrossBoundary(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := make([]byte, (1<<20)+(512*1024)+7) // 1.5 MiB + a few bytes: forces >=2 chunks, uneven boundary
	rand.New(rand.NewSource(42)).Read(data)
	sum := sha256.Sum256(data)

	receipt, err := child.UploadArtifact(context.Background(), "big/dataset", "dataset.bin", "application/octet-stream", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	assert.EqualValues(t, len(data), receipt.GetSizeBytes())
	reportArtifact(t, child, "big/dataset", data, receipt)

	dest := filepath.Join(t.TempDir(), "dataset.bin")
	owner := ownerHome(t, c)
	shaHex, size, err := owner.DownloadArtifact(context.Background(), out.Harp, "big/dataset", dest)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), shaHex)
	assert.EqualValues(t, len(data), size)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// TestArtifactTransfer_ReuploadIdempotent: re-uploading identical content is
// a free no-op at the store (E1a/E1b) — same upload_id, exactly one blob on
// disk regardless of how many times it lands.
func TestArtifactTransfer_ReuploadIdempotent(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("idempotent content")
	sum := sha256.Sum256(data)

	r1, err := child.UploadArtifact(context.Background(), "plan/rollout", "rollout.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	r2, err := child.UploadArtifact(context.Background(), "plan/rollout", "rollout.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)

	assert.Equal(t, r1.GetUploadId(), r2.GetUploadId())
	assert.Equal(t, r1.GetSha256(), r2.GetSha256())

	entries, err := os.ReadDir(filepath.Join(c.stateDir, artifactStoreDirName))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "content-addressed store must hold exactly one blob for identical content uploaded twice")
}

// TestArtifactTransfer_UploadHashMismatch_Rejected (E1e, upload side): the
// server hashes the received stream INDEPENDENTLY of the header's declared
// sha256 and rejects a mismatch with INVALID_ARGUMENT — a flipped byte never
// silently lands in the store under the wrong name.
func TestArtifactTransfer_UploadHashMismatch_Rejected(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	data := []byte("the real content that will actually be sent")
	wrongSHA := sha256.Sum256([]byte("a completely different claim"))

	_, err := uploadRaw(t, client, out.RunID, "plan/corrupt", data, wrongSHA[:], 0)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))

	// The mismatched content must never be stored under any name.
	entries, rerr := os.ReadDir(filepath.Join(c.stateDir, artifactStoreDirName))
	require.NoError(t, rerr)
	assert.Empty(t, entries, "a hash-mismatched upload must not be published to the store")
}

// TestArtifactTransfer_UploadFlippedChunkByte_Rejected: the corruption is IN
// a chunk (not just a bad claimed header) — same INVALID_ARGUMENT outcome,
// the concrete "flipped chunk byte" shape the acceptance names.
func TestArtifactTransfer_UploadFlippedChunkByte_Rejected(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	original := []byte("the plan file bytes, unflipped, as the header's sha256 claims them to be")
	sum := sha256.Sum256(original)
	corrupted := append([]byte(nil), original...)
	corrupted[10] ^= 0xFF // flip one byte after hashing the (correct) header claim

	_, err := uploadRaw(t, client, out.RunID, "plan/flip", corrupted, sum[:], 16)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, statusCode(err))
}

// TestArtifactTransfer_DownloadCorruptedStore_Caught (E1e, download side):
// even if a stored blob is corrupted AFTER a successful upload (disk bit
// rot, a bug), the DOWNLOAD side re-verifies against the manifest sha256
// BEFORE placing the file — the corrupted bytes must never land at
// dest_path.
func TestArtifactTransfer_DownloadCorruptedStore_Caught(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("bytes that will be corrupted on disk after a good upload")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/rot", "rot.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, child, "plan/rot", data, receipt)

	// Simulate store corruption directly on disk (bit rot / a storage bug) —
	// the file's NAME (its integrity claim) no longer matches its content.
	blobPath := filepath.Join(c.stateDir, artifactStoreDirName, receipt.GetUploadId())
	raw, err := os.ReadFile(blobPath)
	require.NoError(t, err)
	raw[0] ^= 0xFF
	require.NoError(t, os.WriteFile(blobPath, raw, 0o600))

	dest := filepath.Join(t.TempDir(), "rot.plan.md")
	owner := ownerHome(t, c)
	_, _, err = owner.DownloadArtifact(context.Background(), out.Harp, "plan/rot", dest)
	require.Error(t, err, "download must hard-fail against a corrupted store file")

	_, statErr := os.Stat(dest)
	assert.True(t, os.IsNotExist(statErr), "a hash-mismatched download must never place a file at dest_path")
}

// TestArtifactTransfer_UploadOwnershipDenied (E1a): a header's run_id must
// match the CALLING credential's own run — a child cannot upload claiming
// to be a different run, even one that exists.
func TestArtifactTransfer_UploadOwnershipDenied(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	victim := spawnResearcher(t, c)
	attacker := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, attacker.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	data := []byte("an upload impersonating another run's ownership")
	sum := sha256.Sum256(data)
	_, err := uploadRaw(t, client, victim.RunID, "plan/steal", data, sum[:], 0) // attacker's credential, victim's run_id
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, statusCode(err))
}

// TestArtifactTransfer_DownloadLineage: the PARENT (session owner) may fetch
// its child's artifact; an unrelated SIBLING child may not.
func TestArtifactTransfer_DownloadLineage(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	producer := spawnResearcher(t, c)
	sibling := spawnResearcher(t, c)
	producerHome := childHome(t, c, producer.RunID)
	siblingHome := childHome(t, c, sibling.RunID)

	data := []byte("only the parent should be able to fetch this")
	sum := sha256.Sum256(data)
	receipt, err := producerHome.UploadArtifact(context.Background(), "plan/private", "private.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, producerHome, "plan/private", data, receipt)

	// Parent (owner) succeeds.
	owner := ownerHome(t, c)
	dest := filepath.Join(t.TempDir(), "private.plan.md")
	_, _, err = owner.DownloadArtifact(context.Background(), producer.Harp, "plan/private", dest)
	require.NoError(t, err)

	// A sibling child (no lineage relationship to producer) is denied.
	dest2 := filepath.Join(t.TempDir(), "stolen.plan.md")
	_, _, err = siblingHome.DownloadArtifact(context.Background(), producer.Harp, "plan/private", dest2)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, statusCode(err))
	_, statErr := os.Stat(dest2)
	assert.True(t, os.IsNotExist(statErr))
}

// TestArtifactTransfer_ConsumerReadOnly (E1a): a D1 consumer-class
// credential may DOWNLOAD (read-only, project-wide) but may NEVER UPLOAD
// (mutating) — the same read-only boundary ConsumerService already
// enforces, extended to ArtifactTransferService.
func TestArtifactTransfer_ConsumerReadOnly(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	data := []byte("consumers may read this, never write")
	sum := sha256.Sum256(data)
	receipt, err := child.UploadArtifact(context.Background(), "plan/rollout", "rollout.plan.md", "text/markdown", sum, int64(len(data)), bytes.NewReader(data))
	require.NoError(t, err)
	reportArtifact(t, child, "plan/rollout", data, receipt)

	consumerToken := c.consumerCreds.token()
	require.NotEmpty(t, consumerToken)
	consumerClient := dialArtifactClient(t, c, consumerToken)

	// Upload denied at the gRPC auth layer, before any handler logic runs.
	_, err = uploadRaw(t, consumerClient, "", "plan/hostile", []byte("nope"), sha256Sum([]byte("nope")), 0)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, statusCode(err))

	// Download succeeds: consumers are read-only, not blind.
	stream, err := consumerClient.DownloadArtifact(context.Background(), &agentcoordpb.ArtifactDownloadRequest{
		AgentId:    out.Harp,
		ArtifactId: "plan/rollout",
	})
	require.NoError(t, err)
	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetHeader())
	assert.Equal(t, sum[:], first.GetHeader().GetSha256())
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// TestUploadArtifact_RefusesAZeroByteArtifact pins the server half of
// U016-F18: the upload service capped the maximum size and never the
// minimum, so a 0-byte artifact uploaded, journaled, and returned a success
// receipt carrying a content-addressed id.
func TestUploadArtifact_RefusesAZeroByteArtifact(t *testing.T) {
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	home := childHome(t, c, out.RunID)

	_, err := home.UploadArtifact(context.Background(), "plan/empty", "empty.plan.md", "text/markdown",
		sha256.Sum256(nil), 0, bytes.NewReader(nil))
	require.Error(t, err, "a 0-byte artifact must not earn an upload receipt")
	assert.Contains(t, err.Error(), "empty artifact")
}

// TestUploadArtifact_RejectsSizeMismatch pins U019-F02's surviving slice: the
// zero-byte floor (U016-F18, above) only refuses a DECLARED size of 0. A
// client that declares a non-zero header.size_bytes but then delivers fewer
// (here, zero) actual chunk bytes sails past both the cap and the floor
// checks — writeAtomic happily hashes an empty reader and the handler used
// to return an OK receipt claiming SizeBytes: 0, silently contradicting the
// caller's own declared size. sha256 stays optional by design (the server's
// own hash is authoritative, never the uploader's claim — see
// artifactstore.go's writeAtomic doc), so the cross-check has to be on size.
func TestUploadArtifact_RejectsSizeMismatch(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	client := dialArtifactClient(t, c, env[EnvCoordCred])

	stream, err := client.UploadArtifact(context.Background())
	require.NoError(t, err)
	// Declare 1000 bytes, then send NO chunks at all before closing.
	sendErr := stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Header{Header: &agentcoordpb.ArtifactUploadHeader{
		RunId:      out.RunID,
		ArtifactId: "plan/short-delivery",
		Name:       "short-delivery",
		MediaType:  "text/markdown",
		SizeBytes:  1000,
		// sha256 deliberately omitted — optional by design; the size
		// cross-check must catch this on its own.
	}}})
	require.NoError(t, sendErr)

	_, err = stream.CloseAndRecv()
	require.Error(t, err, "a declared size of 1000 bytes with zero bytes actually delivered must not earn a success receipt")
	assert.Equal(t, codes.InvalidArgument, statusCode(err))

	entries, rerr := os.ReadDir(filepath.Join(c.stateDir, artifactStoreDirName))
	require.NoError(t, rerr)
	assert.Empty(t, entries, "a size-mismatched upload must not be published to the store")
}
