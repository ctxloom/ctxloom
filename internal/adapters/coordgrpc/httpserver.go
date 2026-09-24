package coordgrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// MCPPath is retained in the advertised CTXLOOM_COORD_URL shape
// (http://<host>:<port>/mcp) for continuity — the runner derives its gRPC
// dial target from the URL's host:port. No HTTP-MCP handler is served on it:
// the gRPC RunChannel is the only agent ingress; the h2c listener carries the
// gRPC planes alone.
// Declared in discover, which reads it back out of endpoint.json: advertised
// path and discovered path are one constant.
const MCPPath = discover.MCPPath

// coordServing is the coordinator's listener set: the loopback listener, plus
// any listener a container cell's re-minted reach named (Listen) — a private
// bridge gateway, or the public fallback — on the loopback listener's port,
// never 0.0.0.0.
// One plaintext-HTTP/2 (h2c) listener carries the gRPC channels; non-gRPC
// requests answer 404 (the tool surface lives at each RUNNER's local
// socket).
type coordServing struct {
	c       *coord.Coordinator
	handler http.Handler
	httpSrv *http.Server
	grpcSrv *grpc.Server

	mu       sync.Mutex
	loopback net.Listener
	loopURL  string
	// extra holds the listeners Listen opened, by address; warned the public
	// addresses already reported.
	extra  map[string]net.Listener
	warned map[string]bool
}

// endpointState persists the bound ports so a relaunched coordinator
// re-binds the SAME endpoint (acceptance (4): adopted container
// RunnerChannels re-Hello against a stable re-bindable endpoint).
// ConsumerCred is how an out-of-process viewer (the TUI) discovers the
// read-only watch credential — 0600, host-local, re-minted every Serve()
// (consumer.go's consumerCreds is never journaled, so this file IS its only
// persistence).
// The layout is discover.State: the reader cannot import this package (it is
// upstream through internal/adapters/operations), so the shape lives with the reader and
// a renamed field breaks the build rather than discovery, where a mismatch is
// indistinguishable from "no coordinator is running".
type endpointState = discover.State

// Serve stands the coordinator's listeners up: loopback, plus any address a
// container cell names later (Listen). The bound listener set is
// published to the coordinator as its coord.Transport; a second Serve on a
// serving coordinator is a no-op.
func Serve(c *coord.Coordinator) error {
	if c.Serving() {
		return nil // already serving: idempotent no-op, not a new admission
	}
	if c.Draining() {
		return fmt.Errorf("coord: %w", coord.ErrDraining)
	}
	s := &coordServing{c: c, extra: map[string]net.Listener{}, warned: map[string]bool{}}

	grpcSrv := grpcServer(c)
	s.grpcSrv = grpcSrv
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gRPC (RunnerChannel/RunChannel) is plaintext HTTP/2 with the grpc
		// content-type. Nothing else is served here — the MCP tool path
		// terminates at each runner's local
		// socket; per-request credential auth lives in the gRPC
		// interceptors.
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	// Unencrypted HTTP/2 (h2c) via net/http's Protocols — the modern
	// replacement for the deprecated x/net/http2/h2c wrapper. No bind is ever
	// 0.0.0.0; the credential authenticates
	// every gRPC stream and request.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	s.httpSrv = &http.Server{Handler: s.handler, Protocols: protocols}

	ep := s.loadEndpoint()
	// Mint the consumer-class watch credential fresh for this process
	// and persist it into endpoint.json ALONGSIDE the ports it's saved
	// with — the file is a viewer's one discovery point for both. Minted
	// BEFORE anything is bound so that every step which can fail runs while
	// there is nothing to unwind: a Serve that returns an error must leave no
	// listener and no serving goroutine behind, and the transport is only
	// bound (BindTransport) at the end, so anything left bound here would
	// never be closed.
	if _, err := c.MintConsumerCredential(); err != nil {
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

	if err := c.BindTransport(s); err != nil {
		// Close ran while this was binding and found nothing to take down:
		// the listeners are this call's to unwind.
		s.Close()
		return fmt.Errorf("coord: %w", err)
	}
	// A previous incarnation listened beyond loopback for a container run; a
	// container runner from before the restart redials that recorded address
	// and port, so it is re-bound NOW — not on the next container spawn, which
	// may never come — or the run it holds can only end as runner loss.
	for _, addr := range ep.ListenAddrs {
		if err := s.Listen(present.Listen{Addr: addr}); err != nil {
			c.Reporter().Warnf("coord: re-open the container listener recorded on %s: %v (a container runner from before the restart cannot be re-adopted)", addr, err)
		}
	}
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
	return filepath.Join(s.c.StateDir(), discover.FileName)
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
		s.c.Reporter().Warnf("coordinator: %s does not decode (%v): re-binding on fresh ports, so a relaunched endpoint will not match the recorded one", discover.FileName, uerr)
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
	ep := endpointState{}
	for addr := range s.extra {
		ep.ListenAddrs = append(ep.ListenAddrs, addr)
	}
	sort.Strings(ep.ListenAddrs)
	if s.loopback != nil {
		ep.LoopbackPort = s.loopback.Addr().(*net.TCPAddr).Port
	}
	ep.ConsumerCred = s.c.ConsumerCredential()
	raw, _ := json.Marshal(ep)
	if err := os.WriteFile(s.endpointPath(), raw, 0o600); err != nil {
		s.c.Reporter().Warnf("coordinator: persist endpoint: %v", err)
	}
}

// LoopbackURL is the loopback listener's URL — the coordinator's
// Transport.LoopbackURL.
func (s *coordServing) LoopbackURL() string { return s.loopURL }

// Listen opens the listener a container cell named, on the loopback
// listener's port — the coordinator's Transport.Listen. The cell's runtime
// chose the address (a private bridge gateway, or the public fallback); this
// knows no runtime. A public address is reported once: reachable beyond this
// host, its every stream still authenticates against a per-run credential.
func (s *coordServing) Listen(l present.Listen) error {
	if l.Addr == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.extra[l.Addr]; !ok {
		port := s.loopback.Addr().(*net.TCPAddr).Port
		ln, err := net.Listen("tcp", net.JoinHostPort(l.Addr, fmt.Sprint(port)))
		if err != nil {
			return fmt.Errorf("coord: listen on %s for a container runner: %w", l.Addr, err)
		}
		go func() { _ = s.httpSrv.Serve(ln) }()
		s.extra[l.Addr] = ln
		s.saveEndpointLocked()
	}
	if l.Public && !s.warned[l.Addr] {
		s.warned[l.Addr] = true
		s.c.Reporter().Warnf("coordinator: listening on %s, which is reachable beyond this host, because %s; the endpoint is token-protected (every stream authenticates with a per-run credential)", l.Addr, l.Why)
	}
	return nil
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
func (s *coordServing) Close() {
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
	for _, ln := range s.extra {
		_ = ln.Close()
	}
}
