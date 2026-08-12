package mcp

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeRunnerHTTP_ReportsAServeFailure pins the fix for the runner
// starting its MCP endpoint in a goroutine and discarding Serve's return
// value. Serve only ever returns on termination, so discarding it means a
// post-startup failure is completely invisible: the runner keeps running,
// keeps advertising its socket and its discovery marker, and every ctxloom
// tool in the harness's session fails against an endpoint nobody said had
// died.
func TestServeRunnerHTTP_ReportsAServeFailure(t *testing.T) {
	warnings := captureWarnings(t)

	sock := filepath.Join(t.TempDir(), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	// Closing the listener out from under Serve stands in for any reason the
	// endpoint stops answering after the socket was published.
	require.NoError(t, ln.Close())

	serveRunnerHTTP(&http.Server{Handler: http.NewServeMux()}, ln, sock)

	out := warnings.String()
	assert.Contains(t, out, "stopped serving", "a dead endpoint must say so")
	assert.Contains(t, out, sock, "the warning must name the socket that is now dead")
}

// TestServeRunnerHTTP_SilentOnCleanShutdown is the other arm: a deliberate
// Shutdown is not a fault, and warning on it would train the reader to ignore
// the warning that matters.
func TestServeRunnerHTTP_SilentOnCleanShutdown(t *testing.T) {
	warnings := captureWarnings(t)

	sock := filepath.Join(t.TempDir(), "mcp.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.NewServeMux()}

	done := make(chan struct{})
	go func() {
		serveRunnerHTTP(srv, ln, sock)
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveRunnerHTTP did not return after Shutdown")
	}

	assert.Empty(t, warnings.String(), "a clean shutdown must not warn")
}
