package interaction_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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
	sig := interaction.NewWakeSignal(nil)
	cs, updates := wakeClient(t, serveWithWake(t, sig))
	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: engine.WakeURI}))

	require.NoError(t, sig.Fire(context.Background(), "0123456789abcdef"))

	got := testsupport.Await(t, 10*time.Second, updates, "the subscriber was never notified")
	assert.Equal(t, engine.WakeURI, got.URI)
	assert.Equal(t, "0123456789abcdef", got.Meta["nonce"])
}

// With nobody subscribed a wake would go nowhere; it fails, so the caller
// disarms the nonce and says the owner was not woken.
func TestWakeSignal_FailsWithNoSubscriber(t *testing.T) {
	sig := interaction.NewWakeSignal(nil)
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "never served")

	cs, _ := wakeClient(t, serveWithWake(t, sig))
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "connected but not subscribed")

	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: engine.WakeURI}))
	require.NoError(t, cs.Unsubscribe(context.Background(), &sdk.UnsubscribeParams{URI: engine.WakeURI}))
	require.ErrorIs(t, sig.Fire(context.Background(), "0123456789abcdef"), interaction.ErrNoWakeSubscriber, "unsubscribed")
}

// A subscriber whose session ended without unsubscribing (the relay died
// with its engine) is not a subscriber.
func TestWakeSignal_AClosedSessionIsNoSubscriber(t *testing.T) {
	sig := interaction.NewWakeSignal(nil)
	url := serveWithWake(t, sig)
	cs, _ := wakeClient(t, url)
	require.NoError(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: engine.WakeURI}))
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
	cs, _ := wakeClient(t, serveWithWake(t, interaction.NewWakeSignal(nil)))
	res, err := cs.ListResources(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Resources)
	for _, r := range res.Resources {
		assert.NotEqual(t, engine.WakeURI, r.URI)
	}
	require.Error(t, cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: res.Resources[0].URI}))
}

func TestServe_RefusesWithoutAWakeSignal(t *testing.T) {
	_, err := interaction.Endpoint{Home: deadHome(t)}.Serve(context.Background(), loadoutAt(freePort(t)), delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.ErrorIs(t, err, interaction.ErrNoWakeSignal)
}

// registrations records what a WakeSignal registered as the owner's wake
// (runner.Home.SetWake in production) and how often it was released.
type registrations struct {
	mu       sync.Mutex
	bound    []engine.Wake
	released int
}

func (r *registrations) register(w engine.Wake) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bound = append(r.bound, w)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.released++
	}
}

func (r *registrations) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bound), r.released
}

// TestWakeSignal_ARelaysSubscriptionBindsTheOwnersWake: the relay's
// subscription IS the binding — the first subscriber registers the signal as
// the session owner's wake, a second subscriber (a respawned relay) does not
// register it twice, and the last one to unsubscribe releases it.
// MUTATION — register on serve instead of on subscribe, or release on any
// unsubscribe — turns this red.
func TestWakeSignal_ARelaysSubscriptionBindsTheOwnersWake(t *testing.T) {
	reg := &registrations{}
	sig := interaction.NewWakeSignal(reg.register)
	url := serveWithWake(t, sig)
	first, _ := wakeClient(t, url)
	second, _ := wakeClient(t, url)
	bound, _ := reg.counts()
	require.Zero(t, bound, "nothing is bound until a relay subscribes")

	require.NoError(t, first.Subscribe(context.Background(), &sdk.SubscribeParams{URI: engine.WakeURI}))
	require.NoError(t, second.Subscribe(context.Background(), &sdk.SubscribeParams{URI: engine.WakeURI}))
	bound, released := reg.counts()
	require.Equal(t, 1, bound, "bound once, by the first subscriber")
	require.Same(t, sig, reg.bound[0], "the owner's wake is the signal itself")
	require.Zero(t, released)

	require.NoError(t, first.Unsubscribe(context.Background(), &sdk.UnsubscribeParams{URI: engine.WakeURI}))
	_, released = reg.counts()
	require.Zero(t, released, "a subscriber remains")
	require.NoError(t, second.Unsubscribe(context.Background(), &sdk.UnsubscribeParams{URI: engine.WakeURI}))
	_, released = reg.counts()
	require.Equal(t, 1, released, "the last subscriber leaving releases the binding")
}
