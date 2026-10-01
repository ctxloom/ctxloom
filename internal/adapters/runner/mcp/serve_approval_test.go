package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// recordingRoute is an approval route that answers fixed bytes and records
// what reached it.
type recordingRoute struct {
	mu       sync.Mutex
	events   []string
	payloads []string
	hostArgs []string
}

func (r *recordingRoute) Hook(_ context.Context, event string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.payloads = append(r.payloads, string(payload))
	return []byte(`{"decided":"by the route"}`), nil
}

func (r *recordingRoute) Host(_ context.Context, args json.RawMessage) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostArgs = append(r.hostArgs, string(args))
	return `{"behavior":"deny","message":"held"}`, nil
}

// serveWithHome serves lo over home, so a test can bind the run's route.
func serveWithHome(t *testing.T, lo delivery.Loadout, home *runner.Home) {
	t.Helper()
	served, err := runnermcp.Endpoint{Home: home}.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
}

// hookURL is the approval hook's address on lo's endpoint, for event.
func hookURL(t *testing.T, lo delivery.Loadout, event string) string {
	t.Helper()
	u, err := url.Parse(lo.MCP.URL)
	require.NoError(t, err)
	u.Path = runner.HookPath
	u.RawQuery = url.Values{runner.HookEventParam: {event}}.Encode()
	return u.String()
}

func postHook(t *testing.T, target string, headers map[string]string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, string(out)
}

// TestServe_TheApprovalHookIsGuardedAndReachesTheRunsRoute: the hook's POST
// rides the session endpoint's own listener behind the same bearer and
// Origin rules; with no route bound it decides nothing, and with one bound
// the event and the engine's payload reach it and its bytes are the answer.
func TestServe_TheApprovalHookIsGuardedAndReachesTheRunsRoute(t *testing.T) {
	lo := loadoutAt(freePort(t))
	home := deadHome(t)
	serveWithHome(t, lo, home)
	target := hookURL(t, lo, "PermissionRequest")
	bearer := map[string]string{"Authorization": "Bearer bearer-token"}

	code, _ := postHook(t, target, nil, `{}`)
	assert.Equal(t, http.StatusUnauthorized, code, "no bearer")
	code, _ = postHook(t, target, map[string]string{"Authorization": "Bearer bearer-token", "Origin": "http://evil.example"}, `{}`)
	assert.Equal(t, http.StatusForbidden, code, "bad Origin")
	code, _ = postHook(t, target, bearer, `{}`)
	assert.Equal(t, http.StatusNotFound, code, "no route bound: no decision")

	route := &recordingRoute{}
	home.SetApprovalHost(route)
	code, body := postHook(t, target, bearer, `{"tool_name":"Bash"}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"decided":"by the route"}`, body)
	assert.Equal(t, []string{"PermissionRequest"}, route.events)
	assert.Equal(t, []string{`{"tool_name":"Bash"}`}, route.payloads)
}

// TestServe_ThePermissionHostIsTheRunsRoute: the engine's permission host is
// served against the run's route — refused when the run routes no
// approvals, and answering the route's own text when it does.
func TestServe_ThePermissionHostIsTheRunsRoute(t *testing.T) {
	lo := loadoutAt(freePort(t))
	home := deadHome(t)
	serveWithHome(t, lo, home)
	cs := connect(t, lo.MCP.URL, map[string]string{"Authorization": "Bearer bearer-token"})
	args := map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "toolu_1"}

	_, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: engine.PermissionHostTool, Arguments: args})
	require.ErrorContains(t, err, "routes no approvals", "no route bound: the host holds nothing")

	route := &recordingRoute{}
	home.SetApprovalHost(route)
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: engine.PermissionHostTool, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	assert.JSONEq(t, `{"behavior":"deny","message":"held"}`, text.Text)
	require.Len(t, route.hostArgs, 1)
	assert.JSONEq(t, `{"tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_1"}`, route.hostArgs[0])
}
