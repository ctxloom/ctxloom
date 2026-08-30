package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// runStartHandoffFile is the fixed name of the 0600 RunStart handoff dropped in
// the session persist dir (§5.A4). Never argv/env: RunStart can carry fragments
// and env values, and argv is world-readable via /proc/<pid>/cmdline.
const runStartHandoffFile = "runstart.json"

// llmTurnCmd is the docker-exec INTERACTIVE half:
// `ctxloom llm turn <backend> --start <path>` runs the Run-RPC body
// (grpc.RunTurn: Setup → Execute → Cleanup) DIRECTLY on the exec's TTY instead
// of behind a go-plugin bidi stream. The host launches it via
// `docker|podman exec -it <container> …` (internal/vpio/dockerexec) into the
// already-running StartRunner keepalive container; stdin/stdout are the exec
// TTY, and resize is fed by the process's own SIGWINCH (the daemon retargets
// the container TTY on a host-side resize). It opens NO plugin listener — the
// exec rides the daemon's control socket.
var llmTurnCmd = &cobra.Command{
	Use:    "turn <backend>",
	Short:  "Run one interactive engine turn on this TTY (internal use, docker-exec transport)",
	Long:   `Runs one interactive engine turn directly on the current TTY for the specified backend, reading its RunStart from the --start handoff file. Used internally by the docker-exec interactive launcher; opens NO plugin listener.`,
	Args:   cobra.ExactArgs(1),
	Hidden: true,
	RunE:   runLLMTurn,
}

func runLLMTurn(cmd *cobra.Command, args []string) error {
	// NOTE (terminal-query suppression lives in the Launcher, not here):
	// ctxloom's package-INIT terminal-capability detection (termenv querying
	// the background via OSC 11 + a DSR terminator) fires before this RunE —
	// before main() even — so it can only be silenced by the process's TERM,
	// which the docker-exec Launcher forces to `dumb` for the turn process
	// (dockerexec.turnTermValue). The engine child still gets real color via
	// RunStart.Options.Env's TERM (stamped by startContainerInteractive),
	// which overrides the turn's dumb in the child's BuildEnv.

	// Fail-loudly gate: same shape as `llm serve`/`llm host` —
	// checkpoint before standUpRunner's config load. Unlike serve/host, a
	// standUpRunner ERROR here is deliberately downgraded to a warning
	// below (interactive turn has no RunID, so no EngineHost, and an MCP
	// hiccup degrades to the shim's local fallback rather than failing the
	// turn) — but a fatal-class FINDING (a corrupted/malformed
	// config.yaml) is a different, stronger signal and still aborts unless
	// --degraded, same as the other two process-owning entry points.
	gates := newPhaseGates(os.Stderr)

	backendName := args[0]
	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}
	if llmTurnStartPath == "" {
		return fmt.Errorf("llm turn: --start (the RunStart handoff path) is required")
	}
	req, err := readRunStartHandoff(llmTurnStartPath)
	if err != nil {
		return err
	}

	// Stand up the runner-local MCP surface (config + socket + marker +
	// CTXLOOM_MCP_SOCKET export) so the engine child spawned by Execute has
	// its ctxloom tools and coordinator reach-back — the same standup `llm
	// serve`/`llm host` run, which also consumes+scrubs the reach-back trio
	// so the engine never inherits it. Best-effort: with no trio in the env
	// it is a no-op, and an MCP hiccup on this interactive path degrades the
	// shim to its local fallback rather than failing the turn (there is no
	// hosted delegated run here — no RunID, so no EngineHost).
	standup, serr := standUpRunner(cmd, backend, backendName, llmTurnLabel)
	if serr != nil {
		clidiag.Warn("ctxloom", "runner MCP standup for interactive turn failed (continuing without it): %v", serr)
		standup = &runnerStandup{}
	}
	defer standup.teardown()

	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}

	// Resize: the exec TTY's SIGWINCH (delivered by the daemon when the host
	// resizes) drives the engine's pty via the same watchResize the go-plugin
	// frontend uses, adapted to RunTurn's agent.WindowSize channel.
	resize := turnResize(cmd.Context(), os.Stdin)

	// nil stdinCleanup: os.Stdin is the PROCESS's terminal, owned by this
	// process for its whole life and shared with everything else that reads or
	// writes it. Nothing downstream may close it — a close here would take the
	// terminal out from under the rest of the command, and it is not ours to
	// take. A cleanup func is only ever supplied by the layer that CREATED the
	// reader (the go-plugin transport, which makes an io.Pipe and must close
	// its read end); a caller that merely borrows a reader passes nil.
	//
	// This is a decision, not an omission. It used to hold by accident: the
	// layer below type-asserted stdin to *io.PipeReader and so happened to skip
	// a real terminal. That accident is gone, and the same protection is now
	// stated here on purpose.
	//
	// nil wrapStreams: the docker-exec transport has no session-owner terminal
	// wake to inject into (llm_serve.go wires WrapStreams only for the
	// go-plugin transport's session-owner case).
	result, err := pb.RunTurn(cmd.Context(), backend, req, os.Stdin, nil, os.Stdout, os.Stderr, resize, nil)
	if err != nil {
		return fmt.Errorf("interactive turn failed: %w", err)
	}
	if result.ExitCode != 0 {
		return &ExitError{Code: int(result.ExitCode)}
	}
	return nil
}

