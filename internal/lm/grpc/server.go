package grpc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// LLMGRPCPlugin is the implementation of plugin.GRPCPlugin for AI backends.
type LLMGRPCPlugin struct {
	plugin.Plugin
	// Impl is the concrete backend implementation.
	// This is only set on the server (plugin) side.
	Impl agent.Backend
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
	Impl agent.Backend
	// watchPoll overrides the WatchSession poll cadence; zero means the default.
	// Tests set a small value to drive the loop quickly.
	watchPoll time.Duration
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
		setupReq := &agent.SetupRequest{
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
			// Fault tolerance (CLAUDE.md): the user must reach their LLM "even
			// through most misconfigurations." Setup now does load-bearing-but-
			// non-essential work — context provision, skill registration, settings
			// + hook flush — any of which can fail on a bad write without making the
			// agent unlaunchable. Warn and proceed to Execute rather than aborting,
			// matching the documented startup sequence ("apply hooks: warn on
			// errors, continue" / "always respond with initialized").
			clidiag.Warn("ctxloom", "backend setup failed (launching anyway): %v", err)
		}
	}

	// Pump the rest of the bidi stream — the frontend's keystrokes and resizes —
	// into the agent's pty: stdin via an io.Pipe, resize via a channel. The
	// frontend owns the terminal; the controller just forwards. The pump stops
	// when the client half-closes or the run ends (Recv errors), closing both so
	// the pty's stdin copier unblocks.
	stdinR, stdinW := io.Pipe()
	resizeCh := make(chan agent.WindowSize, 1)
	go func() {
		defer func() { _ = stdinW.Close() }()
		defer close(resizeCh)
		stdinClosed := false
		for {
			in, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			switch v := in.GetInput().(type) {
			case *RunInput_Stdin:
				if stdinClosed {
					continue
				}
				if _, werr := stdinW.Write(v.Stdin); werr != nil {
					// The pty's stdin copier exited and closed the read end
					// (ErrClosedPipe). Stop forwarding keystrokes, but KEEP
					// draining Recv: resize messages must still reach the pty,
					// and abandoning the stream here would leave the run with
					// no consumer.
					stdinClosed = true
				}
			case *RunInput_Resize:
				// Latest-wins coalescing: when a stale size is still pending,
				// replace it — a plain "drop the new one" select keeps the
				// OLD size, leaving the pty out of date after a resize burst.
				ws := agent.WindowSize{Rows: uint16(v.Resize.GetRows()), Cols: uint16(v.Resize.GetCols())}
				select {
				case resizeCh <- ws:
				default:
					// Full with a stale size: drain it (the consumer may beat
					// us to it) and send the newer one. This goroutine is the
					// sole producer, so the retry send cannot block.
					select {
					case <-resizeCh:
					default:
					}
					resizeCh <- ws
				}
			}
		}
	}()

	// Build execute request from RunStart
	execReq := &agent.ExecuteRequest{
		Prompt:      convertFragment(req.Prompt),
		Mode:        agent.ExecutionMode(opts.GetMode()),
		Model:       opts.GetModel(),
		Env:         env,
		Verbosity:   verbosity,
		DryRun:      opts.GetDryRun(),
		AutoApprove: opts.GetAutoApprove(),
		Temperature: opts.GetTemperature(),
		SkipSetup:   opts.GetSkipSetup(),
		Stdin:       stdinR,
		Resize:      resizeCh,
	}

	// Execute the backend
	result, err := s.Impl.Execute(stream.Context(), execReq, stdoutWriter, stderrWriter)

	// Cleanup runs on both Execute paths — a failed launch must still release
	// the backend's resources. And a teardown hiccup must not mask the Execute
	// result: after a successful run it would report a completed session as
	// "AI plugin failed" (partial success is success, CLAUDE.md), and after a
	// failed one it would bury the real error. Warn and carry on, matching how
	// Setup degrades above.
	if cerr := s.Impl.Cleanup(stream.Context()); cerr != nil {
		clidiag.Warn("ctxloom", "backend cleanup failed: %v", cerr)
	}
	if err != nil {
		return err
	}
	// A buggy backend that returns (nil, nil) must not panic the serving
	// goroutine (CLAUDE.md: log, don't crash). Degrade to a zero-value result
	// so the final message still sends rather than nil-dereferencing below.
	if result == nil {
		clidiag.Warn("ctxloom", "backend returned a nil result with no error; defaulting to exit code 0")
		result = &agent.ExecuteResult{}
	}

	// Send the exit code and model info as the final message
	return stream.Send(&RunResponse{
		Output:    &RunResponse_ExitCode{ExitCode: result.ExitCode},
		ModelInfo: convertModelInfoToProto(result.ModelInfo),
	})
}

// convertFragment converts a proto Fragment to a backend Fragment.
func convertFragment(f *Fragment) *agent.Fragment {
	if f == nil {
		return nil
	}
	return &agent.Fragment{
		Name:        f.Name,
		Version:     f.Version,
		Tags:        f.Tags,
		Content:     f.Content,
		IsDistilled: f.IsDistilled,
		DistilledBy: f.DistilledBy,
	}
}

// convertFragments converts a slice of proto Fragments to backend Fragments.
func convertFragments(frags []*Fragment) []*agent.Fragment {
	if frags == nil {
		return nil
	}
	result := make([]*agent.Fragment, len(frags))
	for i, f := range frags {
		result[i] = convertFragment(f)
	}
	return result
}

// convertModelInfoToProto converts a backend ModelInfo to a proto ModelInfo.
func convertModelInfoToProto(m *agent.ModelInfo) *ModelInfo {
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
