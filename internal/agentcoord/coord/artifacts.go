package coord

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// E1 — artifact transfer (agentcoord.v1.ArtifactTransferService,
// artifacts.proto). Server-side upload/download; the content-addressed store
// itself lives in artifactstore.go. Both RPCs re-derive identity from the
// connection credential per call (never a cached principal — the same
// discipline grpcServer's auth interceptor documents for RunnerChannel/
// RunChannel).

// artifactChunkCap bounds one upload/download chunk (E1a: "each <= 1 MiB"),
// well under the 4 MiB default gRPC frame cap.
const artifactChunkCap = 1 << 20

// artifactUploadSizeCap bounds one artifact's total size — "runner-read,
// size-capped sanely" (E1c's generic publish case). 64 MiB comfortably
// covers plan/report/dataset artifacts without risking runaway memory/disk
// from a misbehaving caller; revisit if a real consumer needs more.
const artifactUploadSizeCap = 64 << 20

// artifactService implements agentcoord.v1.ArtifactTransferService.
type artifactService struct {
	agentcoordpb.UnimplementedArtifactTransferServiceServer
	c *Coordinator
}

// authorizeArtifactUpload: a caller may only upload bytes for ITS OWN
// current run (or, for a session-owner/depth-0 caller with no run_id, an
// empty header.run_id) — never another identity's. header.run_id is a
// CLAIM asserted against the credential-derived caller.RunID, never trusted
// by itself (A1's identity discipline).
func authorizeArtifactUpload(caller Identity, headerRunID string) error {
	if caller.Consumer {
		return status.Error(codes.PermissionDenied, "upload: a read-only consumer credential cannot upload")
	}
	if caller.RunID != headerRunID {
		return status.Errorf(codes.PermissionDenied, "upload: run_id %q does not match this connection's credential", headerRunID)
	}
	return nil
}

// authorizeArtifactDownload mirrors serveStopRun's ownership pattern
// (runchannel.go): the record's own harp (fetch your own artifact), its
// DIRECT PARENT (lineage — a parent fetching its child's), or any
// consumer-class credential (read-only, project-wide, matching
// ConsumerService's existing trust boundary).
func (c *Coordinator) authorizeArtifactDownload(caller Identity, ownerHarp string) error {
	if caller.Consumer || caller.Harp == ownerHarp {
		return nil
	}
	allowed := false
	c.runs.View(func() {
		if r := c.runsF.currentRun(ownerHarp); r != nil && r.ParentHarp == caller.Harp {
			allowed = true
		}
	})
	if allowed {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "download: %q is not this session, its child, or a consumer credential", ownerHarp)
}

// recvUploadHeader reads the stream's mandatory first frame and applies
// every check that can be made before a single content byte is accepted:
// shape, the required artifact_id, the caller's ownership of the run it
// claims, and the declared-size cap and floor.
func recvUploadHeader(stream grpc.ClientStreamingServer[agentcoordpb.ArtifactUploadRequest, agentcoordpb.ArtifactReceipt], caller Identity) (*agentcoordpb.ArtifactUploadHeader, error) {
	first, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	header := first.GetHeader()
	if header == nil {
		return nil, status.Error(codes.InvalidArgument, "upload: first ArtifactUploadRequest must be header")
	}
	if header.GetArtifactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "upload: artifact_id is required")
	}
	if err := authorizeArtifactUpload(caller, header.GetRunId()); err != nil {
		return nil, err
	}
	if header.GetSizeBytes() > artifactUploadSizeCap {
		return nil, status.Errorf(codes.InvalidArgument, "upload: declared size %d exceeds the %d-byte cap", header.GetSizeBytes(), artifactUploadSizeCap)
	}
	// A FLOOR as well as a cap (U016-F18). Only the maximum was ever checked,
	// so a 0-byte artifact uploaded, journaled, and returned a success
	// receipt with a content-addressed id — a receipt for nothing. The runner
	// refuses first (mcp_runner.go's artifactStamper.publish); this is the
	// server's own guard, because the transfer service is a credentialed
	// surface any runner reaches, not just ours.
	if header.GetSizeBytes() == 0 {
		return nil, status.Error(codes.InvalidArgument, "upload: declared size is 0 — an empty artifact is a receipt for nothing, refusing it")
	}
	return header, nil
}

