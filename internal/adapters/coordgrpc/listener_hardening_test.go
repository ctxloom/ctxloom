package coordgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// recordedAddrs is endpoint.json's listen_addrs and host_peers_only_addrs; the
// credential in the same file is never read out.
func recordedAddrs(t *testing.T, stateDir string) (addrs, hostOnly []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, discover.FileName))
	require.NoError(t, err)
	var ep discover.State
	require.NoError(t, json.Unmarshal(raw, &ep))
	return ep.ListenAddrs, ep.HostPeersOnlyAddrs
}

func dials(t *testing.T, addr, port string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// TestUnlisten_ClosesTheListenerOnTheLastHolder: a listener two runs need
// stays open for the first one's end and closes on the second's, and
// endpoint.json lists it exactly as long as it is open.
func TestUnlisten_ClosesTheListenerOnTheLastHolder(t *testing.T) {
	addr := privateStandIn(t)
	stateDir := t.TempDir()
	c := servedCoordinator(t, stateDir)
	t.Cleanup(c.Close)
	srv := c.Transport().(*coordServing)
	loop, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)

	require.NoError(t, srv.Listen(present.Listen{Addr: addr}, "run-1"))
	require.NoError(t, srv.Listen(present.Listen{Addr: addr}, "run-2"))
	require.True(t, dials(t, addr, loop.Port()))

	srv.Unlisten("run-1")
	assert.True(t, dials(t, addr, loop.Port()), "run-2 still needs the listener")
	listed, _ := recordedAddrs(t, stateDir)
	assert.Equal(t, []string{addr}, listed)

	srv.Unlisten("run-2")
	assert.False(t, dials(t, addr, loop.Port()), "the last holder's end closes the listener")
	listed, _ = recordedAddrs(t, stateDir)
	assert.Empty(t, listed, "endpoint.json no longer lists the closed address")

	srv.Unlisten("run-2") // a run that holds nothing is a no-op
	require.NoError(t, srv.Listen(present.Listen{Addr: addr}, "run-3"), "a later container run re-opens it")
	assert.True(t, dials(t, addr, loop.Port()))
}

// TestServe_RecordedAddressIsNotReopenedWithoutAContainerRun: an address a
// previous incarnation recorded is re-opened only for a live container run, and
// the file stops listing it otherwise.
func TestServe_RecordedAddressIsNotReopenedWithoutAContainerRun(t *testing.T) {
	addr := privateStandIn(t)
	stateDir := t.TempDir()
	first := servedCoordinator(t, stateDir)
	require.NoError(t, first.Transport().(*coordServing).Listen(present.Listen{Addr: addr}, "run-1"))
	firstURL := first.LoopbackURL()
	first.Close()
	listed, _ := recordedAddrs(t, stateDir)
	require.Equal(t, []string{addr}, listed, "premise: the file records the address")

	second := servedCoordinator(t, stateDir)
	t.Cleanup(second.Close)
	require.Equal(t, firstURL, second.LoopbackURL())
	loop, err := url.Parse(second.LoopbackURL())
	require.NoError(t, err)
	assert.False(t, dials(t, addr, loop.Port()), "no container run is live, so nothing is listening there")
	listed, _ = recordedAddrs(t, stateDir)
	assert.Empty(t, listed)
}

// TestListen_HostPeersOnlyIsRecordedForARelaunch: a filtered listener is
// recorded as filtered, so the relaunch that re-opens it keeps the refusal.
func TestListen_HostPeersOnlyIsRecordedForARelaunch(t *testing.T) {
	addr := privateStandIn(t)
	stateDir := t.TempDir()
	c := servedCoordinator(t, stateDir)
	t.Cleanup(c.Close)
	srv := c.Transport().(*coordServing)
	require.NoError(t, srv.Listen(present.Listen{Addr: addr, HostPeersOnly: true}, "run-1"))

	listed, hostOnly := recordedAddrs(t, stateDir)
	assert.Equal(t, []string{addr}, listed)
	assert.Equal(t, []string{addr}, hostOnly)

	srv.Unlisten("run-1")
	_, hostOnly = recordedAddrs(t, stateDir)
	assert.Empty(t, hostOnly)
}

// fakeConn is a connection whose peer is whatever a test says.
type fakeConn struct {
	net.Conn
	remote net.Addr
	closed atomic.Bool
}

func (f *fakeConn) RemoteAddr() net.Addr { return f.remote }
func (f *fakeConn) Close() error         { f.closed.Store(true); return nil }

// fakeListener hands out its queued connections, then blocks until closed.
type fakeListener struct {
	conns chan net.Conn
}

func (l *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *fakeListener) Close() error   { close(l.conns); return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1} }

func peer(ip string) *fakeConn {
	return &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 4000}}
}

