package relay_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/claude/relay"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// bound caps every wait in these tests: a defect that loses a message fails
// the test instead of parking it until the package timeout.
const bound = 10 * time.Second

const (
	bearer = "bearer-5ec2e7"
	token  = "tok-5f1d0c9e-never-print-me"
	nonce  = "0123456789abcdef"
)

// endpoint serves a REAL session endpoint (runner/interaction) carrying one
// premised fragment, and returns its URL and its wake signal.
func endpoint(t *testing.T) (string, *interaction.WakeSignal) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL: "http://127.0.0.1:1/mcp", Token: "unused", Harness: "test", Version: "test", Harp: "h",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })
	lo := delivery.Loadout{
		Package: composite.Package{
			Premised: []composite.Item[composite.Fragment]{{Ref: "demo/gamma", Value: composite.Fragment{Name: "gamma", Body: "GAMMA-BODY", Premise: "when removing a worktree"}}},
		},
		Index:    composite.Index{Entries: []composite.IndexEntry{{Ref: "demo/gamma", Kind: trust.KindFragment, Premise: "when removing a worktree"}}},
		MCP:      sessions.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp", Credential: bearer},
		Identity: sessions.Identity{Harp: "h", Depth: 1},
		WorkDir:  "/work",
	}
	sig := interaction.NewWakeSignal(nil)
	served, err := interaction.Endpoint{Home: home, Wake: sig}.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
	return lo.MCP.URL, sig
}

// lines is a synchronised stderr: every write is one received value, so a
// test waits on the relay's diagnostics instead of polling a buffer.
type lines chan string

func (l lines) Write(p []byte) (int, error) { l <- string(p); return len(p), nil }

// startRelay runs the relay with env as its environment and connects a client
// standing in for claude to its stdio side. It returns the client session,
// the relay's stderr, and Run's result.
func startRelay(t *testing.T, env map[string]string) (*mcp.ClientSession, lines, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	claudeSide, relaySide := mcp.NewInMemoryTransports()
	stderr := make(lines, 16)
	done := make(chan error, 1)
	go func() {
		done <- relay.Run(ctx, relay.Config{Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Stderr: stderr}, relaySide)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-stand-in", Version: "0"}, nil)
	cs, err := client.Connect(ctx, claudeSide, nil)
	require.NoError(t, err)
	return cs, stderr, done
}

// messagingSocket listens where claude's messaging socket would be and yields
// the lines of each connection, read to EOF.
func messagingSocket(t *testing.T) (string, <-chan []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	posts := make(chan []string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var got []string
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				got = append(got, sc.Text())
			}
			_ = conn.Close()
			posts <- got
		}
	}()
	return path, posts
}

// fireOnceSubscribed fires sig, retrying only while no relay has subscribed
// yet: the subscription is the relay's own second session, which nothing
// orders before the test's first call. WakeSignal.Fire is synchronised, so
// this is a bounded retry over a locked read, not a poll of shared state.
func fireOnceSubscribed(t *testing.T, sig *interaction.WakeSignal) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := sig.Fire(context.Background(), nonce)
		if !errors.Is(err, interaction.ErrNoWakeSubscriber) {
			require.NoError(t, err)
			return
		}
		require.True(t, time.Now().Before(deadline), "the relay never subscribed to the wake")
		time.Sleep(10 * time.Millisecond)
	}
}

func baseEnv(url string) map[string]string {
	return map[string]string{claude.EnvRelayURL: url, claude.EnvRelayBearer: bearer}
}

// Every tool call passes through unchanged: what claude sees through the
// relay is what a direct client of the endpoint sees.
func TestRelay_PassesToolCallsThroughToTheEndpoint(t *testing.T) {
	url, _ := endpoint(t)
	cs, _, _ := startRelay(t, baseEnv(url))

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"assemble_context", "search_content", "agent_send", "agent_report"} {
		assert.True(t, names[want], "tool %s is relayed", want)
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/gamma"}}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	body, _ := json.Marshal(res.StructuredContent)
	assert.Contains(t, string(body), "GAMMA-BODY")

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/absent"}}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "the endpoint's refusal reaches claude as the endpoint gave it")

	frag, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ctxloom://fragments/gamma"})
	require.NoError(t, err)
	require.Len(t, frag.Contents, 1)
	assert.Equal(t, "GAMMA-BODY", frag.Contents[0].Text)
}