// uploadFailure resolves the two independent failure reports one upload can
// produce — the chunk goroutine's and the store's — into the ONE status the
// caller is told, or nil when the upload stands.
//
// Which is the CAUSE: a chunk-shape or transport error closed the pipe with
// ITSELF as the reason, so the store's failure is downstream of it and the
// chunk error is authoritative. But the handler's own pr.Close() ALSO makes
// the goroutine observe io.ErrClosedPipe, and that one is this handler
// unwinding a failed store write — never a cause. Preferring it
// unconditionally answered a full disk with "upload: io: read/write on
// closed pipe".
func uploadFailure(cerr, werr error, header *agentcoordpb.ArtifactUploadHeader, shaHex string) error {
	if cerr != nil && (werr == nil || !errors.Is(cerr, io.ErrClosedPipe)) {
		if se, ok := status.FromError(cerr); ok {
			return se.Err()
		}
		return status.Errorf(codes.Internal, "upload: %v", cerr)
	}
	switch {
	case werr == nil:
		return nil
	case errors.Is(werr, errArtifactSHAMismatch):
		return status.Errorf(codes.InvalidArgument, "upload: received content (sha256 %s) does not match the declared sha256", shaHex)
	case errors.Is(werr, errArtifactSizeMismatch):
		// U019-F02: the declared-size floor only catches a DECLARED size of
		// 0. A client that declares a non-zero size_bytes but delivers fewer
		// bytes (in the limit, none) sails past both the cap and the floor —
		// sha256 is optional by design (writeAtomic's own hash is
		// authoritative), so this is the only thing standing between a
		// truncated delivery and a receipt that silently contradicts the
		// caller's own declared size. Checked inside writeAtomic, before
		// publish, so a mismatched upload never earns a name in the
		// content-addressed store.
		return status.Errorf(codes.InvalidArgument, "upload: declared size %d does not match the bytes actually received", header.GetSizeBytes())
	default:
		return status.Errorf(codes.Internal, "upload: %v", werr)
	}
}

