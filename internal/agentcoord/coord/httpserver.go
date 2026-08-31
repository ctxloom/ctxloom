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
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// MCPPath is retained in the advertised CTXLOOM_COORD_URL shape
// (http://<host>:<port>/mcp) for continuity — the runner derives its gRPC
// dial target from the URL's host:port. The agent-facing HTTP-MCP handlers
// were DELETED in B1.6 (deliverable 4): the gRPC RunChannel is the only
// agent ingress now; the h2c listener stays for later frontends/watch APIs.
// Declared in discover, which reads it back out of endpoint.json: advertised
// path and discovered path are one constant.
const MCPPath = discover.MCPPath

// coordServing is the coordinator's listener set: the loopback listener
// (default) plus, only while a container runner is active, listeners on the
// container-reachable bridge/host interfaces — never 0.0.0.0.
// One plaintext-HTTP/2 (h2c) listener carries the gRPC channels; non-gRPC
// requests answer 404 (the tool surface lives at each RUNNER's local
// socket).
type coordServing struct {
	c       *Coordinator
	handler http.Handler
	httpSrv *http.Server
	grpcSrv *grpc.Server

	mu       sync.Mutex
	loopback net.Listener
	loopURL  string
	wide     []net.Listener
	wideURL  string
	widePort int
}

// endpointState persists the bound ports so a relaunched coordinator
// re-binds the SAME endpoint (acceptance (4): adopted container
// RunnerChannels re-Hello against a stable re-bindable endpoint). D1:
// ConsumerCred is how an out-of-process viewer (the TUI, D2) discovers the
// read-only watch credential — 0600, host-local, re-minted every Serve()
// (consumer.go's consumerCreds is never journaled, so this file IS its only
// persistence).
// The layout is discover.State: the reader cannot import this package (it is
// upstream through internal/operations), so the shape lives with the reader and
// a renamed field breaks the build rather than discovery, where a mismatch is
// indistinguishable from "no coordinator is running".
type endpointState = discover.State

// Serve stands the listeners up: loopback by default; widening happens on
// demand when a container child spawns.
func (c *Coordinator) Serve() error {
	if c.srv.Load() != nil {
		return nil // already serving: idempotent no-op, not a new admission
	}
	if c.Draining() {
		return fmt.Errorf("coord: %w", ErrDraining)
	}
	s := &coordServing{c: c}

	grpcSrv := c.grpcServer()
	s.grpcSrv = grpcSrv
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gRPC (RunnerChannel/RunChannel) is plaintext HTTP/2 with the grpc
		// content-type. Nothing else is served here since the B1.6 surface
		// shrink — the MCP tool path terminates at each runner's local
		// socket; per-request credential auth lives in the gRPC
		// interceptors.
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	// Unencrypted HTTP/2 (h2c) via net/http's Protocols — the modern
	// replacement for the deprecated x/net/http2/h2c wrapper. The bind is
	// loopback/bridge-only (never 0.0.0.0); the credential authenticates
	// every gRPC stream and request.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	s.httpSrv = &http.Server{Handler: s.handler, Protocols: protocols}

	ep := s.loadEndpoint()
	// D1: mint the consumer-class watch credential fresh for this process
	// and persist it into endpoint.json ALONGSIDE the ports it's saved
	// with — the file is a viewer's one discovery point for both. Minted
	// BEFORE anything is bound so that every step which can fail runs while
	// there is nothing to unwind: a Serve that returns an error must leave no
	// listener and no serving goroutine behind, and c.srv is only assigned at
	// the end, so anything left bound here would never be closed.
	if _, err := c.consumerCreds.mint(); err != nil {
		return fmt.Errorf("coord: mint consumer credential: %w", err)
	}
	ln, err := bindPreferring("127.0.0.1", ep.LoopbackPort)
	if err != nil {
		return fmt.Errorf("coord: bind loopback listener: %w", err)
	}
	s.loopback = ln
	s.loopURL = discover.LoopbackURL(ln.Addr().(*net.TCPAddr).Port)
	go func() { _ = s.httpSrv.Serve(ln) }()
	s.saveEndpoint()

	c.srv.Store(s)
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

func (s *coordServing) endpointPath() string {
	return filepath.Join(s.c.stateDir, discover.FileName)
}

// loadEndpoint reads the recorded endpoint state. An ABSENT file is the
// ordinary first start and says nothing; a file that does not decode is
// reported, because silently falling back to the zero state re-picks every port
// ephemerally — the stable re-bindable endpoint an adopted container
// RunnerChannel re-Hellos against is gone, and from the outside that is
// indistinguishable from a first-ever start.
func (s *coordServing) loadEndpoint() endpointState {
	var ep endpointState
	raw, err := os.ReadFile(s.endpointPath())
	if err != nil {
		return ep
	}
	if uerr := json.Unmarshal(raw, &ep); uerr != nil {
		clidiag.Warn("ctxloom", "coordinator: %s does not decode (%v): re-binding on fresh ports, so a relaunched endpoint will not match the recorded one", discover.FileName, uerr)
		return endpointState{}
	}
	return ep
}

// saveEndpoint persists the bound ports and the consumer credential.
func (s *coordServing) saveEndpoint() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveEndpointLocked()
}

