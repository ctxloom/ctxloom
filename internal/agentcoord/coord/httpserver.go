package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// MCPPath is the coordinator's MCP endpoint path; CTXLOOM_COORD_URL is
// http://<host>:<port>/mcp and the gRPC RunnerChannel rides the same
// host:port (one plaintext-HTTP/2 listener, content-type routed).
const MCPPath = "/mcp"

// ServerFactory builds the per-identity MCP tool surface. The host (cli)
// supplies it: the coordinator caches one *mcp.Server per credential so
// EVERY tool (context/session/memory/agents) sees the CALLER's identity —
// never the host process's env (review R12f).
type ServerFactory func(id Identity) *mcp.Server

// coordServing is the coordinator's listener set: the loopback listener
// (default) plus, only while a container runner is active, listeners on the
// container-reachable bridge/host interfaces — never 0.0.0.0 (review R10).
type coordServing struct {
	c       *Coordinator
	handler http.Handler
	httpSrv *http.Server

	mu        sync.Mutex
	loopback  net.Listener
	loopURL   string
	wide      []net.Listener
	wideURL   string
	widePort  int
	mcpCache  map[string]*mcp.Server // credHash → cached per-identity server
	factory   ServerFactory
}

// endpointState persists the bound ports so a relaunched coordinator
// re-binds the SAME endpoint (acceptance (4): adopted container
// RunnerChannels re-Hello against a stable re-bindable endpoint).
type endpointState struct {
	LoopbackPort int `json:"loopback_port,omitempty"`
	WidePort     int `json:"wide_port,omitempty"`
}

// ctxIdentityKey carries the verified caller identity on the request context.
type ctxIdentityKey struct{}

// IdentityFrom returns the verified caller identity the auth middleware
// stamped on ctx.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxIdentityKey{}).(Identity)
	return id, ok
}

// Serve stands the listeners up: loopback by default; widening happens on
// demand when a container child spawns. factory builds the per-identity MCP
// surface.
func (c *Coordinator) Serve(factory ServerFactory) error {
	if c.srv != nil {
		return nil
	}
	if factory == nil {
		// Core-only hosting (tests): the endpoint exists (URLs resolve, the
		// gRPC RunnerChannel is live) but MCP answers an empty toolset.
		factory = func(Identity) *mcp.Server {
			return mcp.NewServer(&mcp.Implementation{Name: "ctxloom-coordinator", Version: "dev"}, nil)
		}
	}
	s := &coordServing{c: c, factory: factory, mcpCache: make(map[string]*mcp.Server)}

	grpcSrv := c.grpcServer()
	mcpHandler := s.authMiddleware(mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			return nil // unreachable behind the middleware
		}
		return s.serverFor(r.Header.Get("Authorization"), id)
	}, nil))

	mux := http.NewServeMux()
	mux.Handle(MCPPath, mcpHandler)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gRPC (the RunnerChannel) is plaintext HTTP/2 with the grpc
		// content-type; everything else is the MCP endpoint.
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	// Unencrypted HTTP/2 (h2c) via net/http's Protocols — the modern
	// replacement for the deprecated x/net/http2/h2c wrapper. Both the gRPC
	// RunnerChannel (prior-knowledge h2c) and HTTP/1.1 MCP POSTs share one
	// listener; the credential authenticates every request, and the bind is
	// loopback/bridge-only (never 0.0.0.0).
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	s.httpSrv = &http.Server{Handler: s.handler, Protocols: protocols}

	ep := s.loadEndpoint()
	ln, err := bindPreferring("127.0.0.1", ep.LoopbackPort)
	if err != nil {
		return fmt.Errorf("coord: bind loopback listener: %w", err)
	}
	s.loopback = ln
	s.loopURL = fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, MCPPath)
	go func() { _ = s.httpSrv.Serve(ln) }()
	s.saveEndpoint()

	c.srv = s
	return nil
}

// bindPreferring binds host:port, falling back to an ephemeral port when the
// recorded one is taken.
func bindPreferring(host string, port int) (net.Listener, error) {
	if port > 0 {
		if ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port))); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

func (s *coordServing) endpointPath() string { return filepath.Join(s.c.stateDir, "endpoint.json") }

func (s *coordServing) loadEndpoint() endpointState {
	var ep endpointState
	if raw, err := os.ReadFile(s.endpointPath()); err == nil {
		_ = json.Unmarshal(raw, &ep)
	}
	return ep
}

func (s *coordServing) saveEndpoint() {
	s.mu.Lock()
	ep := endpointState{WidePort: s.widePort}
	if s.loopback != nil {
		ep.LoopbackPort = s.loopback.Addr().(*net.TCPAddr).Port
	}
	s.mu.Unlock()
	raw, _ := json.Marshal(ep)
	if err := os.WriteFile(s.endpointPath(), raw, 0o600); err != nil {
		clidiag.Warn("ctxloom", "coordinator: persist endpoint: %v", err)
	}
}

