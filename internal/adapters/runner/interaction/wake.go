package interaction

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

var (
	// ErrNoWakeSubscriber refuses a wake no relay is subscribed to receive:
	// the notification would go nowhere, and the caller must learn the
	// owner was not woken.
	ErrNoWakeSubscriber = errors.New("runner interaction: no session relay is subscribed to the wake")
	// ErrNotSubscribable refuses a subscription to anything but engine.WakeURI.
	ErrNotSubscribable = errors.New("runner interaction: only the wake can be subscribed to")
	// ErrNoWakeSignal refuses to serve without the wake signal: a relay's
	// subscription would have nothing to register with.
	ErrNoWakeSignal = errors.New("runner interaction: no wake signal to serve the wake subscription with")
)

// WakeSignal is the runner's half of an engine wake: the subscriptions to
// engine.WakeURI on the session's endpoint, and Fire, which notifies them. It is an
// engine.Wake, so the runner fires it exactly as it would fire any bound
// wake; what makes the turn start is the relay's.
//
// A relay's subscription IS the binding: the first subscriber registers this
// signal as the session owner's wake (register — runner.Home.SetWake in
// production), and the release runs when the last subscriber unsubscribes or
// the endpoint is served again. A relay that dies without unsubscribing
// leaves the registration in place, which costs nothing: Fire reads liveness
// from the server and fails when no live subscriber remains, and a respawned
// relay's subscription finds the signal still registered.
type WakeSignal struct {
	mu       sync.Mutex
	server   *mcp.Server
	subs     map[*mcp.ServerSession]bool
	register func(engine.Wake) (release func())
	release  func()
}

// NewWakeSignal returns a signal no endpoint serves yet, which registers
// itself with register while a relay is subscribed. A nil register registers
// nowhere (a surface served only to be documented).
func NewWakeSignal(register func(engine.Wake) (release func())) *WakeSignal {
	return &WakeSignal{subs: map[*mcp.ServerSession]bool{}, register: register}
}

// serve makes server the one this signal fires on. A later serve (a rebound
// incarnation of the endpoint) replaces the earlier server, forgets its
// subscribers and releases their registration.
func (w *WakeSignal) serve(server *mcp.Server) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.server = server
	w.subs = map[*mcp.ServerSession]bool{}
	w.releaseLocked()
}

// releaseLocked releases the registration, if any. Caller holds w.mu.
func (w *WakeSignal) releaseLocked() {
	if w.release != nil {
		w.release()
		w.release = nil
	}
}

// options installs the subscription handlers, which admit engine.WakeURI alone.
func (w *WakeSignal) options(opts *mcp.ServerOptions) {
	opts.SubscribeHandler = func(_ context.Context, req *mcp.SubscribeRequest) error {
		if req.Params.URI != engine.WakeURI {
			return fmt.Errorf("%w: %q", ErrNotSubscribable, req.Params.URI)
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		w.subs[req.Session] = true
		if w.release == nil && w.register != nil {
			w.release = w.register(w)
		}
		return nil
	}
	opts.UnsubscribeHandler = func(_ context.Context, req *mcp.UnsubscribeRequest) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.subs, req.Session)
		if len(w.subs) == 0 {
			w.releaseLocked()
		}
		return nil
	}
}

// Fire notifies every live subscriber of engine.WakeURI with the nonce. A
// subscriber whose session has since closed is not one: the SDK drops a
// closed session silently, so liveness is read from the server, not from
// this record. Delivery itself is unacknowledged by design (the runner's
// alarm on an unredeemed nonce is the backstop).
func (w *WakeSignal) Fire(ctx context.Context, nonce string) error {
	w.mu.Lock()
	server, live := w.server, 0
	if server != nil {
		for ss := range server.Sessions() {
			if w.subs[ss] {
				live++
			}
		}
	}
	w.mu.Unlock()
	if live == 0 {
		return ErrNoWakeSubscriber
	}
	return server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: engine.WakeURI, Meta: mcp.Meta{"nonce": nonce}})
}

var _ engine.Wake = (*WakeSignal)(nil)
