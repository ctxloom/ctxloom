package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// E1 — the RUNNER side of artifact transfer: Home dials
// ArtifactTransferService on the SAME credentialed connection it already
// holds for RunnerChannel/RunChannel (h.conn), so no separate dial or
// credential plumbing is needed. UploadArtifact backs the produce path
// (mcp_runner.go's reportHandler/planStamper); DownloadArtifact backs the
// consume path (mcp_runner.go's fetchArtifactHandler, RouteArtifactFetch).

// artifactUploadChunkSize matches the coordinator's own cap
// (coord/artifacts.go's artifactChunkCap) by CONTRACT (documented in
// artifacts.proto), not by Go-level sharing — this file is the client role,
// artifacts.go is the server role; a single process may play both only in
// tests.
const artifactUploadChunkSize = 1 << 20

// UploadArtifact streams r's bytes to the coordinator's content-addressed
// store in <= 1 MiB chunks and returns the server-verified receipt. sha256Sum
// is the runner's OWN claim from its local read of the source (e.g. the plan
// file already hashed for dedup purposes) — the coordinator hashes the
// stream independently and rejects a mismatch (E1e); this value is never
// trusted by itself.
func (h *Home) UploadArtifact(ctx context.Context, artifactID, name, mediaType string, sha256Sum [32]byte, size int64, r io.Reader) (*agentcoordpb.ArtifactReceipt, error) {
	client := agentcoordpb.NewArtifactTransferServiceClient(h.conn)
	stream, err := client.UploadArtifact(ctx)
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", artifactID, err)
	}
	if err := stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Header{Header: &agentcoordpb.ArtifactUploadHeader{
		RunId:      h.cfg.RunID,
		ArtifactId: artifactID,
		Name:       name,
		MediaType:  mediaType,
		SizeBytes:  uint64(size),
		Sha256:     sha256Sum[:],
	}}}); err != nil {
		return nil, fmt.Errorf("upload %s: send header: %w", artifactID, err)
	}

	buf := make([]byte, artifactUploadChunkSize)
	var offset uint64
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentcoordpb.ArtifactUploadRequest{Kind: &agentcoordpb.ArtifactUploadRequest_Chunk{Chunk: &agentcoordpb.ArtifactChunk{
				Offset: offset,
				Data:   append([]byte(nil), buf[:n]...),
			}}}); serr != nil {
				return nil, fmt.Errorf("upload %s: send chunk at offset %d: %w", artifactID, offset, serr)
			}
			offset += uint64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("upload %s: read source: %w", artifactID, rerr)
		}
	}
	receipt, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", artifactID, err)
	}
	return receipt, nil
}

// DownloadArtifact fetches (agentID, artifactID)'s bytes and places them at
// the ALREADY-RESOLVED, already-safety-checked absolute destPath (the
// caller — mcp_runner.go's fetchArtifactHandler — owns the cwd-safe
// placement policy; this method is pure mechanism). The manifest header
// arrives FIRST; content is written to a same-directory temp file, hashed
// as it streams, and the receiver recomputes sha256 against the header
// BEFORE placing the file — a mismatch is a hard failure, the temp file is
// discarded, and destPath is never touched (E1e).
func (h *Home) DownloadArtifact(ctx context.Context, agentID, artifactID, destPath string) (shaHex string, size int64, err error) {
	stream, header, err := h.openDownload(ctx, agentID, artifactID)
	if err == nil {
		shaHex, size, err = placeVerified(stream, header.GetSha256(), destPath)
	}
	if err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	return shaHex, size, nil
}

// openDownload starts the download stream and reads its header frame, which
// the protocol sends exactly once, first.
func (h *Home) openDownload(ctx context.Context, agentID, artifactID string) (grpc.ServerStreamingClient[agentcoordpb.ArtifactDownloadFrame], *agentcoordpb.ArtifactDownloadHeader, error) {
	client := agentcoordpb.NewArtifactTransferServiceClient(h.conn)
	stream, err := client.DownloadArtifact(ctx, &agentcoordpb.ArtifactDownloadRequest{
		AgentId:    agentID,
		ArtifactId: artifactID,
	})
	if err != nil {
		return nil, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	header := first.GetHeader()
	if header == nil {
		return nil, nil, errors.New("server did not send a header frame first")
	}
	return stream, header, nil
}

// placeVerified streams the remaining chunks into a same-directory temp file
// and renames it onto destPath only once the content is non-empty and hashes
// to want. On any refusal the deferred Remove discards the temp file and
// destPath is never touched.
func placeVerified(stream grpc.ServerStreamingClient[agentcoordpb.ArtifactDownloadFrame], want []byte, destPath string) (string, int64, error) {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".ctxloom-fetch-*")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // no-op once renamed away
	}()

	sum, n, err := receiveChunks(stream, tmp)
	if err != nil {
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}

	// The upload side now refuses a declared size of 0
	// (coord/artifacts.go's UploadArtifact), so no NEW empty artifact can
	// be published — but this is the belt half of belt-and-suspenders
	// against a blob that predates that fix (an existing project's
	// coordinator state) or a store manipulated out of band. A 0-byte
	// "artifact" is a receipt for nothing; refuse it here too rather than
	// writing an empty file to destPath and reporting success.
	if n == 0 {
		return "", 0, fmt.Errorf("the stored artifact is 0 bytes — refusing to place an empty file at %s", destPath)
	}
	// E1e: refuse to place a file that does not match its own manifest.
	if !bytes.Equal(sum, want) {
		return "", 0, fmt.Errorf("content does not match the manifest sha256 (store corruption?) — refusing to place %s", destPath)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", 0, fmt.Errorf("place %s: %w", destPath, err)
	}
	return hex.EncodeToString(sum), n, nil
}

// receiveChunks writes every chunk frame to w until the stream ends,
// returning the sha256 and length of what was written. Any frame other than
// a chunk is a protocol violation once the header has been read.
func receiveChunks(stream grpc.ServerStreamingClient[agentcoordpb.ArtifactDownloadFrame], w io.Writer) ([]byte, int64, error) {
	h256 := sha256.New()
	var n int64
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return h256.Sum(nil), n, nil
		}
		if err != nil {
			return nil, 0, err
		}
		chunk := frame.GetChunk()
		if chunk == nil {
			return nil, 0, errors.New("unexpected non-chunk frame after the header")
		}
		data := chunk.GetData()
		if _, err := w.Write(data); err != nil {
			return nil, 0, err
		}
		h256.Write(data)
		n += int64(len(data))
	}
}
