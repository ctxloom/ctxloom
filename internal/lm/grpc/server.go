package grpc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/version"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LLMGRPCPlugin is the implementation of plugin.GRPCPlugin for AI backends.
type LLMGRPCPlugin struct {
	// This plugin speaks gRPC only. Embedding NetRPCUnsupportedPlugin rather
	// than the bare plugin.Plugin interface satisfies that interface with
	// methods that REFUSE net/rpc: a net/rpc dial then fails diagnosably
	// instead of dereferencing a nil embedded interface inside the host.
	plugin.NetRPCUnsupportedPlugin
	// Impl is the concrete backend implementation.
	// This is only set on the server (plugin) side.
	Impl agent.Backend
	// WrapStreams, when set, replaces an interactive turn's stdin/stdout with
	// the pair it returns before Execute sees them — a hook for injecting
	// into the engine's own terminal, not a decorator on Impl. A decorator
	// that embeds agent.Backend only promotes Backend's own methods, so it
	// silently erases whichever optional capability interfaces (
	// agent.StructuredChat, agent.StateReader, agent.EngineCLIProvider) the
	// wrapped value implements — this is the deliberate alternative to that.
	// Unset means identity: this plugin injects nothing.
	WrapStreams func(io.Reader, io.Writer) (io.Reader, io.Writer)
}

