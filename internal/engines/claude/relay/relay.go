// Package relay is claude's session relay: the stdio MCP server claude
// spawns as its ctxloom entry (claude.RelayCommand). It exists because of
// who spawns it, not because of what it serves. Every ctxloom tool is served
// by the session's endpoint inside the runner, which is claude's ANCESTOR;
// claude admits a cross-session post as its own session's only from its
// DESCENDANT holding what claude exported to it. So the relay does two
// things and knows no tool:
//
//   - it relays MCP, message for message, between claude's stdio and the
//     session's endpoint (the runner's interaction endpoint, over HTTP with
//     the bearer), so every tool keeps its name and its behaviour;
//   - it subscribes to the endpoint's unlisted wake (engine.WakeURI) and, on
//     each wake, fires claude's declared wake, bound from the environment
//     claude handed this process.
//
// It is claude's tool and no other engine's: only claude's definition
// renders the entry that spawns it.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// Config is what the relay reads from its process.
type Config struct {
	// Env is the relay's own environment (os.LookupEnv): the endpoint claude's
	// entry handed it, and what claude exported for its wake.
	Env engine.WakeEnv
	// Stderr carries the relay's diagnostics; claude keeps an MCP server's
	// stderr in its own logs. Never stdout: stdout is the MCP channel.
	Stderr io.Writer
	// KeepAlive is how often the relay pings each of its endpoint sessions;
	// zero is KeepAliveInterval.
	KeepAlive time.Duration
}

// KeepAliveInterval is how often the relay pings each of its sessions on the
// endpoint. The endpoint closes a session no request reached for
// interaction.IdleSessionTimeout, and only a request resets that clock: the
// wake session asks nothing after subscribing, and claude's own session sits
// idle for as long as the human does. A closed session answers 404, which the
// MCP client treats as terminal: the wake would be lost, and every later
// tool call refused, until claude restarts the relay. Pinging well inside
// the timeout keeps both sessions as alive as the relay is.
//
// A ping carries no deadline of its own (keepUp): the endpoint holds a
// session's idle clock while any request on it is in flight, so a ping that
// is slow to answer costs the session nothing, and only the endpoint ending
// the session loses it. A keepalive that gave up on a slow ping would close
// a live session itself — and with it the wake's subscription.
const KeepAliveInterval = time.Hour

// keepAlive is KeepAlive, or KeepAliveInterval when it is zero.
func (c Config) keepAlive() time.Duration {
	if c.KeepAlive == 0 {
		return KeepAliveInterval
	}
	return c.KeepAlive
}

// ErrNoEndpoint refuses a relay whose entry named no endpoint to relay to.
var ErrNoEndpoint = errors.New("claude relay: no session endpoint to relay to")

// ErrEndpointRefused is the session endpoint answering 401 to the relay's
// bearer. Every launch mints its own endpoint and bearer, so a refusal means
// this relay holds another launch's bearer, typically one that outlived its
// runner. A retry cannot fix that; only the current launch's config can.
var ErrEndpointRefused = errors.New("claude relay: the session endpoint refused this relay's bearer")

// Run relays until claude closes down (its stdio), which is the relay's
// lifetime, and returns nil then; it returns an error when the endpoint
// stops answering, and ErrEndpointRefused when it refused the bearer.
func Run(ctx context.Context, cfg Config, down mcp.Transport) error {
	url, _ := cfg.Env(claude.EnvRelayURL)
	bearer, _ := cfg.Env(claude.EnvRelayBearer)
	if url == "" || bearer == "" {
		return fmt.Errorf("%w: %s and %s must both be set", ErrNoEndpoint, claude.EnvRelayURL, claude.EnvRelayBearer)
	}
	ctx, cancel := context.WithCancel(ctx)
	refused := &atomic.Bool{}
	httpc := &http.Client{Transport: bearerTransport{bearer: bearer, refused: refused}}
	watched := make(chan struct{})
	go func() { defer close(watched); watchWake(ctx, cfg, url, httpc) }()
	err := pump(ctx, down, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpc}, cfg.keepAlive())
	// The relay's process exits when Run returns, so the wake subscription
	// must be closed by then: otherwise the endpoint keeps counting a
	// subscriber that is gone, and a wake fires into nothing.
	cancel()
	<-watched
	// The pump's error is the MCP client's, which does not type which status
	// failed the connection; only the transport saw that it was a 401.
	if refused.Load() {
		return errors.Join(fmt.Errorf("%w: %s", ErrEndpointRefused, url), err)
	}
	return err
}

// bearerTransport puts the endpoint's bearer on every request. An HTTP error
// names the URL, never the headers, so the bearer reaches no diagnostic.
//
// A 401 is RECORDED in refused and the response passed through, rather than
// returned as an error: the MCP client wraps a round-trip error as a
// transient rejection that keeps the connection open, so the relay would
// never end, while a 401 response fails the connection.
type bearerTransport struct {
	bearer  string
	refused *atomic.Bool
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+b.bearer)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		b.refused.Store(true)
	}
	return resp, err
}

