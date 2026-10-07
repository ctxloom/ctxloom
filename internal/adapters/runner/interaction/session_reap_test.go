package interaction_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
)

// exitingClient stands in for runner.Home.SetEngineExit: it holds what the
// endpoint registered, and exit runs it as the engine's exit would.
type exitingClient struct {
	mu     sync.Mutex
	onExit func()
}

func (c *exitingClient) register(onExit func()) (release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onExit = onExit
	return func() {}
}

func (c *exitingClient) exit(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	onExit := c.onExit
	c.mu.Unlock()
	require.NotNil(t, onExit, "the endpoint registers for its client's exit")
	onExit()
}

// reapServe serves an endpoint whose client exits on c.exit, returning the
// served endpoint and a channel that receives once per completed reap.
func reapServe(t *testing.T, c *exitingClient, lo delivery.Loadout) (delivery.Served, <-chan struct{}) {
	t.Helper()
	reaped := make(chan struct{}, 4)
	ep := interaction.WithReapHook(interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal(nil), ClientExit: c.register}, func() {
		select {
		case reaped <- struct{}{}:
		default:
		}
	})
	served, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
	return served, reaped
}

func awaitReap(t *testing.T, reaped <-chan struct{}) {
	t.Helper()
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the reap never completed")
	}
}

// pingOn sends a ping on session id and returns the status: 404 once the
// endpoint no longer holds the session.
func pingOn(t *testing.T, url, id string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"ping"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer bearer-token")
	req.Header.Set("Mcp-Session-Id", id)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	return res.StatusCode
}

// parkedReaders counts the goroutines parked reading a server session's
// incoming messages — the goroutine every leaked session holds forever.
func parkedReaders() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "(*streamableServerConn).Read(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

var bearer = map[string]string{"Authorization": "Bearer bearer-token"}

// TestServe_TheClientsExitClosesItsSessions: the engine's process tree is
// every client the endpoint has, and none of them sends DELETE on its way
// out. When it exits, every session it held is closed: the endpoint no
// longer knows the session id, and the session's reader goroutine is gone.
func TestServe_TheClientsExitClosesItsSessions(t *testing.T) {
	c := &exitingClient{}
	lo := loadoutAt(freePort(t))
	_, reaped := reapServe(t, c, lo)
	base := parkedReaders()
	id := connect(t, lo.MCP.URL, bearer).ID()
	require.Equal(t, http.StatusOK, pingOn(t, lo.MCP.URL, id), "the session is live before its client exits")

	c.exit(t)
	awaitReap(t, reaped)

	assert.Equal(t, http.StatusNotFound, pingOn(t, lo.MCP.URL, id), "the exited client's session is closed")
	assert.LessOrEqual(t, parkedReaders(), base, "the closed session's reader goroutine is gone")
}

// TestServe_ASessionOpenedAfterTheExitOutlivesIt: the sessions closed are
// the ones open AT the exit; the next engine process's session, opened after
// it, is not the exited client's and stays live.
func TestServe_ASessionOpenedAfterTheExitOutlivesIt(t *testing.T) {
	c := &exitingClient{}
	lo := loadoutAt(freePort(t))
	_, reaped := reapServe(t, c, lo)
	before := connect(t, lo.MCP.URL, bearer).ID()

	c.exit(t)
	after := connect(t, lo.MCP.URL, bearer).ID()
	awaitReap(t, reaped)

	assert.Equal(t, http.StatusNotFound, pingOn(t, lo.MCP.URL, before))
	assert.Equal(t, http.StatusOK, pingOn(t, lo.MCP.URL, after), "a session opened after the exit is not the exited client's")
}

// TestServe_CloseClosesTheOpenSessions: shutting the HTTP server down does
// not end the MCP sessions it served; Close closes them too.
func TestServe_CloseClosesTheOpenSessions(t *testing.T) {
	c := &exitingClient{}
	lo := loadoutAt(freePort(t))
	served, reaped := reapServe(t, c, lo)
	base := parkedReaders()
	connect(t, lo.MCP.URL, bearer)

	require.NoError(t, served.Close())
	awaitReap(t, reaped)

	assert.LessOrEqual(t, parkedReaders(), base, "no session the endpoint served outlives its Close")
}

// TestServe_ASpareConnectionDoesNotHoldClose: an HTTP client may hold a
// connection it dialed and never sent a request on — Go's transport parks
// one whenever another connection frees up before its dial completes, which
// the SDK client's concurrent POST and standalone GET invite. Shutdown counts
// such a connection as busy for its first seconds, longer than Close's
// budget; Close must still end it and succeed.
func TestServe_ASpareConnectionDoesNotHoldClose(t *testing.T) {
	c := &exitingClient{}
	lo := loadoutAt(freePort(t))
	served, _ := reapServe(t, c, lo)
	connect(t, lo.MCP.URL, bearer)

	u, err := url.Parse(lo.MCP.URL)
	require.NoError(t, err)
	spare, err := net.Dial("tcp", u.Host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = spare.Close() })
	// The accept loop takes connections in order and tracks each before it
	// accepts the next, so once a request on a connection dialed AFTER the
	// spare is answered, the server holds the spare as a new connection.
	fresh := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	res, err := fresh.Get(lo.MCP.URL)
	require.NoError(t, err)
	_ = res.Body.Close()

	require.NoError(t, served.Close(), "a connection that never carried a request does not hold Close")
	_, err = spare.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "Close ends the spare connection rather than leaving it open")
}