// The relay's lifetime is claude's: when claude closes the relay's stdio, Run
// returns without error.
func TestRelay_ReturnsWhenClaudeCloses(t *testing.T) {
	url, _ := endpoint(t)
	cs, _, done := startRelay(t, baseEnv(url))
	require.NoError(t, cs.Close())
	require.NoError(t, testsupport.Await(t, bound, done, "the relay outlived claude's stdio"))
}

// A wake the runner fires reaches claude's messaging socket as claude's own
// descendant's post: the auth line, then the wake line carrying the nonce.
func TestRelay_AFiredWakePostsTheNonceToClaudesMessagingSocket(t *testing.T) {
	url, sig := endpoint(t)
	sock, posts := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	startRelay(t, env)

	fireOnceSubscribed(t, sig)

	got := testsupport.Await(t, bound, posts, "nothing was posted to claude's messaging socket")
	require.Len(t, got, 2)
	assert.JSONEq(t, `{"type":"auth","token":"`+token+`"}`, got[0])
	var post struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(got[1]), &post))
	assert.Equal(t, "user", post.Type)
	assert.Equal(t, engine.WakeText(nonce), post.Message.Content)
}

// A relay whose environment cannot bind claude's wake says so once on stderr,
// naming the missing variable, and never subscribes: the runner then learns
// the owner cannot be woken when it tries.
func TestRelay_AnUnboundWakeIsReportedAndNeverSubscribed(t *testing.T) {
	url, sig := endpoint(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	cs, stderr, _ := startRelay(t, env)

	line := testsupport.Await(t, bound, (<-chan string)(stderr), "the relay reported nothing")
	assert.Contains(t, line, "CLAUDE_CODE_MESSAGING_SOCKET")
	assert.NotContains(t, line, token)
	assert.NotContains(t, line, bearer)

	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err, "the relay still relays")
	require.ErrorIs(t, sig.Fire(context.Background(), nonce), interaction.ErrNoWakeSubscriber)
}

// A post that fails is reported on stderr, and neither the token nor the
// bearer is ever in what the relay writes.
func TestRelay_AFailedPostIsReportedWithoutTheTokenOrBearer(t *testing.T) {
	url, sig := endpoint(t)
	dir, err := os.MkdirTemp("", "rl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = filepath.Join(dir, "absent.sock")
	env["CLAUDE_CODE_MESSAGING_TOKEN"] = token
	_, stderr, _ := startRelay(t, env)

	fireOnceSubscribed(t, sig)

	line := testsupport.Await(t, bound, (<-chan string)(stderr), "the relay reported nothing")
	assert.Contains(t, line, "absent.sock")
	assert.True(t, strings.Contains(line, "wake"), "the line names what failed: %q", line)
	assert.NotContains(t, line, token)
	assert.NotContains(t, line, bearer)
}

// Without the endpoint and its bearer there is nothing to relay to: Run
// refuses at once, naming the variables, and never echoes the bearer.
func TestRelay_RefusesWithoutTheEndpoint(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{claude.EnvRelayURL: "http://127.0.0.1:1/mcp"},
		{claude.EnvRelayBearer: bearer},
	} {
		err := relay.Run(context.Background(), relay.Config{Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Stderr: make(lines, 1)}, &mcp.InMemoryTransport{})
		require.ErrorIs(t, err, relay.ErrNoEndpoint, "env %v", env)
		assert.Contains(t, err.Error(), claude.EnvRelayURL)
		assert.NotContains(t, err.Error(), bearer)
	}
}

// A call the endpoint did not take is answered — with an error naming it —
// rather than left for claude to wait on forever.
func TestRelay_ACallTheEndpointDidNotTakeIsAnsweredWithAnError(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	cs, _, _ := startRelay(t, baseEnv(upstream.URL))
	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not take")
}