// writeRunStartHandoff serializes req (protojson) to a 0600 file in the session
// persist dir (~/.ctxloom/sessions/<harp>/persist/runstart.json) and returns
// its HOST path. That dir is bind-mounted into the StartRunner container, so the
// in-container turn process reads the SAME file at the container-home-rebased
// path (see operations.ContainerPersistDirForPolicy). The host hands off by
// file — never argv/env — because RunStart can be large (fragments) and can
// carry env values, and argv is world-readable.
func writeRunStartHandoff(harp string, req *pb.RunStart) (string, error) {
	if runStartCarriesNothing(req) {
		return "", fmt.Errorf("run-start handoff: refusing to hand off a RunStart that carries no options, prompt, fragments or managed config — the turn would launch the engine context-free")
	}
	persist, err := paths.HarpPersistDir(harp)
	if err != nil {
		return "", fmt.Errorf("run-start handoff: %w", err)
	}
	if err := os.MkdirAll(persist, 0o755); err != nil {
		return "", fmt.Errorf("run-start handoff: create persist dir: %w", err)
	}
	data, err := protojson.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("run-start handoff: marshal: %w", err)
	}
	path := filepath.Join(persist, runStartHandoffFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("run-start handoff: write %s: %w", path, err)
	}
	return path, nil
}

// readRunStartHandoff decodes the RunStart the host wrote and, ON SUCCESS,
// DELETES the file so the payload does not linger in the bind-mounted dir. A
// missing/corrupt file is a loud error — never a silent empty RunStart (which
// would run the engine context-free, the project's signature silent-no-op).
//
// The unlink is deliberately NOT deferred: it scrubs a payload that reached the
// engine, so a payload that failed to decode must survive as the only evidence
// of why the turn failed and the only way to reproduce it.
func readRunStartHandoff(path string) (*pb.RunStart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("run-start handoff: read %s: %w", path, err)
	}
	var req pb.RunStart
	if err := protojson.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("run-start handoff: decode %s: %w", path, err)
	}
	if runStartCarriesNothing(&req) {
		return nil, fmt.Errorf("run-start handoff: %s decoded to a RunStart carrying no options, prompt, fragments or managed config — refusing to run the engine context-free", path)
	}
	_ = os.Remove(path)
	return &req, nil
}

// runStartCarriesNothing reports whether req would deliver nothing at all to
// the engine: no options (work dir / env), no prompt, no fragments and no
// managed config. It is the floor BOTH halves of the handoff enforce, so an
// empty payload cannot be published by the host nor consumed by the turn.
//
// The test is over the WHOLE message rather than any single field because each
// field alone is legitimately absent: an interactive turn carries no prompt and
// no fragments (the user types on the engine's own TTY), and a skip_setup run
// carries no managed config. Only a wholly empty RunStart is a defect, and it
// is one the production writer cannot produce (stampHostTerminalEnv always
// stamps Options) — which is exactly why it read as success.
func runStartCarriesNothing(req *pb.RunStart) bool {
	return req == nil ||
		(req.GetOptions() == nil &&
			req.GetPrompt() == nil &&
			len(req.GetFragments()) == 0 &&
			req.GetManagedConfig() == nil)
}

// turnResize adapts the cross-platform watchResize (SIGWINCH-sourced
// pb.WindowSize on unix; the single ConPTY size on Windows) to RunTurn's
// agent.WindowSize channel, closing when watchResize closes (ctx done). The
// in-container turn always runs on Linux, where this delivers each SIGWINCH the
// daemon raises after a host-side resize.
func turnResize(ctx context.Context, f *os.File) <-chan agent.WindowSize {
	src := watchResize(ctx, f)
	out := make(chan agent.WindowSize, 1)
	go func() {
		defer close(out)
		for ws := range src {
			s := agent.WindowSize{Rows: uint16(ws.GetRows()), Cols: uint16(ws.GetCols())}
			select {
			case out <- s:
			default:
				// Latest-wins: evict the stale pending size, then send the newer.
				select {
				case <-out:
				default:
				}
				out <- s
			}
		}
	}()
	return out
}

// llmTurnLabel / llmTurnStartPath are turn's flags, the counterparts to `llm
// serve`/`llm host`'s label seam plus the RunStart handoff path.
var (
	llmTurnLabel     string
	llmTurnStartPath string
)

func init() {
	llmCmd.AddCommand(llmTurnCmd)
	llmTurnCmd.Flags().StringVar(&llmTurnLabel, "label", "", "Config label to apply (internal; passed by the docker-exec launcher)")
	llmTurnCmd.Flags().StringVar(&llmTurnStartPath, "start", "", "Path to the serialized RunStart handoff file (internal)")
}
