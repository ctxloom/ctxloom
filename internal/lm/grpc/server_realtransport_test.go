package grpc

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/test/bufconn"
)

// concurrentWriteBackend writes to stdout and stderr from many goroutines
// concurrently inside Execute. os/exec drives the real backends this way (it
// copies the child's stdout and stderr on separate goroutines), so this is the
// condition that makes the two streamWriters call stream.Send concurrently —
// the exact situation a shared mutex guards against.
type concurrentWriteBackend struct {
	chunks int // chunks written to EACH of stdout and stderr
}

func (b *concurrentWriteBackend) Name() string                          { return "concurrent" }
func (b *concurrentWriteBackend) Version() string                       { return "test" }
func (b *concurrentWriteBackend) SupportedModes() []agent.ExecutionMode { return nil }
func (b *concurrentWriteBackend) History() agent.SessionHistory         { return nil }
func (b *concurrentWriteBackend) Setup(context.Context, *agent.SetupRequest) error {
	return nil
}
func (b *concurrentWriteBackend) Cleanup(context.Context) error { return nil }

func (b *concurrentWriteBackend) Execute(_ context.Context, _ *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < b.chunks; i++ {
			_, _ = stdout.Write([]byte("o"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < b.chunks; i++ {
			_, _ = stderr.Write([]byte("e"))
		}
	}()
	wg.Wait()
	return &agent.ExecuteResult{ExitCode: 0}, nil
}

// TestGRPCServer_Run_RealTransport_ConcurrentSends drives GRPCServer.Run over a
// REAL gRPC transport (bufconn), not the in-memory fakeRunServer. The backend
// hammers stdout and stderr concurrently; without the shared sendMu in
// streamWriter, the two writers would invoke stream.Send concurrently on one
// gRPC stream, which the transport forbids — surfacing as a panic or a
// -race-detected data race. Run this with `-race` for full coverage.
func TestGRPCServer_Run_RealTransport_ConcurrentSends(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const chunks = 500

	lis := bufconn.Listen(1 << 20)
	grpcServer := googlegrpc.NewServer()
	RegisterLLMServer(grpcServer, &GRPCServer{Impl: &concurrentWriteBackend{chunks: chunks}})

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()

	conn, err := googlegrpc.NewClient(
		"passthrough:///bufnet",
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := NewLLMClient(conn)

	stream, err := client.Run(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&RunInput{Input: &RunInput_Start{Start: &RunStart{
		Prompt:  &Fragment{Content: "hi"},
		Options: &RunOptions{SkipSetup: true},
	}}}))
	require.NoError(t, stream.CloseSend())

	// Drain the stream. A concurrent-Send violation would crash the server
	// goroutine and surface here as a stream error (or never deliver the exit
	// code). Count what we received to prove nothing was dropped.
	var stdoutBytes, stderrBytes int
	var gotExit bool
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		require.NoError(t, rerr, "stream.Recv failed — concurrent Send corruption?")
		switch resp.Output.(type) {
		case *RunResponse_Stdout:
			stdoutBytes += len(resp.GetStdout())
		case *RunResponse_Stderr:
			stderrBytes += len(resp.GetStderr())
		case *RunResponse_ExitCode:
			gotExit = true
			assert.Equal(t, int32(0), resp.GetExitCode())
		}
	}

	assert.True(t, gotExit, "must receive the terminal exit-code message")
	assert.Equal(t, chunks, stdoutBytes, "all stdout bytes must arrive intact")
	assert.Equal(t, chunks, stderrBytes, "all stderr bytes must arrive intact")

	// Clean shutdown so goleak sees no lingering transport goroutines.
	require.NoError(t, conn.Close())
	grpcServer.GracefulStop()
	require.NoError(t, <-serveErr)
}

// TestPluginTransport_ServesHealthAndReflection REFUTES the "no standard
// mechanisms" claim that this transport registers no
// grpc.health.v1 health service and no server reflection.
//
// It registers neither ITSELF, and must not: the LLM service is always served
// by go-plugin's own GRPCServer (LLMGRPCPlugin.GRPCServer only calls
// RegisterLLMServer on the server go-plugin hands it), and that server
// registers both unconditionally during Init — the health service is what
// go-plugin's own Client.Ping is implemented against, which its source calls
// "not optional". Adding our own registration would be a duplicate
// registration on the same server, i.e. a panic.
//
// This drives the REAL go-plugin serving path (plugin.TestPluginGRPCConn builds
// the same GRPCServer with the same Init) over our own PluginMap, so it fails
// if that ever stops being true — for instance if the LLM service were moved
// onto a plain grpc.NewServer of our own.
func TestPluginTransport_ServesHealthAndReflection(t *testing.T) {
	client, _ := plugin.TestPluginGRPCConn(t, false, PluginMap())
	// Close() is the ONLY teardown call here, and the omission is load-bearing.
	// go-plugin's GRPCServer.Stop is not safe to call concurrently with itself:
	// it reads s.broker, closes it, then writes s.broker = nil, with no lock
	// (go-plugin v1.7.0 grpc_server.go:118-126). Client.Close() issues the
	// controller's Shutdown RPC, whose handler calls Stop on go-plugin's own
	// goroutine — so a second, direct Stop() from this cleanup races that one
	// inside the library. The race detector caught exactly that pairing.
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := grpc_health_v1.NewHealthClient(client.Conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{
		Service: plugin.GRPCServiceName,
	})
	require.NoError(t, err, "the plugin transport must answer a standard grpc.health.v1 check")
	assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, health.GetStatus())

	refStream, err := reflectionpb.NewServerReflectionClient(client.Conn).ServerReflectionInfo(ctx)
	require.NoError(t, err)
	require.NoError(t, refStream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{},
	}))
	resp, err := refStream.Recv()
	require.NoError(t, err, "the plugin transport must answer standard server reflection")
	require.NoError(t, refStream.CloseSend())

	var served []string
	for _, s := range resp.GetListServicesResponse().GetService() {
		served = append(served, s.GetName())
	}
	assert.Contains(t, served, "grpc.health.v1.Health")
	assert.Contains(t, served, "ctxloom.lm.llm.LLM", "reflection must describe OUR service, not just go-plugin's own")
}
