package interaction_test

import (
	"context"
	"net/http"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
)

// serveWithWake serves the loadout's endpoint with sig as its wake signal.
func serveWithWake(t *testing.T, sig *interaction.WakeSignal) string {
	t.Helper()
	lo := loadoutAt(freePort(t))
	served, err := interaction.Endpoint{Home: deadHome(t), Wake: sig}.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
	return lo.MCP.URL
}

// wakeClient connects to the endpoint with the bearer and hands every
// resources/updated notification it receives to the returned channel.
func wakeClient(t *testing.T, url string) (*sdk.ClientSession, <-chan *sdk.ResourceUpdatedNotificationParams) {
	t.Helper()
	updates := make(chan *sdk.ResourceUpdatedNotificationParams, 4)
	client := sdk.NewClient(&sdk.Implementation{Name: "relay-probe", Version: "0"}, &sdk.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			updates <- req.Params
		},
	})
	transport := &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: headerTransport{headers: map[string]string{"Authorization": "Bearer bearer-token"}}}}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs, updates
}

// A wake fires as resources/updated on the unlisted wake URI, carrying the
// nonce in _meta, to the session that subscribed to it.
func TestWakeSignal_FiresTheNonceToTheSubscribedSession(t *testing.T) {
	sig := interaction.NewWakeSignal()
	cs, updates := wakeClient(t, serveWithWake(t, sig))
	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: interaction.WakeURI}))

	require.NoError(t, sig.Fire(context.Background(), "0123456789abcdef"))

	got := <-updates
	assert.Equal(t, interaction.WakeURI, got.URI)
	assert.Equal(t, "0123456789abcdef", got.Meta["nonce"])
}

// With nobody subscribed a wake would go nowhere; it fails, so the caller
// disarms the nonce and says the owner was not woken.
func TestWakeSignal_FailsWithNoSubscriber(t *testing.T) {
	sig := interaction.NewWakeSignal()
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "never served")

	cs, _ := wakeClient(t, serveWithWake(t, sig))
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "connected but not subscribed")

	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: interaction.WakeURI}))
	require.NoError(t, cs.Unsubscribe(context.Background(), &sdk.UnsubscribeParams{URI: interaction.WakeURI}))
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "unsubscribed")
}

// A subscriber whose session ended without unsubscribing (the relay died
// with its engine) is not a subscriber.
func TestWakeSignal_AClosedSessionIsNoSubscriber(t *testing.T) {
	sig := interaction.NewWakeSignal()
	url := serveWithWake(t, sig)
	cs, _ := wakeClient(t, url)
	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: interaction.WakeURI}))
	require.NoError(t, cs.Close())
	// The server learns of the close from the client's DELETE, which Close
	// sends and waits on; a fresh session's round-trip orders this read
	// after it.
	probe, _ := wakeClient(t, url)
	_, err := probe.ListResources(context.Background(), nil)
	require.NoError(t, err)

	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber)
}

// The wake URI is a control channel, not context: it is never listed, and no
// other URI is subscribable.
func TestWakeSignal_TheWakeURIIsUnlistedAndTheOnlySubscribable(t *testing.T) {
	cs, _ := wakeClient(t, serveWithWake(t, interaction.NewWakeSignal()))
	res, err := cs.ListResources(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Resources)
	for _, r := range res.Resources {
		assert.NotEqual(t, interaction.WakeURI, r.URI)
	}
	require.Error(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: res.Resources[0].URI}))
}

func TestServe_RefusesWithoutAWakeSignal(t *testing.T) {
	_, err := interaction.Endpoint{Home: deadHome(t)}.Serve(context.Background(), loadoutAt(freePort(t)), delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.ErrorIs(t, err, interaction.ErrNoWakeSignal)
}