// saveEndpointLocked is saveEndpoint with s.mu already held — the form a caller
// inside a locked section uses, so the lock discipline is in the name rather
// than in a `go` that dodges re-entrancy. The write is SYNCHRONOUS: the file is
// on disk before the call that changed the endpoint returns, which is what a
// container child spawned immediately afterwards discovers the coordinator
// through.
func (s *coordServing) saveEndpointLocked() {
	ep := endpointState{WidePort: s.widePort}
	if s.loopback != nil {
		ep.LoopbackPort = s.loopback.Addr().(*net.TCPAddr).Port
	}
	ep.ConsumerCred = s.c.consumerCreds.token()
	raw, _ := json.Marshal(ep)
	if err := os.WriteFile(s.endpointPath(), raw, 0o600); err != nil {
		clidiag.Warn("ctxloom", "coordinator: persist endpoint: %v", err)
	}
}

// LoopbackURL is the coordinator URL for host-side callers (the parent
// harness's runner, host children's runners). Empty until Serve.
//
// test-only: no production call site — production reaches the
// same value through ReachURL("host"). Kept as its own accessor rather than
// deleted because it has a genuine cross-package test consumer
// (internal/mcp/mcp_runner_artifact_test.go), and every one of its 8 call
// sites would need to grow by a line to handle ReachURL's error return,
// which costs more lines than this wrapper.
func (c *Coordinator) LoopbackURL() string {
	srv := c.srv.Load()
	if srv == nil {
		return ""
	}
	return srv.loopURL
}

// ReachURL resolves the URL a caller on runtimeAxis dials: loopback for host
// runs; the widened bridge/host-interface listener for container runs
// (opened on demand, never 0.0.0.0). The hosting glue uses it for the parent
// harness's env trio.
func (c *Coordinator) ReachURL(runtimeAxis agent.RuntimeAxis) (string, error) {
	srv := c.srv.Load()
	if srv == nil {
		return "", errors.New("coordinator listeners are not up")
	}
	// EITHER ownership mode is a container, and both need the widened
	// listener: loopback inside a container is the container's own loopback,
	// so handing one back is not a degraded URL, it is an unreachable one.
	if !agent.IsContainerRuntimeAxis(runtimeAxis) {
		return srv.loopURL, nil
	}
	return srv.ensureWide()
}

// ensureWide resolves the container-reachable URL once and returns it, and it
// is the ONE place the coordinator ever listens past loopback.
//
// On Docker Desktop / Podman Machine GOOS targets (darwin, windows) the
// container runs inside a VM whose networking installs a magic hostname that
// already routes to the host's loopback interface — the coordinator advertises
// that hostname against the EXISTING loopback listener and binds nothing new
// at all.
//
// On Linux — no VM hop, no magic hostname — it binds the bridge-gateway and
// primary-outbound-interface addresses. Those are ROUTABLE host addresses: on a
// rootless/slirp setup the primary outbound IP is the machine's LAN address, so
// this listener is reachable from the LAN, not only from the container. What
// bounds the exposure is not the address but three things that hold together:
// it is never the wildcard 0.0.0.0, it is opened only once a container run
// actually needs it (a host-only session never binds it), and every gRPC stream
// and request on it authenticates against a per-run credential.
//
// "Never 0.0.0.0" is satisfied everywhere. "Nothing LAN-visible" holds on
// darwin/windows and does NOT hold on Linux — the LAN-visible bind is the cost
// of reaching a rootless container, deliberately paid.
func (s *coordServing) ensureWide() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wideURL != "" {
		return s.wideURL, nil
	}
	if host := advertiseHostFor(runtime.GOOS, preferredContainerRuntime()); host != "" {
		port := s.loopback.Addr().(*net.TCPAddr).Port
		s.wideURL = fmt.Sprintf("http://%s%s", net.JoinHostPort(host, fmt.Sprint(port)), MCPPath)
		return s.wideURL, nil
	}
	// Linux (or any GOOS advertiseHostFor didn't special-case): bind
	// container-reachable listeners, most specific first — the container
	// runtime's bridge gateway (rootful daemons — e.g. docker0's
	// 172.17.0.1), then the host's primary outbound interface (rootless
	// slirp/pasta setups reach the host's non-loopback addresses). All
	// candidates bind ONE shared port so a single URL works wherever the
	// packet lands. LABEL (plan): the per-runtime reachability matrix is
	// verified live by the smoke run, not assumed here; an unbindable
	// candidate set fails loudly at the spawn verb.
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
	s.saveEndpointLocked()
	return s.wideURL, nil
}

