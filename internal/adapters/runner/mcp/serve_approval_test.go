package mcp_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
)

// recordingRoute is an approval route that answers fixed bytes and records
// what reached it.
type recordingRoute struct {
	mu       sync.Mutex
	events   []string
	payloads []string
}

func (r *recordingRoute) Hook(_ context.Context, event string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.payloads = append(r.payloads, string(payload))
	return []byte(`{"decided":"by the route"}`), nil
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
	home.SetApprovalRoute(route)
	code, body := postHook(t, target, bearer, `{"tool_name":"Bash"}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.JSONEq(t, `{"decided":"by the route"}`, body)
	assert.Equal(t, []string{"PermissionRequest"}, route.events)
	assert.Equal(t, []string{`{"tool_name":"Bash"}`}, route.payloads)
}
