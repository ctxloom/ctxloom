package grpc

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// wakeCapableBackend is a fakeBackend that ALSO implements every optional
// backend capability interface the wake feature's decorator could erase:
// agent.StructuredChat (chat.go's `s.Impl.(agent.StructuredChat)`),
// agent.StateReader (operations/manage.go), and agent.EngineCLIProvider
// (lm/backends/enginecli.go).
type wakeCapableBackend struct {
	fakeBackend
}

func (b *wakeCapableBackend) Chat(_ context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	out <- agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model}}
	for range in {
	}
	return nil
}

func (b *wakeCapableBackend) State(dir string) (agent.DeliveryState, error) {
	return agent.FileDeliveryState{Rel: "CLAUDE.md"}, nil
}

func (b *wakeCapableBackend) EngineCLIs() []agent.EngineCLI {
	return []agent.EngineCLI{{Engine: "mock", Binary: "mock"}}
}

var (
	_ agent.StructuredChat    = (*wakeCapableBackend)(nil)
	_ agent.StateReader       = (*wakeCapableBackend)(nil)
	_ agent.EngineCLIProvider = (*wakeCapableBackend)(nil)
)

// TestLLMGRPCPlugin_WakeWiring_PreservesOptionalCapabilities is the regression
// pinned for the bug that broke merge 220fbda2 and TestACPAgent_SelfConformance
// (and friends) on the way in: terminalNudgeBackend embedded agent.Backend to
// override Execute, and embedding the INTERFACE only promotes Backend's own
// methods — an optional capability interface the wrapped value ALSO
// implements (agent.StructuredChat, agent.StateReader,
// agent.EngineCLIProvider) is silently erased for anyone holding the
// decorator, exactly what broke chat.go's `s.Impl.(agent.StructuredChat)`.
//
// LLMGRPCPlugin.WrapStreams replaces that decorator with a plain func value
// carried ALONGSIDE Impl, so arming the wake (WrapStreams set, exactly as
// llm_serve.go does for a session-owner Home with no EngineHost) must never
// change what Impl implements. This drives the Chat RPC over a REAL gRPC
// transport (bufconn) — the same shape the go-plugin transport uses — with
// WrapStreams non-nil: a successful, dispatching Chat proves GRPCServer.Impl
// is the UNWRAPPED backend (a decorator would make this Unimplemented, per
// TestGRPCServer_Chat_UnimplementedWhenBackendLacksCapability), and since
// Impl is therefore exactly the wakeCapableBackend value, the direct
// assertions below prove the other two survive it as well.
func TestLLMGRPCPlugin_WakeWiring_PreservesOptionalCapabilities(t *testing.T) {
	backend := &wakeCapableBackend{fakeBackend: fakeBackend{name: "mock"}}

	lis := bufconn.Listen(1 << 20)
	grpcServer := googlegrpc.NewServer()
	plug := &LLMGRPCPlugin{
		Impl: backend,
		// Armed exactly as llm_serve.go arms it for a session-owner Home with
		// no EngineHost: non-nil, so a reintroduced decorator would be live
		// here. The identity wrap is enough to prove the point — the bug was
		// never about WHAT the wrap does, only about Impl no longer being the
		// concrete backend.
		WrapStreams: func(r io.Reader, w io.Writer) (io.Reader, io.Writer, func()) { return r, w, func() {} },
	}
	require.NoError(t, plug.GRPCServer(nil, grpcServer))

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	conn, err := googlegrpc.NewClient(
		"passthrough:///bufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := NewLLMClient(conn)
	stream, err := client.Chat(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&ChatInput{Input: &ChatInput_Start{Start: &ChatStart{Model: "m"}}}))
	require.NoError(t, stream.CloseSend())

	ev, err := stream.Recv()
	require.NoError(t, err, "GRPCServer.Impl must still satisfy agent.StructuredChat with WrapStreams armed — "+
		"a Backend-embedding decorator would surface this as Unimplemented")
	require.NotNil(t, ev.GetSession(), "Chat must actually dispatch into the backend, not merely avoid Unimplemented")
	assert.Equal(t, "m", ev.GetSession().GetModel())

	// Transitively: the successful dispatch above is only possible if Impl IS
	// backend, unwrapped — so the other two optional interfaces the SAME
	// value implements must also have survived.
	ok := false
	var asAny any = backend
	if _, ok = asAny.(agent.StateReader); !ok {
		t.Error("wakeCapableBackend no longer satisfies agent.StateReader")
	}
	if _, ok = asAny.(agent.EngineCLIProvider); !ok {
		t.Error("wakeCapableBackend no longer satisfies agent.EngineCLIProvider")
	}
}
