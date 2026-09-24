package coordgrpc

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServe_BindsLoopbackOnly: nothing wide exists until a container child asks
// for it. This is the invariant that keeps a plain host session off the
// machine's network entirely.
func TestServe_BindsLoopbackOnly(t *testing.T) {
	c := coordharness.New(t, t.TempDir())
	t.Cleanup(c.Close)
	require.NoError(t, Serve(c))

	parsed, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	assert.True(t, net.ParseIP(host).IsLoopback(), "Serve binds loopback and nothing else: %q", host)

	srv := c.Transport().(*coordServing)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Empty(t, srv.extra, "no listener beyond loopback may exist before a container cell names one")
}

// privateStandIn is a host address other than the loopback listener's that a
// test may bind: 127.0.0.2 answers on Linux's lo (the whole 127/8), and stands
// in for a bridge gateway or the public fallback.
func privateStandIn(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 is not bindable on this host: %v", err)
	}
	_ = ln.Close()
	return "127.0.0.2"
}

// TestListen_OpensTheCellsAddressOnTheLoopbackPort: the listener a cell names
// is opened on the loopback listener's port, so a reach re-minted by swapping
// only the host lands on it; asked twice, it is opened once.
func TestListen_OpensTheCellsAddressOnTheLoopbackPort(t *testing.T) {
	addr := privateStandIn(t)
	c := coordharness.New(t, t.TempDir())
	t.Cleanup(c.Close)
	require.NoError(t, Serve(c))
	srv := c.Transport().(*coordServing)

	require.NoError(t, srv.Listen(present.Listen{Addr: addr}))
	require.NoError(t, srv.Listen(present.Listen{Addr: addr}), "a second cell naming the same address reuses its listener")

	loop, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := net.Dial("tcp", net.JoinHostPort(addr, loop.Port()))
	require.NoError(t, err, "the named address answers on the loopback listener's port")
	_ = conn.Close()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Len(t, srv.extra, 1)
}

// TestListen_PublicIsReportedOnceWithItsReason: the fallback is allowed, never
// silent — reported once per address, naming the runtime's reason and that
// the endpoint is token-protected. A private listener draws no report.
func TestListen_PublicIsReportedOnceWithItsReason(t *testing.T) {
	addr := privateStandIn(t)
	warnings := captureWarnings(t)
	c := coordharness.New(t, t.TempDir())
	t.Cleanup(c.Close)
	require.NoError(t, Serve(c))
	srv := c.Transport().(*coordServing)

	require.NoError(t, srv.Listen(present.Listen{Addr: addr}))
	assert.NotContains(t, warnings.String(), addr, "a private listener is not reported")

	pub := present.Listen{Addr: addr, Public: true, Why: "the runtime offers no private route"}
	require.NoError(t, srv.Listen(pub))
	require.NoError(t, srv.Listen(pub))
	out := warnings.String()
	assert.Equal(t, 1, strings.Count(out, "reachable beyond this host"), "reported once: %s", out)
	assert.Contains(t, out, "the runtime offers no private route")
	assert.Contains(t, out, "token-protected")
}