// close shuts every listener down. Order: Stop the gRPC server FIRST — it is
// served via ServeHTTP (h2c, no separate net.Listener of its own; see
// grpcServer/Serve), so httpSrv.Shutdown's own listener-close does not touch
// it, and it is the ONLY thing that actually signals an in-flight
// RunChannel/RunnerChannel/artifact-transfer handler to stop — those streams
// key off their own gRPC-transport context, not c.baseCtx (Coordinator.
// Close's cancel does not reach them). Without this, srv.close previously
// just closed the listeners and left every live streaming handler running
// until its client side happened to disconnect — exactly the "in-flight
// streaming handlers keep running" gap this diagnosed.
//
// Stop(), not GracefulStop(): grpc-go's ServeHTTP path (the only path this
// server ever runs — see grpcServer's doc) wraps each connection in a
// serverHandlerTransport, whose Drain() is UNCONDITIONALLY
// `panic("Drain() is not implemented")` (internal/transport/handler_server.
// go) — GracefulStop calls exactly that and crashes the process (confirmed
// live: `just test-pkg` panicked here before this was pinned to Stop()).
// RunChannel/RunnerChannel are perpetual streams anyway (they never
// "finish" on their own), so a graceful drain would never resolve even if it
// didn't panic — a hard Stop is the only correct choice for this transport.
func (s *coordServing) close() {
	if s.grpcSrv != nil {
		s.grpcSrv.Stop()
	}
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

// advertiseHostFor returns the magic hostname a containerized child on GOOS
// goos resolves to reach the coordinator's host-loopback listener, for the
// given container-runtime name ("docker" | "podman" | anything else,
// treated as docker) — or "" when goos needs the Linux bridge-
// gateway/primary-outbound-IP path instead (ensureWide's unchanged
// fallback).
//
// Docker Desktop (macOS/Windows) and Podman Machine both run containers
// inside a lightweight VM (gvisor-tap-vsock) whose networking installs a
// magic DNS name resolving to the VM's host-forwarding endpoint — traffic
// reaches the host's 127.0.0.1 without the coordinator ever widening past
// loopback. Linux containers share the host kernel directly (no VM hop), so
// neither name resolves there; "" tells ensureWide to keep doing what
// already works on Linux.
func advertiseHostFor(goos, runtimeName string) string {
	switch goos {
	case "darwin", "windows":
		if runtimeName == "podman" {
			return "host.containers.internal"
		}
		return "host.docker.internal"
	default:
		return ""
	}
}

// preferredContainerRuntime reports which container CLI this host has
// available — "docker" preferred when both are on PATH (containerReachIPs
// probes in the same docker-then-podman order), "docker" also the default
// when neither is found (host.docker.internal is the far more common Docker-
// Desktop mapping, and a wrong guess here only affects the advertised
// hostname string, not whether the coordinator binds anything wider than
// loopback).
func preferredContainerRuntime() string {
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker"
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return "podman"
	}
	return "docker"
}

// containerReachIPs collects candidate host IPs a container can dial,
// most specific first: each detected container runtime's default bridge
// gateway, then the host's primary outbound interface IP. Every candidate must
// PARSE as an address: the gateway probes are text/template expressions the
// runtime evaluates, and a runtime that renames a field, errors mid-render, or
// prints a diagnostic on stdout answers with prose, not an address. Prose comes
// in more shapes than the "<no value>" sentinel a missing template field
// renders to, so the test is that it PARSES — nothing else may become an
// address the coordinator binds and advertises to a container.
func containerReachIPs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		ip := strings.TrimSpace(raw)
		if net.ParseIP(ip) == nil || seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, ip)
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