// UploadArtifact receives header+chunks (offset-contiguous, <= 1 MiB each),
// hashes the stream independently of the header's declared sha256, and
// rejects a mismatch with INVALID_ARGUMENT (E1e) — the coordinator's own
// hash is authoritative. A chunk-shape violation (out-of-order offset, an
// oversized chunk, a chunk before the header) is also INVALID_ARGUMENT.
func (s *artifactService) UploadArtifact(stream grpc.ClientStreamingServer[agentcoordpb.ArtifactUploadRequest, agentcoordpb.ArtifactReceipt]) error {
	c := s.c
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}

	header, err := recvUploadHeader(stream, id)
	if err != nil {
		return err
	}

	// Bridge the push-based Recv loop onto an io.Reader writeAtomic can
	// drain: io.Pipe is synchronous (unbuffered), so chunk arrival and the
	// store write stay backpressured to each other — no unbounded buffering
	// of a large or slow upload.
	pr, pw := io.Pipe()
	chunkErrCh := make(chan error, 1)
	go func() {
		var wantOffset, total uint64
		for {
			req, rerr := stream.Recv()
			if rerr == io.EOF {
				_ = pw.Close()
				chunkErrCh <- nil
				return
			}
			if rerr != nil {
				_ = pw.CloseWithError(rerr)
				chunkErrCh <- rerr
				return
			}
			chunk := req.GetChunk()
			if chunk == nil {
				cerr := status.Error(codes.InvalidArgument, "upload: expected a chunk after the header")
				_ = pw.CloseWithError(cerr)
				chunkErrCh <- cerr
				return
			}
			if chunk.GetOffset() != wantOffset {
				cerr := status.Errorf(codes.InvalidArgument, "upload: out-of-order chunk (want offset %d, got %d)", wantOffset, chunk.GetOffset())
				_ = pw.CloseWithError(cerr)
				chunkErrCh <- cerr
				return
			}
			data := chunk.GetData()
			if len(data) > artifactChunkCap {
				cerr := status.Errorf(codes.InvalidArgument, "upload: chunk of %d bytes exceeds the %d-byte cap", len(data), artifactChunkCap)
				_ = pw.CloseWithError(cerr)
				chunkErrCh <- cerr
				return
			}
			total += uint64(len(data))
			if total > artifactUploadSizeCap {
				cerr := status.Errorf(codes.InvalidArgument, "upload: total size exceeds the %d-byte cap", artifactUploadSizeCap)
				_ = pw.CloseWithError(cerr)
				chunkErrCh <- cerr
				return
			}
			if len(data) > 0 {
				if _, werr := pw.Write(data); werr != nil {
					chunkErrCh <- werr
					return
				}
			}
			wantOffset += uint64(len(data))
		}
	}()

	shaHex, size, werr := c.artifacts.writeAtomic(pr, header.GetSha256(), header.GetSizeBytes())
	_ = pr.Close()
	if err := uploadFailure(<-chunkErrCh, werr, header, shaHex); err != nil {
		return err
	}

	c.audit("artifact.uploaded", id.Harp, map[string]string{
		"run_id":      header.GetRunId(),
		"artifact_id": header.GetArtifactId(),
		"sha256":      shaHex,
		"size_bytes":  fmt.Sprint(size),
	})

	shaBytes, _ := hex.DecodeString(shaHex) // shaHex is our own hex.EncodeToString output
	return stream.SendAndClose(&agentcoordpb.ArtifactReceipt{
		UploadId:  shaHex,
		Sha256:    shaBytes,
		SizeBytes: uint64(size),
		StoredAt:  timestamppb.New(c.now()),
	})
}

// DownloadArtifact resolves (agent_id, artifact_id) against the reports
// fold's latest-revision manifest, sends its header FIRST (the receiver
// verifies against it BEFORE placing any bytes — E1e), then streams the
// stored content in <= 1 MiB chunks from req.offset.
func (s *artifactService) DownloadArtifact(req *agentcoordpb.ArtifactDownloadRequest, stream grpc.ServerStreamingServer[agentcoordpb.ArtifactDownloadFrame]) error {
	c := s.c
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	rec, err := c.resolveDownload(id, req)
	if err != nil {
		return err
	}
	f, shaBytes, err := c.openDownloadBlob(rec, req.GetArtifactId())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := stream.Send(downloadHeaderFrame(rec, shaBytes)); err != nil {
		return err
	}
	return streamArtifactBody(f, req.GetOffset(), stream)
}

// resolveDownload applies every check that stands between a download request
// and the manifest it names: both identifiers present, the caller entitled to
// the owner's artifacts, and a record actually existing.
func (c *Coordinator) resolveDownload(caller Identity, req *agentcoordpb.ArtifactDownloadRequest) (ArtifactRecord, error) {
	ownerHarp := req.GetAgentId()
	if ownerHarp == "" {
		return ArtifactRecord{}, status.Error(codes.InvalidArgument, "download: agent_id is required")
	}
	if req.GetArtifactId() == "" {
		return ArtifactRecord{}, status.Error(codes.InvalidArgument, "download: artifact_id is required")
	}
	if err := c.authorizeArtifactDownload(caller, ownerHarp); err != nil {
		return ArtifactRecord{}, err
	}
	rec, ok := c.artifactRecord(ownerHarp, req.GetArtifactId())
	if !ok {
		return ArtifactRecord{}, status.Errorf(codes.NotFound, "download: no artifact %q for %q", req.GetArtifactId(), ownerHarp)
	}
	// An unsatisfiable resume range is refused HERE rather than at the seek:
	// seeking past EOF succeeds, so the request would otherwise be answered
	// with a header describing the whole artifact and no chunks at all — a
	// success carrying zero bytes, which a receiver cannot distinguish from
	// an empty artifact. offset == size is unsatisfiable too: it names the
	// byte after the last one.
	if off := req.GetOffset(); off > 0 && off >= rec.SizeBytes {
		return ArtifactRecord{}, status.Errorf(codes.InvalidArgument,
			"download: offset %d is past the end of %q (%d bytes)", off, req.GetArtifactId(), rec.SizeBytes)
	}
	return rec, nil
}

