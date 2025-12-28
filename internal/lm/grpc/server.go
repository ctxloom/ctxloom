package grpc

import (
	"context"
	"io"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// Backend is the interface that AI backend implementations must satisfy.
// This is the internal interface used by the plugin server.
type Backend interface {
	// Name returns the unique identifier for this backend.
	Name() string

	// Version returns the version of this backend.
	Version() string

	// SupportedModes returns the execution modes this backend supports.
	SupportedModes() []ExecutionMode

	// Run executes the AI backend with the given request.
	// Output is streamed via the stdout and stderr writers.
	// Returns exit code, model info (if available), and any error.
	Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (exitCode int32, modelInfo *ModelInfo, err error)
}

// AIPluginGRPC is the implementation of plugin.GRPCPlugin for AI backends.
type AIPluginGRPC struct {
	plugin.Plugin
	// Impl is the concrete backend implementation.
	// This is only set on the server (plugin) side.
	Impl Backend
}

// GRPCServer returns the gRPC server for the plugin.
func (p *AIPluginGRPC) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterAIPluginServer(s, &GRPCServer{Impl: p.Impl})
	return nil
}

// GRPCClient returns the gRPC client for the plugin.
func (p *AIPluginGRPC) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &GRPCClient{client: NewAIPluginClient(c)}, nil
}

// GRPCServer wraps a Backend implementation to serve over gRPC.
type GRPCServer struct {
	UnimplementedAIPluginServer
	Impl Backend
}

// Info returns metadata about the plugin.
func (s *GRPCServer) Info(ctx context.Context, _ *Empty) (*PluginInfo, error) {
	return &PluginInfo{
		Name:           s.Impl.Name(),
		Version:        s.Impl.Version(),
		SupportedModes: s.Impl.SupportedModes(),
	}, nil
}

// Run executes the backend and streams output.
func (s *GRPCServer) Run(req *RunRequest, stream AIPlugin_RunServer) error {
	// Create writers that send output over the stream
	stdoutWriter := &streamWriter{stream: stream, isStderr: false}
	stderrWriter := &streamWriter{stream: stream, isStderr: true}

	exitCode, modelInfo, err := s.Impl.Run(stream.Context(), req, stdoutWriter, stderrWriter)
	if err != nil {
		return err
	}

	// Send the exit code and model info as the final message
	return stream.Send(&RunResponse{
		Output:    &RunResponse_ExitCode{ExitCode: exitCode},
		ModelInfo: modelInfo,
	})
}

// streamWriter writes to a gRPC stream.
type streamWriter struct {
	stream   AIPlugin_RunServer
	isStderr bool
}

func (w *streamWriter) Write(p []byte) (int, error) {
	var resp *RunResponse
	if w.isStderr {
		resp = &RunResponse{Output: &RunResponse_Stderr{Stderr: p}}
	} else {
		resp = &RunResponse{Output: &RunResponse_Stdout{Stdout: p}}
	}

	if err := w.stream.Send(resp); err != nil {
		return 0, err
	}
	return len(p), nil
}
