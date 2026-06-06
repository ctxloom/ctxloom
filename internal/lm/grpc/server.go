package grpc

import (
	"context"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// LLMGRPCPlugin is the implementation of plugin.GRPCPlugin for AI backends.
type LLMGRPCPlugin struct {
	plugin.Plugin
	// Impl is the concrete backend implementation.
	// This is only set on the server (plugin) side.
	Impl backends.Backend
}

// GRPCServer returns the gRPC server for the plugin.
func (p *LLMGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterLLMServer(s, &GRPCServer{Impl: p.Impl})
	return nil
}

// GRPCClient returns the gRPC client for the plugin.
func (p *LLMGRPCPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &GRPCClient{client: NewLLMClient(c)}, nil
}

// GRPCServer wraps a Backend implementation to serve over gRPC.
type GRPCServer struct {
	UnimplementedLLMServer
	Impl backends.Backend
}

// Info returns metadata about the plugin.
func (s *GRPCServer) Info(ctx context.Context, _ *Empty) (*LLMInfo, error) {
	modes := s.Impl.SupportedModes()
	pbModes := make([]ExecutionMode, len(modes))
	for i, m := range modes {
		pbModes[i] = ExecutionMode(m)
	}
	return &LLMInfo{
		Name:           s.Impl.Name(),
		Version:        s.Impl.Version(),
		SupportedModes: pbModes,
	}, nil
}

// Run executes the backend and streams output over a bidirectional stream. The
// first RunInput carries the RunStart (setup + launch params); subsequent inputs
// carry stdin/resize from the frontend (consumed by B2 — for now the pty still
// reads local stdin).
func (s *GRPCServer) Run(stream LLM_RunServer) error {
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive run start: %w", err)
	}
	req := first.GetStart()
	if req == nil {
		return fmt.Errorf("first Run message must carry start")
	}

	// Create writers that send output over the stream. os/exec copies
	// stdout and stderr from separate goroutines, so both writers may call
	// stream.Send concurrently — which gRPC forbids. Share one mutex so
	// sends are serialized.
	var sendMu sync.Mutex
	stdoutWriter := &streamWriter{stream: stream, sendMu: &sendMu, isStderr: false}
	stderrWriter := &streamWriter{stream: stream, sendMu: &sendMu, isStderr: true}

	// Build setup request from RunStart. Treat nil Options as
	// fully-default so callers using proto-zero-values don't crash —
	// use the generated Get* accessors throughout (they're nil-safe).
	opts := req.GetOptions()
	workDir := opts.GetWorkDir()
	env := opts.GetEnv()
	verbosity := opts.GetVerbosity()
	if env == nil {
		env = make(map[string]string)
	}

	// Setup the backend (skip for distillation/minimal mode)
	if !opts.GetSkipSetup() {
		setupReq := &backends.SetupRequest{
			WorkDir:   workDir,
			Fragments: convertFragments(req.Fragments),
			Env:       env,
			Verbosity: verbosity,
			// Host-assembled config/bundle setup payload (nil when the host
			// sent none, e.g. skip_setup). Converted from proto back to the
			// wire-typed Go form the agent's Setup consumes.
			Managed: managedConfigFromProto(req.GetManagedConfig()),
		}
		if err := s.Impl.Setup(stream.Context(), setupReq); err != nil {
			return err
		}
	}

	// Build execute request from RunStart
	execReq := &backends.ExecuteRequest{
		Prompt:      convertFragment(req.Prompt),
		Mode:        backends.ExecutionMode(opts.GetMode()),
		Model:       opts.GetModel(),
		Env:         env,
		Verbosity:   verbosity,
		DryRun:      opts.GetDryRun(),
		AutoApprove: opts.GetAutoApprove(),
		Temperature: opts.GetTemperature(),
		SkipSetup:   opts.GetSkipSetup(),
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

// streamWriter writes to a gRPC stream. sendMu is shared between the stdout
// and stderr writers so their concurrent Write calls never invoke
// stream.Send concurrently (gRPC forbids concurrent Send on one stream).
type streamWriter struct {
	stream   LLM_RunServer
	sendMu   *sync.Mutex
	isStderr bool
}

func (w *streamWriter) Write(p []byte) (int, error) {
	var resp *RunResponse
	if w.isStderr {
		resp = &RunResponse{Output: &RunResponse_Stderr{Stderr: p}}
	} else {
		resp = &RunResponse{Output: &RunResponse_Stdout{Stdout: p}}
	}

	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	if err := w.stream.Send(resp); err != nil {
		return 0, err
	}
	return len(p), nil
}
