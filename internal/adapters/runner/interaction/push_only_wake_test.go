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
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// pushSettle is how long the test listens for a push after the wake has
// arrived. A negative claim ("nothing else came") needs a window, and the SDK
// debounces list_changed pushes (go-sdk's notificationDelay, 10ms), so a push
// queued before the wake can still land after it. The window is far past that
// debounce so it is not tuned to it.
const pushSettle = 300 * time.Millisecond

// pushLog records everything the server sends the client unprompted.
type pushLog struct {
	mu      sync.Mutex
	methods []string // every request or notification the client received
	handled []string // what each registered handler was invoked with
	updated []string // resources/updated URIs
}

func (l *pushLog) handle(what string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handled = append(l.handled, what)
}

func (l *pushLog) snapshot() (methods, handled, updated []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.methods...), append([]string(nil), l.handled...), append([]string(nil), l.updated...)
}

// TestEndpoint_PushesOnlyTheWake pins that the session endpoint's ONLY
// server-to-client traffic is the wake. claude's relay drops the server push
// stream: a future push (a list_changed, a log line, a sampling or elicitation
// request) would vanish silently in production, so it must fail here first.
func TestEndpoint_PushesOnlyTheWake(t *testing.T) {
	sig := interaction.NewWakeSignal(nil)
	url := serveWithWake(t, sig)
	log := &pushLog{}
	wake := make(chan struct{}, 4)

	client := sdk.NewClient(&sdk.Implementation{Name: "push-probe", Version: "0"}, &sdk.ClientOptions{
		LoggingMessageHandler: func(context.Context, *sdk.LoggingMessageRequest) { log.handle("logging") },
		ProgressNotificationHandler: func(context.Context, *sdk.ProgressNotificationClientRequest) {
			log.handle("progress")
		},
		ToolListChangedHandler:     func(context.Context, *sdk.ToolListChangedRequest) { log.handle("tools/list_changed") },
		PromptListChangedHandler:   func(context.Context, *sdk.PromptListChangedRequest) { log.handle("prompts/list_changed") },
		ResourceListChangedHandler: func(context.Context, *sdk.ResourceListChangedRequest) { log.handle("resources/list_changed") },
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			log.handle("resources/updated")
			log.mu.Lock()
			log.updated = append(log.updated, req.Params.URI)
			log.mu.Unlock()
			wake <- struct{}{}
		},
		CreateMessageHandler: func(context.Context, *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error) {
			log.handle("sampling/createMessage")
			return &sdk.CreateMessageResult{Content: &sdk.TextContent{}}, nil
		},
		ElicitationHandler: func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			log.handle("elicitation/create")
			return &sdk.ElicitResult{Action: "decline"}, nil
		},
		ElicitationCompleteHandler: func(context.Context, *sdk.ElicitationCompleteNotificationRequest) {
			log.handle("elicitation/complete")
		},
	})
	// The handlers cover what the SDK has a hook for; this sees every method
	// the client receives, hooked or not (ping, roots/list, anything new).
	client.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			log.mu.Lock()
			log.methods = append(log.methods, method)
			log.mu.Unlock()
			return next(ctx, method, req)
		}
	})
	transport := &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: headerTransport{headers: map[string]string{"Authorization": "Bearer bearer-token"}}}}
	ctx := context.Background()
	cs, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	init := cs.InitializeResult()
	require.NotNil(t, init.Capabilities)
	_, channel := init.Capabilities.Experimental["claude/channel"]
	assert.False(t, channel, "the endpoint must not advertise claude/channel: that is a push channel the relay would drop")

	_, err = cs.ListTools(ctx, nil)
	require.NoError(t, err)
	_, err = cs.ListResources(ctx, nil)
	require.NoError(t, err)
	_, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "search_content", Arguments: map[string]any{"query": "gamma"}})
	require.NoError(t, err)
	_, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "assemble_context", Arguments: map[string]any{"bundles": []string{"demo/gamma"}}})
	require.NoError(t, err)

	require.NoError(t, cs.Subscribe(ctx, &sdk.SubscribeParams{URI: engine.WakeURI}))
	require.NoError(t, sig.Fire(ctx, "0123456789abcdef"))
	testsupport.Await(t, 10*time.Second, wake, "the wake never arrived")
	time.Sleep(pushSettle)

	methods, handled, updated := log.snapshot()
	assert.Equal(t, []string{"notifications/resources/updated"}, methods, "the client must receive exactly one push: the wake")
	assert.Equal(t, []string{"resources/updated"}, handled, "no handler but the wake's may fire")
	assert.Equal(t, []string{engine.WakeURI}, updated, "and the one update is for the wake URI")
}
