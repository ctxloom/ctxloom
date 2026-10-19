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

	"github.com/spf13/afero"
	"google.golang.org/grpc"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/hostnet"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	// addresses already reported. holders is, per address, the runs that need
	// it: the last one to end closes the listener (Unlisten). hostOnly marks
	// the addresses that refuse non-host peers, recorded so a relaunch re-opens
	// them the same way.
	extra    map[string]net.Listener
	holders  map[string]map[string]struct{}
	hostOnly map[string]bool
	warned   map[string]bool
}

// The server's connection bounds. ReadHeaderTimeout closes a peer that opens a
// connection and never finishes its request headers. IdleTimeout closes a
// keep-alive connection that holds no request: an HTTP/2 connection with an
// open stream is not idle, so the perpetual RunChannel/RunnerChannel streams
// are unaffected, and a viewer's gRPC client redials an idle connection that
// was closed.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 5 * time.Minute
)

// newHTTPServer is the coordinator's http.Server around h: h2c alongside
// HTTP/1 (which h answers 404), with the connection bounds above.
func newHTTPServer(h http.Handler) *http.Server {
	// Unencrypted HTTP/2 (h2c) via net/http's Protocols — the modern
	// replacement for the deprecated x/net/http2/h2c wrapper.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Handler:           h,
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
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
	s := &coordServing{
		c:        c,
		extra:    map[string]net.Listener{},
		holders:  map[string]map[string]struct{}{},
		hostOnly: map[string]bool{},
		warned:   map[string]bool{},
	}

	grpcSrv := grpcServer(c)
	s.grpcSrv = grpcSrv
	gated := authGate(c, grpcSrv)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gRPC (RunnerChannel/RunChannel) is plaintext HTTP/2 with the grpc
		// content-type. Nothing else is served here — the MCP tool path
		// terminates at each runner's local
		// socket; per-request credential auth lives in the gRPC
		// interceptors.
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gated.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	// No bind is ever 0.0.0.0; the credential authenticates every gRPC stream
	// and request.
	s.httpSrv = newHTTPServer(s.handler)

	ep := s.loadEndpoint()
	// Mint the consumer-class watch credential fresh for this process
	// and persist it into endpoint.json ALONGSIDE the ports it's saved
	// with — the file is a viewer's one discovery point for both.
	c.MintConsumerCredential()
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
	// may never come — or the run it holds can only end as runner loss. Only
	// while such a run is live: with none, nothing would dial the address, and
	// a listener held for nobody is exposure for nothing. The adopted runs hold
	// it, so the last of them to end closes it.
	if live := c.LiveContainerRunIDs(); len(live) > 0 {
		hostOnly := map[string]bool{}
		for _, addr := range ep.HostPeersOnlyAddrs {
			hostOnly[addr] = true
		}
		for _, addr := range ep.ListenAddrs {
			for _, runID := range live {
				if err := s.Listen(present.Listen{Addr: addr, HostPeersOnly: hostOnly[addr]}, runID); err != nil {
					c.Reporter().Warnf("coord: re-open the container listener recorded on %s: %v (a container runner from before the restart cannot be re-adopted)", addr, err)
					break
				}
			}
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
	for addr := range s.extra {
		if s.hostOnly[addr] {
			ep.HostPeersOnlyAddrs = append(ep.HostPeersOnlyAddrs, addr)
		}
	}
	sort.Strings(ep.HostPeersOnlyAddrs)
	if s.loopback != nil {
		ep.LoopbackPort = s.loopback.Addr().(*net.TCPAddr).Port
	}
	ep.ConsumerCred = s.c.ConsumerCredential()
	// The consumer credential's identity is stamped with the one project
	// this coordinator serves (Coordinator.Identify).
	owner, _ := s.c.Identify(ep.ConsumerCred)
	ep.ProjectDir = owner.ProjectDir
	if err := writeEndpoint(afero.NewOsFs(), s.endpointPath(), ep); err != nil {
		s.c.Reporter().Warnf("coordinator: persist endpoint: %v", err)
	}
}

// writeEndpoint replaces endpoint.json whole and owner-only: a reader (a
// relaunched coordinator, a viewer) sees the old file or the new one, never a
// torn one, and a file left looser than owner-only does not keep its mode —
// it carries the consumer credential.
func writeEndpoint(fs afero.Fs, path string, ep endpointState) error {
	raw, err := json.Marshal(ep)
	if err != nil {
		return err
	}
	return safefs.WriteFile(fs, path, raw, safefs.PrivateFileMode)
}

// LoopbackURL is the loopback listener's URL — the coordinator's
// Transport.LoopbackURL.
func (s *coordServing) LoopbackURL() string { return s.loopURL }

// Listen opens the listener a cell named, on the loopback listener's port —
// the coordinator's Transport.Listen — and records holder (the run that needs
// it) against the address; Unlisten closes it when the last holder is gone.
// The cell's runtime chose the address (a private bridge gateway, or the
// public fallback); this knows no runtime. A public address is reported once:
// reachable beyond this host, its every stream still authenticates against a
// per-run credential. A listener the cell marked HostPeersOnly refuses every
// peer that is not one of this host's addresses.
func (s *coordServing) Listen(l present.Listen, holder string) error {
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
		served := ln
		if l.HostPeersOnly {
			addr := l.Addr
			served = newHostPeerListener(ln, hostnet.IsLocalAddr, func(remote string) {
				s.c.Reporter().Warnf("coordinator: refused a connection to %s from %s, which is not an address of this host", addr, remote)
			})
			s.hostOnly[l.Addr] = true
		}
		go func() { _ = s.httpSrv.Serve(served) }()
		s.extra[l.Addr] = ln
		s.holders[l.Addr] = map[string]struct{}{}
		s.saveEndpointLocked()
	}
	s.holders[l.Addr][holder] = struct{}{}
	if l.Public && !s.warned[l.Addr] {
		s.warned[l.Addr] = true
		s.c.Reporter().Warnf("coordinator: listening on %s, which is reachable beyond this host, because %s; the endpoint is token-protected (every stream authenticates with a per-run credential)", l.Addr, l.Why)
	}
	return nil
}

// Unlisten drops holder's claim on every address Listen opened for it, and
// closes each one nobody else needs, removing it from endpoint.json so the
// recorded set stays the one actually open.
func (s *coordServing) Unlisten(holder string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	closed := false
	for addr, held := range s.holders {
		if _, ok := held[holder]; !ok {
			continue
		}
		delete(held, holder)
		if len(held) > 0 {
			continue
		}
		_ = s.extra[addr].Close()
		delete(s.extra, addr)
		delete(s.holders, addr)
		delete(s.hostOnly, addr)
		delete(s.warned, addr)
		closed = true
	}
	if closed {
		s.saveEndpointLocked()
	}
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