// pump connects both sides and relays each way until one ends.
//
// A side that ends cleanly does not make the relay's end clean: claude closes
// its side BECAUSE the endpoint failed (relayUp answered a call with the
// failure), and that close can reach the pump before the endpoint-to-claude
// side reads the failure, which it may not be reading at all while it is busy
// writing to claude. So a clean end still reports what the endpoint holds.
func pump(ctx context.Context, down, up mcp.Transport, keepAlive time.Duration) error {
	dc, err := down.Connect(ctx)
	if err != nil {
		return fmt.Errorf("claude relay: connecting claude's stdio: %w", err)
	}
	defer dc.Close()
	uc, err := up.Connect(ctx)
	if err != nil {
		return fmt.Errorf("claude relay: connecting the session endpoint: %w", err)
	}
	defer uc.Close()
	ended := make(chan error, 2)
	go func() { ended <- upward(ctx, dc, uc, keepAlive) }()
	go func() { ended <- downward(ctx, uc, dc) }()
	if err := <-ended; err != nil {
		return err
	}
	return endpointFailure(uc)
}

// endpointFailure returns the failure the endpoint's connection holds, or nil
// when it holds none. It asks with a context already done, so it never waits:
// a failed connection answers a read with its failure before it looks at the
// context, and a healthy one answers with the context's error (or a message
// nobody is left to relay).
func endpointFailure(uc mcp.Connection) error {
	probe, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := uc.Read(probe)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("claude relay: the session endpoint stopped answering: %w", err)
}

// upward relays claude's messages to the endpoint, and returns nil when
// claude closes its side.
//
// A call is written on its own goroutine. The endpoint sends a call's
// response headers only with its first message, so a write returns only once
// the call starts answering: written in line, one long call (a parked
// receive) would hold back every later message — including the cancellation
// of that very call. Nothing orders a call after initialize here: an MCP
// client sends nothing after the initialize call until its result arrives,
// and the result reaches claude only once the transport holds the session id.
//
// Until claude sends initialize there is no session on the endpoint, and the
// endpoint refuses a call without one in a way that ends the transport for
// good. Claude opens a stdio server with such calls (server/discover,
// measured on claude 2.1.286), so the relay answers any call before
// initialize itself: method not found, what a stdio server answers to a
// method it does not have, and what claude falls back from. For the same
// reason the relay's keepalive starts only once initialize was written: the
// write returns when the endpoint answered it, so the session id is held.
func upward(ctx context.Context, dc, uc mcp.Connection, keepAlive time.Duration) error {
	initialized := false
	for {
		msg, err := dc.Read(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claude relay: reading claude's stdio: %w", err)
		}
		initialized = dispatchUp(ctx, dc, uc, msg, initialized, keepAlive)
	}
}

// dispatchUp routes one of claude's messages (upward) and reports whether
// claude has now sent initialize.
func dispatchUp(ctx context.Context, dc, uc mcp.Connection, msg jsonrpc.Message, initialized bool, keepAlive time.Duration) bool {
	req, isReq := msg.(*jsonrpc.Request)
	initializing := !initialized && isReq && req.Method == "initialize"
	switch {
	case initializing:
		go func() {
			relayUp(ctx, dc, uc, req)
			keepUp(ctx, keepAlive, func(ctx context.Context) error {
				return uc.Write(ctx, &jsonrpc.Request{ID: keepAliveID, Method: "ping"})
			})
		}()
	case !initialized:
		answerUninitialized(ctx, dc, req, isReq)
	case isReq && req.IsCall():
		go relayUp(ctx, dc, uc, req)
	default:
		relayUp(ctx, dc, uc, msg)
	}
	return initialized || initializing
}

// answerUninitialized answers a call that arrived before initialize; there is
// nothing to answer to anything else.
func answerUninitialized(ctx context.Context, dc mcp.Connection, req *jsonrpc.Request, isReq bool) {
	if !isReq || !req.IsCall() {
		return
	}
	_ = dc.Write(ctx, &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{
		Code:    jsonrpc.CodeMethodNotFound,
		Message: fmt.Sprintf("ctxloom relay: %q is not served before initialize", req.Method),
	}})
}

// relayUp writes one message upward. A call that could not be written is
// answered with an error, so claude is not left waiting on a response that
// will never come. A notification or response that could not be written has
// nobody to answer; the transport fails the connection on any failure that
// is not transient, and every later call is then answered with that error.
func relayUp(ctx context.Context, dc, uc mcp.Connection, msg jsonrpc.Message) {
	err := uc.Write(ctx, msg)
	if err == nil {
		return
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok || !req.IsCall() {
		return
	}
	_ = dc.Write(ctx, &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{
		Code:    jsonrpc.CodeInternalError,
		Message: fmt.Sprintf("ctxloom relay: the session endpoint did not take %q: %v", req.Method, err),
	}})
}

