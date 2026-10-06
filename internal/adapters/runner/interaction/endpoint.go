// Package mcp is the runner's implementation of delivery.Dynamic: the
// session's ONE MCP endpoint, bound at the address the Launch carries and
// served as Streamable HTTP under delivery.ServePolicy — a bearer on every
// request, an Origin allowlist with 403 on a miss. The cell-local tools and
// the ctxloom:// resources serve off the Loadout's Package and Index; the
// coordination tools, the host-relayed tools and artifact fetch ride the
// runner's reach-back Home. It holds no config.
package interaction

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Endpoint is one runner's Dynamic port: Serve binds the Loadout's endpoint.
type Endpoint struct {
	// Home is the runner's reach-back link: the coordination frames, the
	// host relays and artifact fetch ride it.
	Home *runner.Home
	// Wake is the session's wake signal: the endpoint serves the relay's
	// subscription to WakeURI on it, and the runner fires it.
	Wake *WakeSignal
	// Reporter receives the endpoint's diagnostics; nil discards.
	Reporter report.Sink
	// ClientExit registers what runs each time the client process that owns
	// the endpoint's MCP sessions exits, and returns its release
	// (runner.Home.SetEngineExit in production). The owner is the engine's
	// process tree: the bearer is delivered only into the engine's entry,
	// and claude's relay is claude's own child. None of them sends DELETE on
	// its way out, so without this every session they opened stays open,
	// with its goroutines, for the life of the runner. Nil registers nowhere.
	ClientExit func(onExit func()) (release func())
	// SessionTimeout closes an MCP session no request has reached for that
	// long; zero is IdleSessionTimeout.
	SessionTimeout time.Duration

	// serveGate, when set, runs at the head of the serve goroutine — before
	// http.Server.Serve has registered the listener. Tests hold it to force
	// Close into that window; see WithServeGate in export_test.go.
	serveGate func()
	// reaped, when set, runs once a reap has closed every session it took;
	// see WithReapHook in export_test.go.
	reaped func()
	// served, when set, is handed the MCP server Serve builds; see
	// WithServerHook in export_test.go.
	served func(*mcp.Server)
}

// IdleSessionTimeout is how long a session may go without a request before
// the endpoint closes it. It collects the sessions ClientExit cannot
// attribute: a client that is still alive but has abandoned its session (a
// relay claude replaced, the spare session a re-initialization opened) never
// sends DELETE and never exits while the engine lives. It is generous
// because a live session can be legitimately idle for hours while its
// coordinator waits on a human, and because the production client, claude's
// relay, does not re-initialize after the 404 a closed session answers: the
// relay pings each of its sessions (relay.KeepAliveInterval) to stay inside
// it, and only a POST resets go-sdk's idle timer.
const IdleSessionTimeout = 12 * time.Hour

// sessionTimeout is SessionTimeout, or IdleSessionTimeout when it is zero.
func (e Endpoint) sessionTimeout() time.Duration {
	if e.SessionTimeout == 0 {
		return IdleSessionTimeout
	}
	return e.SessionTimeout
}

// ErrNoHome refuses to serve without the reach-back link: every
// coordination tool the surface vouches for would answer with nothing.
var ErrNoHome = errors.New("runner mcp: no reach-back home to serve the coordination tools over")

// shutdownBudget bounds Close's graceful shutdown of the HTTP server.
const shutdownBudget = 2 * time.Second

