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
// carry stdin/resize from the frontend. The Setup→Execute→Cleanup body is
// shared with `ctxloom llm turn` (the docker-exec interactive transport, Phase
// 2a) via RunTurn — this method only adapts the go-plugin bidi stream to
// RunTurn's plain stdio+resize contract.
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

	result, err := RunTurn(stream.Context(), s.Impl, req, stdinR, stdoutWriter, stderrWriter, resizeCh)
	if err != nil {
		return err
	}

	// Send the exit code and model info as the final message
	return stream.Send(&RunResponse{
		Output:    &RunResponse_ExitCode{ExitCode: result.ExitCode},
		ModelInfo: convertModelInfoToProto(result.ModelInfo),
	})
}

// RunTurn runs one engine turn's Setup→Execute→Cleanup body against plain
// stdio + a resize channel, returning the engine's result. It is the shared
// core of the interactive transports: the go-plugin Run RPC (GRPCServer.Run,
// stream-wired) and `ctxloom llm turn` (internal/cli, wired to the
// docker-exec TTY's os.Stdin/os.Stdout + a SIGWINCH-fed resize) both drive it,
// so the two never drift on fragment smuggling, headless flooring, cwd
// delivery, or cleanup semantics. ctx bounds Setup/Execute/Cleanup; stdin may
// be nil for a non-interactive turn; resize may be nil when no SIGWINCH source
// exists.
func RunTurn(ctx context.Context, impl agent.Backend, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (*agent.ExecuteResult, error) {
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

	// dire-petal (SILENT NO-OP, now fixed at the seam): Fragments are converted
	// +delivered to the backend by Setup — the SkipSetup Execute path (built
	// further down as execReq) carries no Fragments field at all. Confirmed
	// live: operations/oneshot.go's runResolvedAgent (the "none"-isolation
	// fan-out/weave member path) sets BOTH SkipSetup:true and
	// Fragments:[{Content: composedContext}] so the shared project cwd is
	// never touched with per-member config — and until this fix, that
	// composed context was silently discarded: the member ran context-free,
	// reported exit 0, and produced plausible output with zero context
	// delivered (prim-bluff had only made the drop visible via clidiag.Warn,
	// not fixed it — see the removed warning this replaces).
	//
	// Fixed by smuggling the fragments into the prompt itself — the one
	// channel a SkipSetup run still has — framed with the EXACT SAME envelope
	// (agent.FrameProjectContext) claude's --append-system-prompt-file
	// delivery already uses for a full-setup run, so a SkipSetup run's
	// content reads identically to what a full-setup run would have written.
	// Every current SkipSetup+Fragments caller (today, only the fan-out/weave
	// "none" member) now gets its content delivered instead of dropped; a
	// caller that only ever meant the bare prompt never sets Fragments in the
	// first place, so this is a strict improvement with no new requirement on
	// anyone. promptContent feeds execReq.Prompt below.
	promptContent := req.GetPrompt().GetContent()
	if opts.GetSkipSetup() {
		if framed := agent.FrameProjectContext(agent.AssembleContext(convertFragments(req.Fragments))); framed != "" {
			promptContent = framed + "\n\n" + promptContent
		}
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
			// Resolved isolation cell, decided host-side and carried on the wire.
			// Setup does not consume it yet (plan S4b) — plumbed for a later slice.
			CellKind: cellKindFromProto(opts.GetCellKind()),
		}
		if err := impl.Setup(ctx, setupReq); err != nil {
			// Fault tolerance (CLAUDE.md): the user must reach their LLM "even
			// through most misconfigurations." Setup now does load-bearing-but-
			// non-essential work — context provision, command registration, settings
			// + hook flush — any of which can fail on a bad write without making the
			// agent unlaunchable. Warn and proceed to Execute rather than aborting,
			// matching the documented startup sequence ("apply hooks: warn on
			// errors, continue" / "always respond with initialized").
			clidiag.Warn("ctxloom", "backend setup failed (launching anyway): %v", err)
		}
	}

	// Build execute request from RunStart. execPrompt carries promptContent
	// (the original prompt, prefixed with any smuggled Fragments above) rather
	// than the raw req.Prompt, but keeps req.Prompt's other fields (Name,
	// Tags, ...) intact when a prompt was actually sent; a nil req.Prompt with
	// smuggled content still needs a Fragment to carry it.
	execPrompt := convertFragment(req.Prompt)
	if promptContent != req.GetPrompt().GetContent() {
		if execPrompt == nil {
			execPrompt = &agent.Fragment{}
		}
		execPrompt.Content = promptContent
	}
	execReq := &agent.ExecuteRequest{
		Prompt:      execPrompt,
		WorkDir:     workDir,
		Mode:        agent.ExecutionMode(opts.GetMode()),
		Model:       opts.GetModel(),
		Env:         env,
		Verbosity:   verbosity,
		DryRun:      opts.GetDryRun(),
		Permissions: agent.WireMode(opts.GetPermissionMode()),
		Temperature: opts.GetTemperature(),
		SkipSetup:   opts.GetSkipSetup(),
		CellKind:    cellKindFromProto(opts.GetCellKind()),
		Stdin:       stdin,
		Resize:      resize,
	}

	// Defense in depth: a ONESHOT has no human to answer the engine, so a
	// would-block posture (default/acceptEdits) must not reach the backend and
	// hang. The CLI resolver already floors this, but a direct gRPC caller might
	// not, so enforce the "headless can't hang" invariant at the decode boundary.
	if execReq.Mode == agent.ModeOneshot && !execReq.Permissions.SafeHeadless() {
		execReq.Permissions = agent.PermissionBypass
	}

	// Make cwd reach the child on EVERY path. Setup calls SetWorkDir, but the
	// SkipSetup fan-out (oneshot/map/weave) skips Setup — so without this the
	// passed WorkDir is dropped and the engine runs in the plugin's inherited
	// "." (the isolation-blocking cwd bug). SetWorkDir lives on BaseBackend
	// (every real backend embeds it) but not the Backend interface, and a
	// bare test fake may lack it, so apply it by capability check. Idempotent
	// with Setup's own SetWorkDir on the non-skip path (same value).
	if w, ok := impl.(interface{ SetWorkDir(string) }); ok {
		w.SetWorkDir(execReq.WorkDir)
	}

	// Execute the backend
	result, err := impl.Execute(ctx, execReq, stdout, stderr)

	// Cleanup runs on both Execute paths — a failed launch must still release
	// the backend's resources. And a teardown hiccup must not mask the Execute
	// result: after a successful run it would report a completed session as
	// "AI plugin failed" (partial success is success, CLAUDE.md), and after a
	// failed one it would bury the real error. Warn and carry on, matching how
	// Setup degrades above.
	if cerr := impl.Cleanup(ctx); cerr != nil {
		clidiag.Warn("ctxloom", "backend cleanup failed: %v", cerr)
	}
	if err != nil {
		return nil, err
	}
	// A buggy backend that returns (nil, nil) must not panic the serving
	// goroutine (CLAUDE.md: log, don't crash). Degrade to a zero-value result
	// so the final message still sends rather than nil-dereferencing below.
	if result == nil {
		clidiag.Warn("ctxloom", "backend returned a nil result with no error; defaulting to exit code 0")
		result = &agent.ExecuteResult{}
	}

	return result, nil
}

