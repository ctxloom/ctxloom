package relay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude/relay"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// sessionCutter stands between the relay and the endpoint and can end the
// wake's session as the endpoint would: every later request on it is
// answered 404, and the requests it holds open are cut. The wake's session is
// the one that subscribes; each answered subscribe is announced on
// subscribed, so a test knows a resubscription landed instead of polling for
// it. refuseInit answers every initialize after the cut with 503.
type sessionCutter struct {
	proxy      *httputil.ReverseProxy
	subscribed chan struct{}

	mu         sync.Mutex
	wake       string
	dead       map[string]bool
	open       map[string][]context.CancelFunc
	cut        bool
	refuseInit bool
}

func newSessionCutter(t *testing.T, endpointURL string) (*sessionCutter, string) {
	t.Helper()
	target, err := url.Parse(endpointURL)
	require.NoError(t, err)
	c := &sessionCutter{subscribed: make(chan struct{}, 4), dead: map[string]bool{}, open: map[string][]context.CancelFunc{}}
	c.proxy = &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(&url.URL{Scheme: target.Scheme, Host: target.Host}) }, FlushInterval: -1}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return c, srv.URL + target.Path
}

func (c *sessionCutter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var msg struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &msg)
	id := r.Header.Get("Mcp-Session-Id")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c.mu.Lock()
	refused := c.dead[id] || (msg.Method == "initialize" && c.cut && c.refuseInit)
	if !refused && id != "" {
		c.open[id] = append(c.open[id], cancel)
	}
	c.mu.Unlock()
	switch {
	case c.isDead(id):
		http.Error(w, "session not found", http.StatusNotFound)
		return
	case refused:
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	c.proxy.ServeHTTP(w, r.WithContext(ctx))
	if msg.Method == "resources/subscribe" {
		c.mu.Lock()
		c.wake = id
		c.mu.Unlock()
		c.subscribed <- struct{}{}
	}
}

func (c *sessionCutter) isDead(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead[id]
}

// cutWake ends the wake's current session.
func (c *sessionCutter) cutWake() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = true
	c.dead[c.wake] = true
	for _, cancel := range c.open[c.wake] {
		cancel()
	}
}

// awaitLine returns the first stderr line containing want.
func awaitLine(t *testing.T, stderr lines, want string) string {
	t.Helper()
	for {
		line := testsupport.Await(t, bound, stderr, "the relay never said %q", want)
		if strings.Contains(line, want) {
			return line
		}
	}
}

// A wake session the endpoint ended is said so on stderr, and the relay
// subscribes again, once, on a fresh session: a wake fired after that still
// reaches claude. Losing that session too leaves the wake down, said so.
func TestRelay_ALostWakeSessionIsReportedAndResubscribedOnce(t *testing.T) {
	endpointURL, sig := endpoint(t)
	cutter, url := newSessionCutter(t, endpointURL)
	sock, posts := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	_, stderr, _ := startRelay(t, env)
	testsupport.Await(t, bound, cutter.subscribed, "the relay never subscribed to the wake")

	cutter.cutWake()

	awaitLine(t, stderr, relay.WakeLost)
	testsupport.Await(t, bound, cutter.subscribed, "the relay did not subscribe again")
	require.NoError(t, sig.Fire(context.Background(), nonce))
	testsupport.Await(t, bound, posts, "the wake fired after the resubscription was not posted")

	cutter.cutWake()
	awaitLine(t, stderr, relay.WakeDown)
}

// A resubscription the endpoint refuses leaves the wake down, said so on
// stderr; the relay does not keep retrying.
func TestRelay_ARefusedResubscriptionLeavesTheWakeDownAndSaysSo(t *testing.T) {
	endpointURL, _ := endpoint(t)
	cutter, url := newSessionCutter(t, endpointURL)
	cutter.refuseInit = true
	sock, _ := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	_, stderr, _ := startRelay(t, env)
	testsupport.Await(t, bound, cutter.subscribed, "the relay never subscribed to the wake")

	cutter.cutWake()

	awaitLine(t, stderr, relay.WakeLost)
	awaitLine(t, stderr, "cannot reach the session endpoint for the wake")
}
