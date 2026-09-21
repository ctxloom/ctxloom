package coord

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Transport is the coordinator's wire once its listeners are up: the
// addresses a runner is handed to dial home, and the teardown. The wire
// adapter binds one through BindTransport when it serves; the spawn path
// reads the reach address through it (ReachURL), and Close closes it.
// Nothing here names a listener, a port or a protocol — those are the
// adapter's.
type Transport interface {
	// LoopbackURL is the host-side address ("" before any listener is up).
	LoopbackURL() string
	// ReachURL resolves the address a caller on runtimeAxis dials: loopback
	// for host runs, the container-reachable listener for container runs
	// (opened on demand by the adapter, never 0.0.0.0).
	ReachURL(runtimeAxis launch.RuntimeAxis) (string, error)
	// Close tears the listeners down.
	Close()
}

// ErrNotServing answers a reach-address request before any transport is
// bound.
var ErrNotServing = errors.New("coordinator listeners are not up")

// BindTransport publishes the wire the coordinator serves through. A
// transport already bound stays (binding is idempotent: a second Serve is
// not a new admission); a coordinator that is draining or closed refuses
// (ErrDraining, ErrClosed), and the refusal is the caller's cue to unwind
// the listeners it bound — nothing published means nothing Close will take
// down. ATOMIC with Close: a transport published after Close ran would keep
// its listeners for the life of the process.
func (c *Coordinator) BindTransport(t Transport) error {
	if c.Draining() {
		return ErrDraining
	}
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if c.closed.Load() {
		return ErrClosed
	}
	if c.transport == nil {
		c.transport = t
	}
	return nil
}

// Serving reports whether a transport is bound.
func (c *Coordinator) Serving() bool {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	return c.transport != nil
}

// takeTransport unbinds the transport for Close to tear down.
func (c *Coordinator) takeTransport() Transport {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	t := c.transport
	c.transport = nil
	return t
}

// Transport is the bound wire, nil until the adapter serves.
func (c *Coordinator) Transport() Transport {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	return c.transport
}

// LoopbackURL is the coordinator URL for host-side callers (the parent
// harness's runner, host children's runners). Empty until served.
func (c *Coordinator) LoopbackURL() string {
	t := c.Transport()
	if t == nil {
		return ""
	}
	return t.LoopbackURL()
}

// ReachURL resolves the URL a caller on runtimeAxis dials — the spawn path
// uses it for the runner's env trio.
func (c *Coordinator) ReachURL(runtimeAxis launch.RuntimeAxis) (string, error) {
	t := c.Transport()
	if t == nil {
		return "", ErrNotServing
	}
	return t.ReachURL(runtimeAxis)
}

// StateDir is the coordinator's state directory — where the wire adapter
// records the endpoint it bound, beside the journals.
func (c *Coordinator) StateDir() string { return c.stateDir }

// MintConsumerCredential mints the read-only consumer credential for this
// process, replacing any prior one. It is never journaled: the wire adapter
// persists it beside the endpoint it records, which is a viewer's one
// discovery point for both.
func (c *Coordinator) MintConsumerCredential() (string, error) {
	return c.consumerCreds.mint()
}

// ConsumerCredential is the current consumer credential ("" before mint).
func (c *Coordinator) ConsumerCredential() string { return c.consumerCreds.token() }

// EnterStream admits one wire stream handler under the coordinator's seal:
// ok is false once Close has begun, so a handler that arrives late is
// refused rather than counted after the join it would have been part of.
// The returned done is registered FIRST by the handler so it runs LAST,
// after the handler's own teardown.
func (c *Coordinator) EnterStream() (done func(), ok bool) { return c.streams.enter() }

// Track runs fn on a goroutine Close joins — the stream pumps and receive
// loops the wire adapter runs on the coordinator's behalf.
func (c *Coordinator) Track(fn func()) { c.goTracked(fn) }

// Reporter is the coordinator's diagnostic sink, for the wire adapter to
// report through beside it.
func (c *Coordinator) Reporter() report.Reporter { return c.rep }

// HashToken is the credential hash the coordinator keys a runner by.
func HashToken(token string) string { return hashToken(token) }
