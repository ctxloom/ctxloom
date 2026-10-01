package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// freePort reserves a loopback port the way the originator's minter does:
// listen on :0, read the port, close — the address the runner is then asked
// to BIND, never to choose.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// deadHome is a runner Home whose coordinator is never reachable: enough for
// the endpoint's coordination tools to be REGISTERED (the surface the
// exhaustiveness check vouches for), and never dialed by these tests.
func deadHome(t *testing.T) *runner.Home {
	t.Helper()
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL: "http://127.0.0.1:1/mcp", Token: "unused", Harness: "test", Version: "test", Harp: "h",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })
	return home
}

// loadoutAt is a loadout whose endpoint is the given port and whose package
// carries one premised fragment and one loaded fragment, with the catalog
// index the resources enumerate.
func loadoutAt(port int) delivery.Loadout {
	return delivery.Loadout{
		Package: composite.Package{
			Context:   composite.Context{Text: "CONTEXT-TEXT"},
			Fragments: []composite.Item[composite.Fragment]{{Ref: "demo/loaded", Value: composite.Fragment{Name: "loaded", Body: "LOADED-BODY"}}},
			Premised:  []composite.Item[composite.Fragment]{{Ref: "demo/gamma", Value: composite.Fragment{Name: "gamma", Body: "GAMMA-BODY", Premise: "when removing a worktree"}}},
		},
		Index: composite.Index{Entries: []composite.IndexEntry{
			{Ref: "demo/loaded", Kind: trust.KindFragment},
			{Ref: "demo/gamma", Kind: trust.KindFragment, Premise: "when removing a worktree"},
			{Ref: "demo/deploy", Kind: trust.KindPrompt, Description: "deploy the thing"},
		}},
		MCP:      sessions.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp", Credential: "bearer-token"},
		Identity: sessions.Identity{Harp: "h", Depth: 1},
		WorkDir:  "/work",
	}
}

func serve(t *testing.T, lo delivery.Loadout) delivery.Served {
	t.Helper()
	ep := interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal()}
	served, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
	return served
}

// headerTransport stamps the same headers on every request the SDK client
// makes — the engine's .mcp.json entry names the bearer this way.
type headerTransport struct{ headers map[string]string }

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connect(t *testing.T, url string, headers map[string]string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "probe", Version: "0"}, nil)
	transport := &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: headerTransport{headers: headers}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func post(t *testing.T, url string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	return res.StatusCode
}

// TestServe_BindsTheLoadoutEndpoint_BearerAndOrigin pins the port's contract:
// the runner BINDS the address the loadout names (it never mints one), a
// request with no bearer or the wrong bearer is 401, a request whose Origin
// is off the allowlist is 403, and a bearer-carrying same-origin request is
// served.
func TestServe_BindsTheLoadoutEndpoint_BearerAndOrigin(t *testing.T) {
	lo := loadoutAt(freePort(t))
	serve(t, lo)
	url := lo.MCP.URL

	assert.Equal(t, http.StatusUnauthorized, post(t, url, nil), "no bearer")
	assert.Equal(t, http.StatusUnauthorized, post(t, url, map[string]string{"Authorization": "Bearer wrong"}), "wrong bearer")
	assert.Equal(t, http.StatusForbidden, post(t, url, map[string]string{"Authorization": "Bearer bearer-token", "Origin": "http://evil.example"}), "bad Origin")
	assert.Equal(t, http.StatusUnauthorized, post(t, url, map[string]string{"Origin": "http://evil.example"}), "the bearer is checked before the Origin: an unauthenticated caller learns nothing about the allowlist")

	cs := connect(t, url, map[string]string{"Authorization": "Bearer bearer-token"})
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(tools.Tools))
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	assert.Subset(t, names, []string{"assemble_context", "search_content", "search_library", "agent_send", "agent_recv", "compact_session"},
		"the cell-local, coordination and host-relayed tools are one surface on the bound endpoint")
}

// TestServe_EmptyAllowedOrigins_IsRefused: the allowlist is part of the
// contract, not an option — no caller can serve without one.
func TestServe_EmptyAllowedOrigins_IsRefused(t *testing.T) {
	lo := loadoutAt(freePort(t))
	ep := interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal()}
	_, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{})
	require.ErrorIs(t, err, delivery.ErrNoAllowedOrigins)
}

// TestServe_OccupiedPort_IsErrEndpointUnavailable: the one refusal the
// coordinator's recovery arm answers with a rebind.
func TestServe_OccupiedPort_IsErrEndpointUnavailable(t *testing.T) {
	port := freePort(t)
	occupant, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	require.NoError(t, err)
	defer occupant.Close()

	ep := interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal()}
	_, err = ep.Serve(context.Background(), loadoutAt(port), delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, delivery.ErrEndpointUnavailable), "got %v", err)
}

// TestServe_Close_FreesThePort: the same address is bindable by the next
// incarnation of the session (the endpoint is stable across resumes). The
// serve goroutine is held before it hands the listener to the HTTP server, so
// Close runs in the window where Shutdown does not yet own the listener — the
// ordering a loaded CI runner produced, forced here on every run.
func TestServe_Close_FreesThePort(t *testing.T) {
	lo := loadoutAt(freePort(t))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ep := interaction.WithServeGate(interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal()}, func() { <-release })
	served, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	require.NoError(t, served.Close())
	again, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err, "a closed endpoint's address must be bindable again")
	require.NoError(t, again.Close())
}

// TestServe_FragmentResource_ServedFromThePackage: ctxloom://fragments/{name}
// reads the fragment's body off the loadout's Package — the runner holds no
// config and opens no catalog.
func TestServe_FragmentResource_ServedFromThePackage(t *testing.T) {
	lo := loadoutAt(freePort(t))
	serve(t, lo)
	cs := connect(t, lo.MCP.URL, map[string]string{"Authorization": "Bearer bearer-token"})

	res, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "ctxloom://fragments/gamma"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "GAMMA-BODY", res.Contents[0].Text)

	list, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "ctxloom://fragments"})
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
	assert.Contains(t, list.Contents[0].Text, "demo/gamma")
	assert.Contains(t, list.Contents[0].Text, "when removing a worktree", "the catalog names each premise")

	_, err = cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "ctxloom://fragments/absent"})
	require.Error(t, err, "a fragment the package does not carry is refused, not blank")
}

// TestServe_CellLocalTools_ServeFromPackageAndIndex: assemble_context loads
// premised fragments by ref off the Package; search_content and
// search_library search the Index. None of them opens a config.
func TestServe_CellLocalTools_ServeFromPackageAndIndex(t *testing.T) {
	lo := loadoutAt(freePort(t))
	serve(t, lo)
	cs := connect(t, lo.MCP.URL, map[string]string{"Authorization": "Bearer bearer-token"})

	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/gamma"}}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	body, _ := json.Marshal(res.StructuredContent)
	assert.Contains(t, string(body), "GAMMA-BODY")
	assert.Contains(t, string(body), "demo/gamma")

	res, err = cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/absent"}}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a ref the package does not carry is refused by name")

	res, err = cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "search_content", Arguments: map[string]any{"query": "deploy"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	body, _ = json.Marshal(res.StructuredContent)
	assert.Contains(t, string(body), `"count":1`)
	assert.Contains(t, string(body), "demo/deploy")

	res, err = cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "search_library", Arguments: map[string]any{"query": "nothing-matches"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%v", res.Content)
	body, _ = json.Marshal(res.StructuredContent)
	assert.Contains(t, string(body), `"query":"nothing-matches"`)
	assert.Contains(t, string(body), `"count":0`)
}