// One call that has not started answering (a parked receive) must not hold
// back the next: the second call completes while the first is still open.
// The interleaving is forced — the first call's handler blocks until the
// second has returned.
func TestRelay_ALongCallDoesNotHoldBackTheNext(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0"}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	mcp.AddTool(server, &mcp.Tool{Name: "park"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		close(entered)
		<-release
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "parked"}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(upstream.Close)
	// Cleanups run last-first: the parked handler is released before the
	// server's Close waits on it, so a failure below fails instead of hanging.
	unpark := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unpark)
	cs, _, _ := startRelay(t, baseEnv(upstream.URL))

	parked := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "park"})
		parked <- res
	}()
	testsupport.Await(t, bound, (<-chan struct{})(entered), "the parked call never reached the upstream")

	// Awaited off this goroutine: held back, the call cannot even be
	// cancelled — its cancellation queues behind the same parked write.
	pinged := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
		pinged <- res
	}()
	res := testsupport.Await(t, bound, (<-chan *mcp.CallToolResult)(pinged), "the second call was held back behind the first")
	require.NotNil(t, res)
	assert.Equal(t, "pong", res.Content[0].(*mcp.TextContent).Text)

	unpark()
	assert.Equal(t, "parked", testsupport.Await(t, bound, (<-chan *mcp.CallToolResult)(parked), "the parked call never answered").Content[0].(*mcp.TextContent).Text)
}

// rawCall writes one JSON-RPC call on conn and reads the reply.
func rawCall(t *testing.T, conn mcp.Connection, id int64, method string, params any) *jsonrpc.Response {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	rid, err := jsonrpc.MakeID(float64(id))
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), &jsonrpc.Request{ID: rid, Method: method, Params: raw}))
	msg := testsupport.Within(t, bound, func() jsonrpc.Message {
		m, err := conn.Read(context.Background())
		require.NoError(t, err)
		return m
	}, "no reply to %s", method)
	resp, ok := msg.(*jsonrpc.Response)
	require.True(t, ok, "reply to %s is a response: %T", method, msg)
	return resp
}

// Claude opens a stdio server with calls the endpoint cannot take before the
// session exists (server/discover, measured on claude 2.1.286). The relay
// answers such a call itself — method not found, as a native stdio server
// answers an unknown method — and the session that follows works: the
// endpoint is never asked to take a call it has no session for, which it
// refuses in a way that would end the relay.
func TestRelay_ACallBeforeInitializeIsAnsweredMethodNotFound(t *testing.T) {
	url, _ := endpoint(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	claudeSide, relaySide := mcp.NewInMemoryTransports()
	go func() {
		_ = relay.Run(ctx, relay.Config{Env: func(k string) (string, bool) { v, ok := baseEnv(url)[k]; return v, ok }, Stderr: make(lines, 16)}, relaySide)
	}()
	conn, err := claudeSide.Connect(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	discover := rawCall(t, conn, 1, "server/discover", map[string]any{})
	var wire *jsonrpc.Error
	require.ErrorAs(t, discover.Error, &wire)
	assert.Equal(t, int64(jsonrpc.CodeMethodNotFound), wire.Code)

	initialized := rawCall(t, conn, 2, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "claude-stand-in", "version": "0"},
	})
	require.NoError(t, initialized.Error)
	require.NoError(t, conn.Write(ctx, &jsonrpc.Request{Method: "notifications/initialized", Params: json.RawMessage(`{}`)}))
	tools := rawCall(t, conn, 3, "tools/list", map[string]any{})
	require.NoError(t, tools.Error)
	assert.Contains(t, string(tools.Result), "assemble_context")
}

// When claude closes the relay, the relay's wake subscription ends WITH it:
// the runner then learns at once that nobody can wake the session, instead
// of firing into a subscription whose process is gone.
func TestRelay_ClosingClaudeEndsTheWakeSubscription(t *testing.T) {
	url, sig := endpoint(t)
	sock, _ := messagingSocket(t)
	env := baseEnv(url)
	env["CLAUDE_CODE_MESSAGING_SOCKET"] = sock
	cs, _, done := startRelay(t, env)
	fireOnceSubscribed(t, sig)

	require.NoError(t, cs.Close())
	require.NoError(t, testsupport.Await(t, bound, done, "the relay outlived claude's stdio"))
	// No settling: a relay process exits the moment Run returns, so whatever
	// Run has not finished by then never happens.

	require.ErrorIs(t, sig.Fire(context.Background(), nonce), interaction.ErrNoWakeSubscriber)
}