// CellKindToProto maps a host-side agent.CellKind to the wire CellKind enum for
// stamping onto RunOptions. Shared stamps the explicit SHARED value (not
// UNSPECIFIED) so the wire records a decided cell; the two isolated kinds map
// one-to-one. Inverse of cellKindFromProto.
func CellKindToProto(k agent.CellKind) CellKind {
	switch k {
	case agent.CellKindDirectoryIsolated:
		return CellKind_CELL_KIND_DIRECTORY_ISOLATED
	case agent.CellKindProcessIsolated:
		return CellKind_CELL_KIND_PROCESS_ISOLATED
	default:
		return CellKind_CELL_KIND_SHARED
	}
}

// cellKindFromProto maps the wire CellKind enum to the plugin-side agent.CellKind.
// UNSPECIFIED (and any unknown) decodes to Shared, matching the enum's documented
// default — a run whose host didn't stamp a cell is treated as the shared cwd.
func cellKindFromProto(k CellKind) agent.CellKind {
	switch k {
	case CellKind_CELL_KIND_DIRECTORY_ISOLATED:
		return agent.CellKindDirectoryIsolated
	case CellKind_CELL_KIND_PROCESS_ISOLATED:
		return agent.CellKindProcessIsolated
	default: // CELL_KIND_UNSPECIFIED, CELL_KIND_SHARED
		return agent.CellKindShared
	}
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