// keepAliveID is the id of the relay's own pings: their responses are the
// relay's, and never reach claude.
var keepAliveID, _ = jsonrpc.MakeID("ctxloom-relay-keepalive")

// keepUp pings one session every interval until ctx ends or a ping fails, so
// the session outlives the endpoint's idle timeout however long it is idle.
// Each ping waits for as long as the endpoint takes (KeepAliveInterval).
// What a failed ping means is the caller's: on claude's session it fails the
// connection, which downward then reports, so a lost session ends the relay
// loudly; on the wake's it closes the session, which watchWake reports.
func keepUp(ctx context.Context, interval time.Duration, ping func(context.Context) error) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := ping(ctx); err != nil {
			return
		}
	}
}

// downward relays the endpoint's messages to claude, except the answers to
// the relay's own pings.
func downward(ctx context.Context, uc, dc mcp.Connection) error {
	for {
		msg, err := uc.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claude relay: the session endpoint stopped answering: %w", err)
		}
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.ID == keepAliveID {
			continue
		}
		if err := dc.Write(ctx, msg); err != nil {
			return nil
		}
	}
}

// The diagnostics watchWake prints when its session closes under it — the
// endpoint ended it, or a keepalive ping failed and the client closed it.
// The wake is then gone until something subscribes again, and nothing else
// would notice: a runner firing it learns only that nobody is subscribed.
const (
	wakeLost = "the wake subscription was lost; subscribing again, once"
	wakeDown = "the wake subscription was lost again; this session can no longer be woken"
)

// watchWake binds claude's wake from the relay's environment and, when it
// binds, subscribes to the endpoint's wake on a session of its own and fires
// the wake for each notification. A relay that cannot bind says so once and
// never subscribes: the runner then learns, when it fires, that nobody can
// wake this session. A lost subscription is said so and taken again ONCE, on
// a fresh session; a second loss, or a resubscription that fails, leaves the
// wake down with its diagnostic printed — a retry loop against an endpoint
// that went away would only repeat it.
func watchWake(ctx context.Context, cfg Config, url string, httpc *http.Client) {
	client, ok := wakeClient(ctx, cfg)
	if !ok {
		return
	}
	for _, lost := range []string{wakeLost, wakeDown} {
		cs, ok := subscribeWake(ctx, cfg, client, url, httpc)
		if !ok || !awaitLoss(ctx, cs, cfg.keepAlive()) {
			return
		}
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: %s\n", claude.RelayCommand, lost)
	}
}

// wakeClient binds claude's wake and returns the MCP client whose sessions
// carry it; false, said so, when the wake cannot be bound.
func wakeClient(ctx context.Context, cfg Config) (*mcp.Client, bool) {
	spec, _ := claude.Claude{}.Wake().Get()
	w, err := spec.Bind(ctx, cfg.Env)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: this session cannot be woken: %v\n", claude.RelayCommand, err)
		return nil, false
	}
	return mcp.NewClient(&mcp.Implementation{Name: "ctxloom-" + claude.RelayCommand, Version: version.Version}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			nonce, _ := req.Params.Meta["nonce"].(string)
			if err := w.Fire(ctx, nonce); err != nil {
				fmt.Fprintf(cfg.Stderr, "ctxloom %s: wake %s was not posted: %v\n", claude.RelayCommand, nonce, err)
			}
		},
	}), true
}

// subscribeWake opens a session for the wake and subscribes on it; false,
// said so, when either fails.
func subscribeWake(ctx context.Context, cfg Config, client *mcp.Client, url string, httpc *http.Client) (*mcp.ClientSession, bool) {
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpc}, nil)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: cannot reach the session endpoint for the wake: %v\n", claude.RelayCommand, err)
		return nil, false
	}
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: engine.WakeURI}); err != nil {
		_ = cs.Close()
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: cannot subscribe to the wake: %v\n", claude.RelayCommand, err)
		return nil, false
	}
	return cs, true
}

// awaitLoss holds cs, pinging it every keepAlive, until ctx ends (false) or
// the session closes under it (true), and closes it either way. A ping that
// failed closes it: the endpoint answered that the session is gone.
//
// The session is kept up by keepUp, never by mcp.ClientOptions.KeepAlive:
// go-sdk's keepalive abandons a ping after half the interval and closes the
// session, so an endpoint merely slow to answer would cost the wake its
// subscription.
func awaitLoss(ctx context.Context, cs *mcp.ClientSession, keepAlive time.Duration) bool {
	closed := make(chan struct{})
	go func() { _ = cs.Wait(); close(closed) }()
	pinging, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		keepUp(pinging, keepAlive, func(ctx context.Context) error { return cs.Ping(ctx, nil) })
		_ = cs.Close()
	}()
	defer cs.Close()
	select {
	case <-ctx.Done():
		return false
	case <-closed:
		return ctx.Err() == nil
	}
}
