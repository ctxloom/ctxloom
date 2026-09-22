package testenv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// bearerServer is a Streamable HTTP MCP server behind the same bearer guard
// the runner's endpoint applies: every request must carry the credential or
// is answered 401. It records what each request carried.
func bearerServer(t *testing.T, credential string) (*httptest.Server, func() []string) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "bearer-test", Version: "0"}, nil)
	var mu sync.Mutex
	var seen []string
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, got)
		mu.Unlock()
		if got != "Bearer "+credential {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// TestConnectMCPEndpoint_CarriesTheBearerOnEveryRequest: the harness dials a
// session's bound endpoint the way an engine does — Streamable HTTP with
// the session's bearer on every request — and comes back initialized.
func TestConnectMCPEndpoint_CarriesTheBearerOnEveryRequest(t *testing.T) {
	ts, seen := bearerServer(t, "the-credential")
	s, err := ConnectMCPEndpoint(sessions.Endpoint{URL: ts.URL + "/mcp", Credential: "the-credential"})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, s.Close()) })
	require.NotNil(t, s.InitializeResult(), "a returned session has completed the handshake")

	ctx, cancel := context.WithTimeout(context.Background(), MCPCallTimeout)
	defer cancel()
	_, err = s.ListTools(ctx, nil)
	require.NoError(t, err)
	requests := seen()
	require.NotEmpty(t, requests)
	for _, auth := range requests {
		assert.Equal(t, "Bearer the-credential", auth, "every request carries the bearer")
	}
}

// TestConnectMCPEndpoint_AWrongBearerIsRefusedAtConnect: the endpoint's 401
// surfaces as the connect error, never as a session that answers nothing.
func TestConnectMCPEndpoint_AWrongBearerIsRefusedAtConnect(t *testing.T) {
	ts, _ := bearerServer(t, "the-credential")
	s, err := ConnectMCPEndpoint(sessions.Endpoint{URL: ts.URL + "/mcp", Credential: "not-it"})
	require.Error(t, err)
	assert.Nil(t, s)
	assert.True(t, strings.Contains(err.Error(), "401") || strings.Contains(strings.ToLower(err.Error()), "unauthorized"), "the refusal names the 401: %v", err)
}

// TestConnectMCPEndpoint_RefusesAnEndpointWithoutACredential: a session
// record with no bearer is a session nothing serves; dialing it would only
// produce a confusing 401.
func TestConnectMCPEndpoint_RefusesAnEndpointWithoutACredential(t *testing.T) {
	_, err := ConnectMCPEndpoint(sessions.Endpoint{URL: "http://127.0.0.1:1/mcp"})
	require.ErrorIs(t, err, ErrNoEndpoint)
	_, err = ConnectMCPEndpoint(sessions.Endpoint{Credential: "x"})
	require.ErrorIs(t, err, ErrNoEndpoint)
}
