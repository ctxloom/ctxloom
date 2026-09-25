package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// downloadServer serves one scripted DownloadArtifact stream: it fails the
// call with callErr, or sends frames in order and ends the stream.
type downloadServer struct {
	agentcoordpb.UnimplementedArtifactTransferServiceServer
	frames  []*agentcoordpb.ArtifactDownloadFrame
	callErr error
}

func (d *downloadServer) DownloadArtifact(_ *agentcoordpb.ArtifactDownloadRequest, stream grpc.ServerStreamingServer[agentcoordpb.ArtifactDownloadFrame]) error {
	if d.callErr != nil {
		return d.callErr
	}
	for _, f := range d.frames {
		if err := stream.Send(f); err != nil {
			return err
		}
	}
	return nil
}

// downloadHome is a Home dialled at a loopback server carrying d, with the
// owner-loss coordinator answering its RunnerChannel so the link stays quiet.
func downloadHome(t *testing.T, d *downloadServer) *Home {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	agentcoordpb.RegisterCoordinatorServiceServer(srv, &ownerServer{})
	agentcoordpb.RegisterArtifactTransferServiceServer(srv, d)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ownerLossHome(t, fmt.Sprintf("http://%s/mcp", ln.Addr().String()), time.Hour)
}

func headerFrame(sum []byte) *agentcoordpb.ArtifactDownloadFrame {
	return &agentcoordpb.ArtifactDownloadFrame{Kind: &agentcoordpb.ArtifactDownloadFrame_Header{
		Header: &agentcoordpb.ArtifactDownloadHeader{Sha256: sum},
	}}
}

func chunkFrame(data string) *agentcoordpb.ArtifactDownloadFrame {
	return &agentcoordpb.ArtifactDownloadFrame{Kind: &agentcoordpb.ArtifactDownloadFrame_Chunk{
		Chunk: &agentcoordpb.ArtifactChunk{Data: []byte(data)},
	}}
}

// dirEntries lists dir's names, or nil when it does not exist.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// TestHome_DownloadArtifact_PlacesVerifiedContent: header first, then the
// chunks in order; the bytes land at destPath (its directory created), the
// returned hash and size describe them, and no temp file is left beside it.
func TestHome_DownloadArtifact_PlacesVerifiedContent(t *testing.T) {
	sum := sha256.Sum256([]byte("hello world"))
	h := downloadHome(t, &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{
		headerFrame(sum[:]), chunkFrame("hello "), chunkFrame("world"),
	}})
	dest := filepath.Join(t.TempDir(), "nested", "out.txt")

	shaHex, size, err := h.DownloadArtifact(context.Background(), "agent-a", "art-1", dest)
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(sum[:]), shaHex)
	require.Equal(t, int64(len("hello world")), size)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(got))
	require.Equal(t, []string{"out.txt"}, dirEntries(t, filepath.Dir(dest)))
}

// TestHome_DownloadArtifact_RefusalsNeverTouchDest pins each refusal: the
// error names the artifact and the reason, destPath is never created, and no
// temp file survives.
func TestHome_DownloadArtifact_RefusalsNeverTouchDest(t *testing.T) {
	good := sha256.Sum256([]byte("abc"))
	cases := []struct {
		name    string
		srv     *downloadServer
		wantErr string
	}{
		{
			name:    "call fails",
			srv:     &downloadServer{callErr: status.Error(codes.NotFound, "no such artifact")},
			wantErr: "download agent-a/art-1: rpc error: code = NotFound desc = no such artifact",
		},
		{
			name:    "stream ends before any frame",
			srv:     &downloadServer{},
			wantErr: "download agent-a/art-1: EOF",
		},
		{
			name:    "first frame is not a header",
			srv:     &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{chunkFrame("abc")}},
			wantErr: "download agent-a/art-1: server did not send a header frame first",
		},
		{
			name: "non-chunk frame after the header",
			srv: &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{
				headerFrame(good[:]), chunkFrame("abc"), headerFrame(good[:]),
			}},
			wantErr: "download agent-a/art-1: unexpected non-chunk frame after the header",
		},
		{
			name:    "zero bytes",
			srv:     &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{headerFrame(good[:])}},
			wantErr: "download agent-a/art-1: the stored artifact is 0 bytes — refusing to place an empty file at ",
		},
		{
			name: "content does not match the manifest",
			srv: &downloadServer{frames: []*agentcoordpb.ArtifactDownloadFrame{
				headerFrame(good[:]), chunkFrame("abd"),
			}},
			wantErr: "download agent-a/art-1: content does not match the manifest sha256 (store corruption?) — refusing to place ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := downloadHome(t, tc.srv)
			dir := filepath.Join(t.TempDir(), "d")
			dest := filepath.Join(dir, "out.txt")

			shaHex, size, err := h.DownloadArtifact(context.Background(), "agent-a", "art-1", dest)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
			require.Empty(t, shaHex)
			require.Zero(t, size)
			require.NoFileExists(t, dest)
			require.Empty(t, dirEntries(t, dir), "no temp file may survive a refusal")
		})
	}
}
