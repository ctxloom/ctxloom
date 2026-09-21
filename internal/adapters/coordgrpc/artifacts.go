package coordgrpc

import (
	"encoding/hex"
	"errors"
	"io"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// artifactService implements agentcoord.v1.ArtifactTransferService: the
// chunked upload and download streams over Coordinator.ReceiveArtifact and
// Coordinator.OpenArtifact. Both RPCs re-derive identity from the
// connection credential per call (never a cached principal — the same
// discipline grpcServer's auth interceptor documents for RunnerChannel/
// RunChannel).
type artifactService struct {
	agentcoordpb.UnimplementedArtifactTransferServiceServer
	c *coord.Coordinator
}

// artifactStatus maps a refusal onto the transfer RPCs' codes: the
// coordinator's own error table, with a chunk-stream fault (already a status)
// passed through.
func artifactStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, coord.ErrArtifactSHAMismatch), errors.Is(err, coord.ErrArtifactSizeMismatch), errors.Is(err, coord.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, coord.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, coord.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

func recvUploadHeader(stream grpc.ClientStreamingServer[agentcoordpb.ArtifactUploadRequest, agentcoordpb.ArtifactReceipt]) (coord.ArtifactUpload, error) {
	first, err := stream.Recv()
	if err != nil {
		return coord.ArtifactUpload{}, err
	}
	header := first.GetHeader()
	if header == nil {
		return coord.ArtifactUpload{}, status.Error(codes.InvalidArgument, "upload: first ArtifactUploadRequest must be header")
	}
	return coord.ArtifactUpload{
		RunID:      header.GetRunId(),
		ArtifactID: header.GetArtifactId(),
		Name:       header.GetName(),
		MediaType:  header.GetMediaType(),
		SizeBytes:  header.GetSizeBytes(),
		SHA256:     header.GetSha256(),
	}, nil
}

// uploadFailure decides which of the two failures to report: the chunk
// stream's (the caller's fault, or the transport's) wins over a store
// refusal that was only ever a consequence of it, except when the store's
// verdict is the one that closed the pipe.
func uploadFailure(cerr, werr error) error {
	if cerr != nil && (werr == nil || !errors.Is(cerr, io.ErrClosedPipe)) {
		if se, ok := status.FromError(cerr); ok {
			return se.Err()
		}
		return status.Errorf(codes.Internal, "upload: %v", cerr)
	}
	return artifactStatus(werr)
}

// UploadArtifact receives the header, then streams the chunks into the
// coordinator's store through a pipe; the receipt carries the digest the
// store computed.
func (s *artifactService) UploadArtifact(stream grpc.ClientStreamingServer[agentcoordpb.ArtifactUploadRequest, agentcoordpb.ArtifactReceipt]) error {
	c := s.c
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}

	header, err := recvUploadHeader(stream)
	if err != nil {
		return err
	}

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
			if len(data) > coord.ArtifactChunkCap {
				cerr := status.Errorf(codes.InvalidArgument, "upload: chunk of %d bytes exceeds the %d-byte cap", len(data), coord.ArtifactChunkCap)
				_ = pw.CloseWithError(cerr)
				chunkErrCh <- cerr
				return
			}
			total += uint64(len(data))
			if total > coord.ArtifactUploadSizeCap {
				cerr := status.Errorf(codes.InvalidArgument, "upload: total size exceeds the %d-byte cap", coord.ArtifactUploadSizeCap)
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

	receipt, werr := c.ReceiveArtifact(id, header, pr)
	_ = pr.Close()
	if err := uploadFailure(<-chunkErrCh, werr); err != nil {
		return err
	}
	return stream.SendAndClose(&agentcoordpb.ArtifactReceipt{
		UploadId:  receipt.UploadID,
		Sha256:    receipt.SHA256,
		SizeBytes: receipt.SizeBytes,
		StoredAt:  timestamppb.New(receipt.StoredAt),
	})
}

// DownloadArtifact streams one stored artifact: the manifest header first,
// then the bytes from the requested offset in chunks.
func (s *artifactService) DownloadArtifact(req *agentcoordpb.ArtifactDownloadRequest, stream grpc.ServerStreamingServer[agentcoordpb.ArtifactDownloadFrame]) error {
	c := s.c
	id, ok := c.Identify(mdToken(stream.Context()))
	if !ok {
		return status.Error(codes.Unauthenticated, "unknown or revoked credential")
	}
	rec, f, err := c.OpenArtifact(id, req.GetAgentId(), req.GetArtifactId(), req.GetOffset())
	if err != nil {
		return artifactStatus(err)
	}
	defer func() { _ = f.Close() }()

	shaBytes, _ := hex.DecodeString(rec.SHA256) // OpenArtifact refused a manifest whose digest does not decode
	if err := stream.Send(downloadHeaderFrame(rec, shaBytes)); err != nil {
		return err
	}
	return streamArtifactBody(f, req.GetOffset(), stream)
}

func downloadHeaderFrame(rec coord.ArtifactRecord, shaBytes []byte) *agentcoordpb.ArtifactDownloadFrame {
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

func streamArtifactBody(f *os.File, offset uint64, stream grpc.ServerStreamingServer[agentcoordpb.ArtifactDownloadFrame]) error {
	buf := make([]byte, coord.ArtifactChunkCap)
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