// TestHostPeerListener_ForeignPeersNeverReachTheServer: a connection from one
// of the host's own addresses is handed on; one from any other address is
// closed at accept and never returned, and each foreign address is reported
// once however often it reconnects.
func TestHostPeerListener_ForeignPeersNeverReachTheServer(t *testing.T) {
	isLocal := func(ip string) (bool, error) { return ip == "192.0.2.10", nil }
	var mu sync.Mutex
	var reported []string
	inner := &fakeListener{conns: make(chan net.Conn, 8)}
	ln := newHostPeerListener(inner, isLocal, func(remote string) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, remote)
	})

	foreign1, foreign2, foreign3, local := peer("198.51.100.7"), peer("198.51.100.7"), peer("203.0.113.9"), peer("192.0.2.10")
	for _, c := range []*fakeConn{foreign1, foreign2, foreign3, local} {
		inner.conns <- c
	}

	got, err := ln.Accept()
	require.NoError(t, err)
	assert.Same(t, local, got, "the first connection Accept returns is the local peer's: every foreign one was skipped")
	assert.True(t, foreign1.closed.Load(), "a foreign peer is closed")
	assert.True(t, foreign2.closed.Load())
	assert.True(t, foreign3.closed.Load())
	assert.False(t, local.closed.Load(), "a local peer is left open for the server")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"198.51.100.7", "203.0.113.9"}, reported, "one report per foreign address")
}

// TestHostPeerListener_AnUnreadableHostIsRefused: when the host's addresses
// cannot be listed, no peer is shown to be local, so none is let through.
func TestHostPeerListener_AnUnreadableHostIsRefused(t *testing.T) {
	inner := &fakeListener{conns: make(chan net.Conn, 2)}
	ln := newHostPeerListener(inner, func(string) (bool, error) { return false, errors.New("no interfaces") }, nil)
	c := peer("192.0.2.10")
	inner.conns <- c
	close(inner.conns)

	_, err := ln.Accept()
	assert.ErrorIs(t, err, net.ErrClosed)
	assert.True(t, c.closed.Load())
}

// TestNewHTTPServer_BoundsAPeersConnections: the server bounds how long a peer
// may take to send headers and how long an idle connection may live.
func TestNewHTTPServer_BoundsAPeersConnections(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	assert.Equal(t, readHeaderTimeout, srv.ReadHeaderTimeout)
	assert.Equal(t, idleTimeout, srv.IdleTimeout)
	assert.Positive(t, srv.ReadHeaderTimeout)
	assert.Positive(t, srv.IdleTimeout)
	assert.True(t, srv.Protocols.UnencryptedHTTP2(), "gRPC rides h2c")
}

// TestNewHTTPServer_AStreamOutlivesTheIdleTimeout: IdleTimeout reaps a
// connection with no request, never one with a stream open — the
// RunChannel/RunnerChannel streams are perpetual.
func TestNewHTTPServer_AStreamOutlivesTheIdleTimeout(t *testing.T) {
	const idle = 150 * time.Millisecond
	var closedConns atomic.Int32
	release := make(chan struct{})
	srv := newHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
			_, _ = io.WriteString(w, "still here")
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	srv.IdleTimeout = idle
	srv.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			closedConns.Add(1)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	newClient := func() *http.Client {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		return &http.Client{Transport: &http.Transport{Protocols: protocols, DisableKeepAlives: false}}
	}
	base := "http://" + ln.Addr().String()

	// An idle connection is reaped: the timeout is live, not vacuous.
	idleClient := newClient()
	resp, err := idleClient.Get(base + "/quick")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.Eventually(t, func() bool { return closedConns.Load() == 1 }, 20*idle, 10*time.Millisecond,
		"an idle connection must be closed by IdleTimeout")

	// A connection with a stream open is not.
	client := newClient()
	resp, err = client.Get(base + "/stream")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	time.Sleep(5 * idle)
	close(release)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "the stream was cut while open")
	assert.Equal(t, "still here", string(body))
	assert.Equal(t, int32(1), closedConns.Load(), "the stream's connection was closed as idle")
}

// TestAuthGate_RefusesBeforeTheRequestIsDecoded: an unauthenticated unary RPC
// is refused by the gate, so its (here: malformed) request body is never
// handed to grpc-go to decode; a credentialled one still reaches the handler.
func TestAuthGate_RefusesBeforeTheRequestIsDecoded(t *testing.T) {
	c := servedCoordinator(t, t.TempDir())
	t.Cleanup(c.Close)
	loop, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := grpc.NewClient(loop.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	viewer := agentcoordpb.NewConsumerServiceClient(conn)

	_, err = viewer.ListRuns(context.Background(), &agentcoordpb.ListRunsRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "no credential: refused")

	var rawBodyRead atomic.Bool
	gate := authGate(c, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { rawBodyRead.Store(true) }))
	rec := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/agentcoord.v1.ConsumerService/ListRuns", nil)
	require.NoError(t, err)
	gate.ServeHTTP(rec, req)
	assert.False(t, rawBodyRead.Load(), "the wrapped handler (grpc-go's decode) never ran")
	assert.Equal(t, "16", rec.Header().Get("Grpc-Status"), "Unauthenticated, as a trailers-only gRPC response")

	req.Header.Set("Authorization", "Bearer "+c.ConsumerCredential())
	gate.ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, rawBodyRead.Load(), "a live credential passes the gate")
}