// openDownloadBlob opens the record's stored content and returns the raw
// sha256 the header will carry.
//
// The manifest's own sha is validated BEFORE it is used as a file name: a
// name that is not a content hash is a corrupt manifest, and answering that
// with "stored content missing" would send the reader looking for a blob
// rather than at the record that named it.
func (c *Coordinator) openDownloadBlob(rec ArtifactRecord, artifactID string) (*os.File, []byte, error) {
	shaBytes, err := hex.DecodeString(rec.SHA256)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "download: corrupt manifest sha256 for %q: %v", artifactID, err)
	}
	f, err := c.artifacts.open(rec.SHA256)
	if err != nil {
		if errors.Is(err, errArtifactBadName) {
			return nil, nil, status.Errorf(codes.Internal, "download: corrupt manifest sha256 for %q: %v", artifactID, err)
		}
		return nil, nil, status.Errorf(codes.NotFound, "download: stored content missing for %q: %v", artifactID, err)
	}
	return f, shaBytes, nil
}

// downloadHeaderFrame projects a manifest record onto the header frame the
// receiver verifies the streamed bytes against. A kind name the wire enum
// does not know degrades to UNSPECIFIED rather than failing the transfer.
func downloadHeaderFrame(rec ArtifactRecord, shaBytes []byte) *agentcoordpb.ArtifactDownloadFrame {
	kind := agentcoordpb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED
	if v, ok := agentcoordpb.ArtifactKind_value[rec.Kind]; ok {
		kind = agentcoordpb.ArtifactKind(v)
	}
	return &agentcoordpb.ArtifactDownloadFrame{Kind: &agentcoordpb.ArtifactDownloadFrame_Header{Header: &agentcoordpb.ArtifactDownloadHeader{
		ArtifactId: rec.ArtifactID,
		Revision:   rec.Revision,
		Kind:       kind,
		Name:       rec.Name,
		MediaType:  rec.MediaType,
		SizeBytes:  rec.SizeBytes,
		Sha256:     shaBytes,
	}}}
}

// streamArtifactBody sends f's content from offset in <= 1 MiB chunks, each
// stamped with its absolute offset so a receiver can place bytes without
// tracking its own cursor.
func streamArtifactBody(f *os.File, offset uint64, stream grpc.ServerStreamingServer[agentcoordpb.ArtifactDownloadFrame]) error {
	if offset > 0 {
		if serr := seekToOffset(f, offset); serr != nil {
			return serr
		}
	}
	buf := make([]byte, artifactChunkCap)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentcoordpb.ArtifactDownloadFrame{Kind: &agentcoordpb.ArtifactDownloadFrame_Chunk{Chunk: &agentcoordpb.ArtifactChunk{
				Offset: offset,
				Data:   append([]byte(nil), buf[:n]...),
			}}}); serr != nil {
				return serr
			}
			offset += uint64(n)
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return status.Errorf(codes.Internal, "download: read stored content: %v", rerr)
		}
	}
}

func seekToOffset(f *os.File, offset uint64) error {
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return status.Errorf(codes.InvalidArgument, "download: seek to offset %d: %v", offset, err)
	}
	return nil
}