// authMiddleware verifies the bearer credential PER REQUEST (never per
// connection — review R9) and stamps the identity on the request context.
func (s *coordServing) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "missing bearer credential", http.StatusUnauthorized)
			return
		}
		id, valid := s.c.Identify(token)
		if !valid {
			http.Error(w, "unknown or revoked credential", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxIdentityKey{}, id)))
	})
}

// serverFor returns the cached per-identity MCP server (one per credential;
// identity is immutable per credential, so the cache key is the credential).
func (s *coordServing) serverFor(authHeader string, id Identity) *mcp.Server {
	token, _ := strings.CutPrefix(authHeader, "Bearer ")
	key := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv, ok := s.mcpCache[key]; ok {
		return srv
	}
	srv := s.factory(id)
	s.mcpCache[key] = srv
	return srv
}

// LoopbackURL is the coordinator MCP URL for host-side callers (the parent
// harness, host children). Empty until Serve.
func (c *Coordinator) LoopbackURL() string {
	if c.srv == nil {
		return ""
	}
	return c.srv.loopURL
}

// ReachURL resolves the URL a caller on runtimeAxis can dial — the hosting
// glue uses it for the parent harness's env trio.
func (c *Coordinator) ReachURL(runtimeAxis string) (string, error) {
	return c.reachURL(runtimeAxis)
}

// reachURL resolves the URL a child on runtimeAxis dials: loopback for host
// runs; the widened bridge/host-interface listener for container runs
// (opened on demand, never 0.0.0.0).
func (c *Coordinator) reachURL(runtimeAxis string) (string, error) {
	if c.srv == nil {
		return "", errors.New("coordinator listeners are not up")
	}
	if runtimeAxis != "container" {
		return c.srv.loopURL, nil
	}
	return c.srv.ensureWide()
}

// ensureWide opens the container-reachable listeners once and returns the
// advertised URL. Candidate interfaces, most specific first: the container
// runtime's bridge gateway (rootful daemons — e.g. docker0's 172.17.0.1),
// then the host's primary outbound interface (rootless slirp/pasta setups
// reach the host's non-loopback addresses). All candidates bind ONE shared
// port so a single URL works wherever the packet lands. LABEL (plan): the
// per-runtime reachability matrix is verified live by the smoke run, not
// assumed here; an unbindable candidate set fails loudly at the spawn verb.
func (s *coordServing) ensureWide() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wideURL != "" {
		return s.wideURL, nil
	}
	candidates := containerReachIPs()
	if len(candidates) == 0 {
		return "", errors.New("no container-reachable host interface found (no bridge gateway, no primary outbound IP)")
	}
	port := s.widePort // recorded port first (stable re-bindable endpoint)
	var bound []net.Listener
	var boundIPs []string
	for attempt := 0; attempt < 2; attempt++ {
		bound, boundIPs = nil, nil
		for _, ip := range candidates {
			addr := net.JoinHostPort(ip, fmt.Sprint(port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				continue
			}
			if port == 0 {
				port = ln.Addr().(*net.TCPAddr).Port
			}
			bound = append(bound, ln)
			boundIPs = append(boundIPs, ip)
		}
		if len(bound) > 0 {
			break
		}
		if port == 0 {
			break
		}
		port = 0 // recorded port unavailable on every candidate: re-pick
	}
	if len(bound) == 0 {
		return "", fmt.Errorf("could not bind a container-reachable listener on any of %v", candidates)
	}
	for _, ln := range bound {
		go func(l net.Listener) { _ = s.httpSrv.Serve(l) }(ln)
	}
	s.wide = bound
	s.widePort = port
	// Advertise the FIRST bound candidate (bridge gateway preferred).
	s.wideURL = fmt.Sprintf("http://%s%s", net.JoinHostPort(boundIPs[0], fmt.Sprint(port)), MCPPath)
	go s.saveEndpoint()
	return s.wideURL, nil
}

// close shuts every listener down.
func (s *coordServing) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loopback != nil {
		_ = s.loopback.Close()
	}
	for _, ln := range s.wide {
		_ = ln.Close()
	}
}

// containerReachIPs collects candidate host IPs a container can dial,
// most specific first: each detected container runtime's default bridge
// gateway, then the host's primary outbound interface IP.
func containerReachIPs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip != "" && ip != "<no value>" && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	for _, probe := range [][]string{
		{"docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"},
		{"podman", "network", "inspect", "podman", "--format", "{{range .Subnets}}{{.Gateway}}{{end}}"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		outB, err := exec.CommandContext(ctx, probe[0], probe[1:]...).Output()
		cancel()
		if err == nil {
			add(string(outB))
		}
	}
	if ip := primaryOutboundIP(); ip != "" {
		add(ip)
	}
	return out
}

// primaryOutboundIP resolves the host's primary outbound interface IP with a
// connected UDP socket (no packets are sent).
func primaryOutboundIP() string {
	conn, err := net.Dial("udp", "203.0.113.1:9") // TEST-NET-3: never routed
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}