// GRPCServer returns the gRPC server for the plugin.
func (p *LLMGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterLLMServer(s, &GRPCServer{Impl: p.Impl, wrapStreams: p.WrapStreams})
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
	// wrapStreams is LLMGRPCPlugin.WrapStreams, threaded through GRPCServer so
	// Run can hand it to RunTurn. See LLMGRPCPlugin.WrapStreams for why this is
	// a func value and not a Backend decorator.
	wrapStreams func(io.Reader, io.Writer) (io.Reader, io.Writer)
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
		// The SERVING PROCESS's own build stamp — read fresh on every call
		// (never cached at plugin.Serve startup) so a client's version
		// handshake sees this exact process's compiled-in behavior, not
		// whatever version.Version happened to read at boot.
		CtxloomVersion: version.Version,
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
		return status.Error(codes.InvalidArgument, "first Run message must carry start")
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

	// This goroutine owns stdinR, so it supplies the release: closing the read
	// end is what makes the pump's stdinW.Write above fail with ErrClosedPipe
	// instead of parking forever, and it is also what unblocks any reader the
	// wake has wrapped around it. Nothing downstream can infer this — passing
	// nil here silently restores the wedge.
	result, err := RunTurn(stream.Context(), s.Impl, req, stdinR, func() { _ = stdinR.Close() }, stdoutWriter, stderrWriter, resizeCh, s.wrapStreams)
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
//
// stdinCleanup releases stdin once the engine's pty stops reading it, and only
// the caller that CREATED stdin may supply one. The two transports differ here,
// and the difference is the whole reason this is a parameter: GRPCServer.Run
// owns an io.Pipe and must close its read end (a stream write into a pipe with
// no reader parks forever and no cancellation reaches it), while `ctxloom llm
// turn` passes the process's real os.Stdin, which it does not own and must
// never close. RunTurn cannot tell those apart by looking at the reader — and
// the layer below once tried to, by type assertion, which stopped being true
// the moment the terminal wake began wrapping stdin.
//
// wrapStreams, when non-nil, replaces an interactive turn's stdin/stdout with
// the pair it returns immediately before Execute — the seam a session-owner's
// terminal wake injects into (coord.NewTerminalInjector, wired in by
// llm_serve.go). It is a plain func value rather than a Backend decorator
// specifically so it cannot erase an optional capability interface
// (agent.StructuredChat, agent.StateReader, agent.EngineCLIProvider) that impl
// implements: a decorator embedding agent.Backend only promotes Backend's own
// methods, so wrapping impl itself would make every caller downstream of
// RunTurn lose those assertions on the wrapped value. A oneshot turn's Stdin
// is nil by contract (agent.LaunchBackend.ExecuteCLI's doc comment) and is
// never wrapped.
func RunTurn(ctx context.Context, impl agent.Backend, req *RunStart, stdin io.Reader, stdinCleanup func(), stdout, stderr io.Writer, resize <-chan agent.WindowSize, wrapStreams func(io.Reader, io.Writer) (io.Reader, io.Writer)) (*agent.ExecuteResult, error) {
	// Treat nil Options as fully-default so callers using proto-zero-values
	// don't crash — the generated Get* accessors are nil-safe throughout.
	env := req.GetOptions().GetEnv()
	if env == nil {
		env = make(map[string]string)
	}

	promptContent := turnPromptContent(req)
	runTurnSetup(ctx, impl, req, env)
	execReq := turnExecuteRequest(req, promptContent, env, stdin, stdinCleanup, resize)
	if wrapStreams != nil && execReq.Mode == agent.ModeInteractive && execReq.Stdin != nil {
		wrappedStdin, wrappedStdout := wrapStreams(execReq.Stdin, stdout)
		execReq.Stdin = wrappedStdin
		stdout = wrappedStdout
	}

	// Make cwd reach the child on EVERY path. Setup calls SetWorkDir, but the
	// SkipSetup oneshot path (run --print, delegated agent_run's oneshot
	// fallback) skips Setup — so without this the
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
	// Setup degrades.
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

// turnPromptContent is the prompt text Execute will receive: the sent prompt,
// prefixed with this turn's Fragments when Setup is being skipped.
//
// dire-petal (SILENT NO-OP, fixed at the seam): Fragments are converted and
// delivered to the backend by Setup — the SkipSetup Execute path carries no
// Fragments field at all. Confirmed live: operations/oneshot.go's
// runResolvedAgent (the "none"-isolation oneshot member path) sets BOTH
// SkipSetup:true and Fragments:[{Content: composedContext}] so the shared
// project cwd is never touched with per-member config — and that composed
// context was silently discarded: the member ran context-free, reported exit 0,
// and produced plausible output with zero context delivered.
//
// The fragments are smuggled into the prompt itself — the one channel a
// SkipSetup run still has — framed with the EXACT SAME envelope
// (agent.FrameProjectContext) claude's --append-system-prompt-file delivery
// already uses for a full-setup run, so a SkipSetup run's content reads
// identically to what a full-setup run would have written. A caller that only
// ever meant the bare prompt never sets Fragments in the first place.
func turnPromptContent(req *RunStart) string {
	content := req.GetPrompt().GetContent()
	if !req.GetOptions().GetSkipSetup() {
		return content
	}
	if framed := agent.FrameProjectContext(agent.AssembleContext(convertFragments(req.Fragments))); framed != "" {
		return framed + "\n\n" + content
	}
	return content
}

// runTurnSetup runs the backend's Setup for this turn, unless the turn asked to
// skip it (distillation/minimal mode).
//
// Fault tolerance (CLAUDE.md): the user must reach their LLM "even through most
// misconfigurations." Setup does load-bearing-but-non-essential work — context
// provision, command registration, settings + hook flush — any of which can
// fail on a bad write without making the agent unlaunchable. A failure is
// warned and the turn proceeds to Execute, matching the documented startup
// sequence ("apply hooks: warn on errors, continue" / "always respond with
// initialized"). It is therefore not an error the caller can act on.
func runTurnSetup(ctx context.Context, impl agent.Backend, req *RunStart, env map[string]string) {
	opts := req.GetOptions()
	if opts.GetSkipSetup() {
		return
	}
	setupReq := &agent.SetupRequest{
		WorkDir:   opts.GetWorkDir(),
		Fragments: convertFragments(req.Fragments),
		Env:       env,
		Verbosity: opts.GetVerbosity(),
		// Host-assembled config/bundle setup payload (nil when the host
		// sent none, e.g. skip_setup). Converted from proto back to the
		// wire-typed Go form the agent's Setup consumes.
		Managed: managedConfigFromProto(req.GetManagedConfig()),
		// Resolved isolation cell, decided host-side and carried on the wire.
		// Setup does not consume it yet (plan S4b) — plumbed for a later slice.
		CellKind: cellKindFromProto(opts.GetCellKind()),
	}
	if err := impl.Setup(ctx, setupReq); err != nil {
		clidiag.Warn("ctxloom", "backend setup failed (launching anyway): %v", err)
	}
}

// turnExecuteRequest decodes a RunStart into the backend's ExecuteRequest. It
// is the whole decode boundary for a turn: pure, so every field the wire
// carries is translated in one place.
//
// execPrompt carries promptContent (the sent prompt, prefixed with any smuggled
// Fragments — see turnPromptContent) rather than the raw req.Prompt, but keeps
// req.Prompt's other fields (Name, Tags, ...) intact when a prompt was actually
// sent; a nil req.Prompt with smuggled content still needs a Fragment to carry
// it.
func turnExecuteRequest(req *RunStart, promptContent string, env map[string]string, stdin io.Reader, stdinCleanup func(), resize <-chan agent.WindowSize) *agent.ExecuteRequest {
	opts := req.GetOptions()
	execPrompt := convertFragment(req.Prompt)
	if promptContent != req.GetPrompt().GetContent() {
		if execPrompt == nil {
			execPrompt = &agent.Fragment{}
		}
		execPrompt.Content = promptContent
	}
	execReq := &agent.ExecuteRequest{
		Prompt:      execPrompt,
		WorkDir:     opts.GetWorkDir(),
		Mode:        agent.ExecutionMode(opts.GetMode()),
		Model:       opts.GetModel(),
		Env:         env,
		Verbosity:   opts.GetVerbosity(),
		DryRun:      opts.GetDryRun(),
		Permissions: agent.WireMode(opts.GetPermissionMode()),
		Temperature: opts.GetTemperature(),
		SkipSetup:   opts.GetSkipSetup(),
		CellKind:    cellKindFromProto(opts.GetCellKind()),
		Stdin:       stdin,
		Resize:      resize,

		// Travels with Stdin, never derived from it: only the caller that made
		// the reader knows whether releasing it is legal.
		StdinCleanup: stdinCleanup,
	}

	// Defense in depth: a ONESHOT has no human to answer the engine, so a
	// would-block posture (default/acceptEdits) must not reach the backend and
	// hang. The CLI resolver already floors this, but a direct gRPC caller might
	// not, so enforce the "headless can't hang" invariant at the decode boundary.
	if execReq.Mode == agent.ModeOneshot && !execReq.Permissions.SafeHeadless() {
		execReq.Permissions = agent.PermissionBypass
	}
	return execReq
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
