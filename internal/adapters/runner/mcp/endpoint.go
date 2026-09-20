// Package mcp is the runner's implementation of delivery.Dynamic: the
// session's ONE MCP endpoint, bound at the address the Launch carries and
// served as Streamable HTTP under delivery.ServePolicy — a bearer on every
// request, an Origin allowlist with 403 on a miss. The cell-local tools and
// the ctxloom:// resources serve off the Loadout's Package and Index; the
// coordination tools, the host-relayed tools and artifact fetch ride the
// runner's reach-back Home. It holds no config.
package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Endpoint is one runner's Dynamic port: Serve binds the Loadout's endpoint.
type Endpoint struct {
	// Home is the runner's reach-back link: the coordination frames, the
	// host relays and artifact fetch ride it.
	Home *coord.Home
	// Reporter receives the endpoint's diagnostics; nil discards.
	Reporter report.Sink
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
	if len(policy.AllowedOrigins) == 0 {
		return delivery.Served{}, delivery.ErrNoAllowedOrigins
	}
	if e.Home == nil {
		return delivery.Served{}, ErrNoHome
	}
	if lo.MCP.URL == "" || lo.MCP.Credential == "" {
		return delivery.Served{}, fmt.Errorf("%w: the loadout names no endpoint to bind", delivery.ErrEndpointUnavailable)
	}
	target, err := url.Parse(lo.MCP.URL)
	if err != nil || target.Host == "" {
		return delivery.Served{}, fmt.Errorf("%w: %q is not a bindable URL", delivery.ErrEndpointUnavailable, lo.MCP.URL)
	}
	rep := report.To(e.Reporter)
	server, err := NewServer(rep, e.Home, lo.Identity.Harp, lo.WorkDir, lo.Identity.Leaf, loadoutSurface{lo: lo})
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
	mux.Handle(path, guard(lo.MCP.Credential, policy.AllowedOrigins, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)))
	srv := &http.Server{Handler: mux}
	go func() {
		// Serve returns http.ErrServerClosed on a deliberate Close; anything
		// else is the endpoint dying while the runner carries on, which the
		// engine then sees as every ctxloom tool failing with a transport
		// error that names nothing about the cause.
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			rep.Warnf("session MCP endpoint %s stopped serving: %v (ctxloom tools in this session will fail until the runner is restarted)", lo.MCP.URL, serr)
		}
	}()
	return delivery.Served{Close: func() error {
		sctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		return srv.Shutdown(sctx)
	}}, nil
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
