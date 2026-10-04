//go:build !windows

package claude

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// listenEndpoint listens on a unix socket, which is what dialMessaging dials
// here, closed at cleanup. The dir is a short one under the default temp
// root: darwin caps a socket path at 104 bytes, which a t.TempDir() path can
// exceed.
func listenEndpoint(t *testing.T) (string, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return path, ln
}

// Here claude admits a self-sent post by process ancestry, so a bind without
// a token is not refused and the post carries no auth line.
func TestMessagingWake_FireWithoutATokenPostsOnlyTheWakeLine(t *testing.T) {
	path, got := listenMessaging(t)
	w, err := messagingWake{}.Bind(context.Background(), envOf(map[string]string{envMessagingSocket: path}))
	require.NoError(t, err)

	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	lines := testsupport.Await(t, 10*time.Second, got, "nothing was posted")
	require.Len(t, lines, 1)
	assert.Equal(t, "user", decodeLine(t, lines[0])["type"])
}
