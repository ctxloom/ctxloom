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
}

// ErrNoEndpoint refuses a relay whose entry named no endpoint to relay to.
var ErrNoEndpoint = errors.New("claude relay: no session endpoint to relay to")

// Run relays until claude closes down (its stdio), which is the relay's
// lifetime, and returns nil then; it returns an error when the endpoint
// stops answering.
func Run(ctx context.Context, cfg Config, down mcp.Transport) error {
	url, _ := cfg.Env(claude.EnvRelayURL)
	bearer, _ := cfg.Env(claude.EnvRelayBearer)
	if url == "" || bearer == "" {
		return fmt.Errorf("%w: %s and %s must both be set", ErrNoEndpoint, claude.EnvRelayURL, claude.EnvRelayBearer)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpc := &http.Client{Transport: bearerTransport{bearer: bearer}}
	go watchWake(ctx, cfg, url, httpc)
	return pump(ctx, down, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpc})
}

// bearerTransport puts the endpoint's bearer on every request. An HTTP error
// names the URL, never the headers, so the bearer reaches no diagnostic.
type bearerTransport struct{ bearer string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+b.bearer)
	return http.DefaultTransport.RoundTrip(r)
}

// pump connects both sides and relays each way until one ends.
func pump(ctx context.Context, down, up mcp.Transport) error {
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
	go func() { ended <- upward(ctx, dc, uc) }()
	go func() { ended <- downward(ctx, uc, dc) }()
	return <-ended
}

// upward relays claude's messages to the endpoint, and returns nil when
// claude closes its side.
//
// A call is written on its own goroutine. The endpoint sends a call's
// response headers only with its first message, so a write returns only once
// the call starts answering: written in line, one long call (a parked
// receive) would hold back every later message — including the cancellation
// of that very call. Nothing orders a call after initialize here: an MCP
// client sends nothing but the initialize call until its result arrives, and
// the result reaches claude only once the transport holds the session id.
func upward(ctx context.Context, dc, uc mcp.Connection) error {
	for {
		msg, err := dc.Read(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claude relay: reading claude's stdio: %w", err)
		}
		if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
			go relayUp(ctx, dc, uc, req)
			continue
		}
		relayUp(ctx, dc, uc, msg)
	}
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

// downward relays the endpoint's messages to claude.
func downward(ctx context.Context, uc, dc mcp.Connection) error {
	for {
		msg, err := uc.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claude relay: the session endpoint stopped answering: %w", err)
		}
		if err := dc.Write(ctx, msg); err != nil {
			return nil
		}
	}
}

// watchWake binds claude's wake from the relay's environment and, when it
// binds, subscribes to the endpoint's wake on a session of its own and fires
// the wake for each notification. A relay that cannot bind says so once and
// never subscribes: the runner then learns, when it fires, that nobody can
// wake this session.
func watchWake(ctx context.Context, cfg Config, url string, httpc *http.Client) {
	spec, _ := claude.Claude{}.Wake().Get()
	w, err := spec.Bind(ctx, cfg.Env)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: this session cannot be woken: %v\n", claude.RelayCommand, err)
		return
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-" + claude.RelayCommand, Version: version.Version}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			nonce, _ := req.Params.Meta["nonce"].(string)
			if err := w.Fire(ctx, nonce); err != nil {
				fmt.Fprintf(cfg.Stderr, "ctxloom %s: wake %s was not posted: %v\n", claude.RelayCommand, nonce, err)
			}
		},
	})
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpc}, nil)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: cannot reach the session endpoint for the wake: %v\n", claude.RelayCommand, err)
		return
	}
	defer cs.Close()
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: engine.WakeURI}); err != nil {
		fmt.Fprintf(cfg.Stderr, "ctxloom %s: cannot subscribe to the wake: %v\n", claude.RelayCommand, err)
		return
	}
	<-ctx.Done()
}
