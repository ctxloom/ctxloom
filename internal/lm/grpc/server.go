package grpc

import (
	"context"

	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// AIPluginGRPC is the implementation of plugin.GRPCPlugin for AI backends.
type AIPluginGRPC struct {
	plugin.Plugin
	// Impl is the concrete backend implementation.
	// This is only set on the server (plugin) side.
	Impl backends.Backend
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
	Impl backends.Backend
}

// Info returns metadata about the plugin.
func (s *GRPCServer) Info(ctx context.Context, _ *Empty) (*PluginInfo, error) {
	modes := s.Impl.SupportedModes()
	pbModes := make([]ExecutionMode, len(modes))
	for i, m := range modes {
		pbModes[i] = ExecutionMode(m)
	}
	return &PluginInfo{
		Name:           s.Impl.Name(),
		Version:        s.Impl.Version(),
		SupportedModes: pbModes,
	}, nil
}

// Run executes the backend and streams output.
func (s *GRPCServer) Run(req *RunRequest, stream AIPlugin_RunServer) error {
	// Create writers that send output over the stream
	stdoutWriter := &streamWriter{stream: stream, isStderr: false}
	stderrWriter := &streamWriter{stream: stream, isStderr: true}

	// Build setup request from RunRequest
	opts := req.GetOptions()
	workDir := ""
	env := make(map[string]string)
	verbosity := uint32(0)
	if opts != nil {
		workDir = opts.WorkDir
		env = opts.Env
		verbosity = opts.Verbosity
	}

	setupReq := &backends.SetupRequest{
		WorkDir:   workDir,
		Fragments: convertFragments(req.Fragments),
		Env:       env,
		Verbosity: verbosity,
	}

	// Setup the backend
	if err := s.Impl.Setup(stream.Context(), setupReq); err != nil {
		return err
	}

	// Build execute request from RunRequest
	execReq := &backends.ExecuteRequest{
		Prompt:      convertFragment(req.Prompt),
		Mode:        backends.ExecutionMode(opts.Mode),
		Model:       opts.Model,
		Env:         env,
		Verbosity:   verbosity,
		DryRun:      opts.DryRun,
		AutoApprove: opts.AutoApprove,
		Temperature: opts.Temperature,
	}

	// Execute the backend
	result, err := s.Impl.Execute(stream.Context(), execReq, stdoutWriter, stderrWriter)
	if err != nil {
		return err
	}

	// Cleanup
	if err := s.Impl.Cleanup(stream.Context()); err != nil {
		return err
	}

	// Send the exit code and model info as the final message
	return stream.Send(&RunResponse{
		Output:    &RunResponse_ExitCode{ExitCode: result.ExitCode},
		ModelInfo: convertModelInfoToProto(result.ModelInfo),
	})
}

// convertFragment converts a proto Fragment to a backend Fragment.
func convertFragment(f *Fragment) *backends.Fragment {
	if f == nil {
		return nil
	}
	return &backends.Fragment{
		Name:        f.Name,
		Version:     f.Version,
		Tags:        f.Tags,
		Content:     f.Content,
		IsDistilled: f.IsDistilled,
		DistilledBy: f.DistilledBy,
	}
}

// convertFragments converts a slice of proto Fragments to backend Fragments.
func convertFragments(frags []*Fragment) []*backends.Fragment {
	if frags == nil {
		return nil
	}
	result := make([]*backends.Fragment, len(frags))
	for i, f := range frags {
		result[i] = convertFragment(f)
	}
	return result
}

// convertModelInfoToProto converts a backend ModelInfo to a proto ModelInfo.
func convertModelInfoToProto(m *backends.ModelInfo) *ModelInfo {
	if m == nil {
		return nil
	}
	return &ModelInfo{
		ModelName:    m.ModelName,
		ModelVersion: m.ModelVersion,
		Provider:     m.Provider,
	}
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
