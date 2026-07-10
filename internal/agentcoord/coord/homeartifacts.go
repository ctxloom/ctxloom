package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
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
	client := agentcoordpb.NewArtifactTransferServiceClient(h.conn)
	stream, err := client.DownloadArtifact(ctx, &agentcoordpb.ArtifactDownloadRequest{
		AgentId:    agentID,
		ArtifactId: artifactID,
	})
	if err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	first, err := stream.Recv()
	if err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	header := first.GetHeader()
	if header == nil {
		return "", 0, fmt.Errorf("download %s/%s: server did not send a header frame first", agentID, artifactID)
	}

	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	tmp, err := os.CreateTemp(dir, ".ctxloom-fetch-*")
	if err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // no-op once renamed away
	}()

	h256 := sha256.New()
	var n int64
	for {
		frame, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, rerr)
		}
		chunk := frame.GetChunk()
		if chunk == nil {
			return "", 0, fmt.Errorf("download %s/%s: unexpected non-chunk frame after the header", agentID, artifactID)
		}
		data := chunk.GetData()
		if _, werr := tmp.Write(data); werr != nil {
			return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, werr)
		}
		h256.Write(data)
		n += int64(len(data))
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("download %s/%s: %w", agentID, artifactID, err)
	}

	sum := h256.Sum(nil)
	if !bytes.Equal(sum, header.GetSha256()) {
		// E1e: refuse to place a file that does not match its own manifest —
		// the temp file is cleaned up by the deferred Remove above, destPath
		// is never touched.
		return "", 0, fmt.Errorf("download %s/%s: content does not match the manifest sha256 (store corruption?) — refusing to place %s", agentID, artifactID, destPath)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", 0, fmt.Errorf("download %s/%s: place %s: %w", agentID, artifactID, destPath, err)
	}
	return hex.EncodeToString(sum), n, nil
}