// Serve implements delivery.Dynamic. It BINDS the address lo.MCP names — it
// never chooses one — and refuses with delivery.ErrEndpointUnavailable when
// the address cannot be bound (another process took the port between two
// incarnations of the session), the one refusal the coordinator answers with
// a rebind. Every request must carry the endpoint's bearer (401 otherwise)
// and, when it carries an Origin, one on the policy's allowlist (403
// otherwise); the allowlist may not be empty.
func (e Endpoint) Serve(ctx context.Context, lo delivery.Loadout, policy delivery.ServePolicy) (delivery.Served, error) {
	target, err := e.bindable(lo, policy)
	if err != nil {
		return delivery.Served{}, err
	}
	rep := report.To(e.Reporter)
	server, err := NewServer(rep, e.Home, lo.Identity.Harp, lo.WorkDir, lo.SessionHome, lo.Identity.Leaf, loadoutSurface{lo: lo}, e.Wake)
	if err != nil {
		return delivery.Served{}, err
	}
	ln, err := net.Listen("tcp", target.Host)
	if err != nil {
		return delivery.Served{}, fmt.Errorf("%w: %s: %v", delivery.ErrEndpointUnavailable, target.Host, err)
	}
	path := target.Path
	if path == "" {
		path = "/"
	}
	mux := http.NewServeMux()
	mux.Handle(path, guard(lo.MCP.Credential, policy.AllowedOrigins, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{SessionTimeout: e.sessionTimeout()})))
	// The approval hook's POST rides the same listener, behind the same
	// bearer and Origin rules.
	mux.Handle(runner.HookPath, guard(lo.MCP.Credential, policy.AllowedOrigins, hookHandler(e.Home)))
	srv := &http.Server{Handler: mux}
	if e.served != nil {
		e.served(server)
	}
	release := func() {}
	if e.ClientExit != nil {
		release = e.ClientExit(func() { e.reap(server) })
	}
	go func() {
		if e.serveGate != nil {
			e.serveGate()
		}
		// Serve returns http.ErrServerClosed on a deliberate Close; anything
		// else is the endpoint dying while the runner carries on, which the
		// engine then sees as every ctxloom tool failing with a transport
		// error that names nothing about the cause.
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			rep.Warnf("session MCP endpoint %s stopped serving: %v (ctxloom tools in this session will fail until the runner is restarted)", lo.MCP.URL, serr)
		}
	}()
	return delivery.Served{Close: func() error {
		release()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		// The sessions close FIRST: Shutdown ends the HTTP connections, not
		// the MCP sessions they carry, and a session's open stream keeps its
		// connection from ever going idle for Shutdown to close.
		select {
		case <-e.reap(server):
		case <-sctx.Done():
		}
		err := srv.Shutdown(sctx)
		// Shutdown closes only the listeners srv.Serve has already registered.
		// Until the goroutine above reaches that point ln is not yet srv's, and
		// without this close it would outlive Close and hold the address the
		// next incarnation of the session must bind.
		if cerr := ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) && err == nil {
			err = cerr
		}
		return err
	}}, nil
}

// reap closes every MCP session open on server: their client is gone, so
// none of them can be used again. The set is taken here, on the caller's
// goroutine, so a session the next client opens afterwards is never in it;
// the closes run off it, because ServerSession.Close waits for the session's
// in-flight handlers and must not hold up the engine host's turn loop. done
// is closed once every one of them is.
func (e Endpoint) reap(server *mcp.Server) (done <-chan struct{}) {
	open := slices.Collect(server.Sessions())
	closed := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, ss := range open {
			wg.Go(func() { _ = ss.Close() })
		}
		wg.Wait()
		close(closed)
		if e.reaped != nil {
			e.reaped()
		}
	}()
	return closed
}

// bindable refuses what Serve cannot serve — no allowlist, no reach-back
// home, no wake signal, no endpoint in the loadout — and otherwise returns
// the address to bind.
func (e Endpoint) bindable(lo delivery.Loadout, policy delivery.ServePolicy) (*url.URL, error) {
	switch {
	case len(policy.AllowedOrigins) == 0:
		return nil, delivery.ErrNoAllowedOrigins
	case e.Home == nil:
		return nil, ErrNoHome
	case e.Wake == nil:
		return nil, ErrNoWakeSignal
	case lo.MCP.URL == "" || lo.MCP.Credential == "":
		return nil, fmt.Errorf("%w: the loadout names no endpoint to bind", delivery.ErrEndpointUnavailable)
	}
	target, err := url.Parse(lo.MCP.URL)
	if err != nil || target.Host == "" {
		return nil, fmt.Errorf("%w: %q is not a bindable URL", delivery.ErrEndpointUnavailable, lo.MCP.URL)
	}
	return target, nil
}

// guard is delivery.ServePolicy as an http.Handler: the bearer is checked
// FIRST, in constant time, so an unauthenticated caller learns nothing about
// the allowlist; then a request carrying an Origin must name an allowed one.
// A request with no Origin is a non-browser client (the engine itself), and
// the allowlist has nothing to say about it.
func guard(credential string, allowedOrigins []string, next http.Handler) http.Handler {
	want := []byte("Bearer " + credential)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ctxloom"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, allowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed matches an Origin against the allowlist by scheme and host;
// an allowlist entry that names no port admits any port on that host, so
// the loopback entry covers the per-session port.
func originAllowed(origin string, allowed []string) bool {
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(a, origin) {
			return true
		}
		u, err := url.Parse(a)
		if err != nil {
			continue
		}
		if !strings.EqualFold(u.Scheme, o.Scheme) || !strings.EqualFold(u.Hostname(), o.Hostname()) {
			continue
		}
		if u.Port() == "" || u.Port() == o.Port() {
			return true
		}
	}
	return false
}

var _ delivery.Dynamic = Endpoint{}
