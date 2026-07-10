package coord

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"

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

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetHeader()
	if header == nil {
		return status.Error(codes.InvalidArgument, "upload: first ArtifactUploadRequest must be header")
	}
	if header.GetArtifactId() == "" {
		return status.Error(codes.InvalidArgument, "upload: artifact_id is required")
	}
	if err := authorizeArtifactUpload(id, header.GetRunId()); err != nil {
		return err
	}
	if header.GetSizeBytes() > artifactUploadSizeCap {
		return status.Errorf(codes.InvalidArgument, "upload: declared size %d exceeds the %d-byte cap", header.GetSizeBytes(), artifactUploadSizeCap)
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

	shaHex, size, werr := c.artifacts.writeAtomic(pr, header.GetSha256())
	_ = pr.Close()
	if cerr := <-chunkErrCh; cerr != nil {
		if se, ok := status.FromError(cerr); ok {
			return se.Err()
		}
		return status.Errorf(codes.Internal, "upload: %v", cerr)
	}
	if werr != nil {
		if errors.Is(werr, errArtifactSHAMismatch) {
			return status.Errorf(codes.InvalidArgument, "upload: received content (sha256 %s) does not match the declared sha256", shaHex)
		}
		return status.Errorf(codes.Internal, "upload: %v", werr)
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
	ownerHarp := req.GetAgentId()
	if ownerHarp == "" {
		return status.Error(codes.InvalidArgument, "download: agent_id is required")
	}
	if req.GetArtifactId() == "" {
		return status.Error(codes.InvalidArgument, "download: artifact_id is required")
	}
	if err := c.authorizeArtifactDownload(id, ownerHarp); err != nil {
		return err
	}
	rec, ok := c.artifactRecord(ownerHarp, req.GetArtifactId())
	if !ok {
		return status.Errorf(codes.NotFound, "download: no artifact %q for %q", req.GetArtifactId(), ownerHarp)
	}
	f, err := c.artifacts.open(rec.SHA256)
	if err != nil {
		return status.Errorf(codes.NotFound, "download: stored content missing for %q: %v", req.GetArtifactId(), err)
	}
	defer func() { _ = f.Close() }()

	shaBytes, err := hex.DecodeString(rec.SHA256)
	if err != nil {
		return status.Errorf(codes.Internal, "download: corrupt manifest sha256 for %q: %v", req.GetArtifactId(), err)
	}
	kind := agentcoordpb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED
	if v, ok := agentcoordpb.ArtifactKind_value[rec.Kind]; ok {
		kind = agentcoordpb.ArtifactKind(v)
	}
	if err := stream.Send(&agentcoordpb.ArtifactDownloadFrame{Kind: &agentcoordpb.ArtifactDownloadFrame_Header{Header: &agentcoordpb.ArtifactDownloadHeader{
		ArtifactId: rec.ArtifactID,
		Revision:   rec.Revision,
		Kind:       kind,
		Name:       rec.Name,
		MediaType:  rec.MediaType,
		SizeBytes:  rec.SizeBytes,
		Sha256:     shaBytes,
	}}}); err != nil {
		return err
	}

	offset := req.GetOffset()
	if offset > 0 {
		if _, serr := f.Seek(int64(offset), io.SeekStart); serr != nil {
			return status.Errorf(codes.InvalidArgument, "download: seek to offset %d: %v", offset, serr)
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
