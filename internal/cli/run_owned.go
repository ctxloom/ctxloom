package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Phase 2a-B host side: a TOP-LEVEL container run that is --structured or a
// --print oneshot no longer constructs a go-plugin client. It mints an
// owner-owned run in the in-process coordinator (Coordinator.StartOwnedRun),
// spawns the runner via the SAME StartRunner primitive Phase 1/Part A use, and
// drives the session by WATCHING that run's event stream over the in-process
// Coordinator.WatchRuns — the container dials out on Transport 2, opening no
// in-container listener. Host/worktree top-level runs stay on go-plugin (§0.4);
// only a container policy reaches here.

// ownedRunSession bundles a live owner-owned run with its pre-subscribed event
// stream. The subscription is opened BEFORE StartOwnedRun so the first turn's
// deltas are never missed in the gap between StartRun round-tripping and the
// consumer subscribing.
type ownedRunSession struct {
	coord   *coord.Coordinator
	outcome *coord.RunOutcome
	events  <-chan *agentcoordpb.AgentEvent
	cancel  func()
}

// selectsOwnedRunContainer reports whether a top-level built-in run takes the
// Phase 2a-B Transport 2 / EngineHost arm: a container policy that is either
// --structured (a turn REPL) or a --print oneshot. Interactive container runs
// take Part A's docker-exec arm; host/worktree runs of any mode stay on
// SpawnClient + go-plugin (they never had the mauve-state problem this fixes).
func selectsOwnedRunContainer(policyName string, mode pb.ExecutionMode, structured bool) bool {
	if !isolation.IsContainerPolicyName(policyName) {
		return false
	}
	return structured || mode == pb.ExecutionMode_ONESHOT
}

// startContainerOwnedRun is the Phase 2a-B launch: subscribe to the coordinator
// event stream, then mint the owner-owned run and spawn its runner via the
// StartRunner keepalive-with-run-id primitive (the runner runs `ctxloom llm
// host` WITH the run-id trio, so EngineHost drives the engine and emits
// AgentEvents over the RunChannel — byte-for-byte the Phase-1 delegated
// runner). The returned RunnerHandle is the caller's teardown handle
// (isolation `docker rm -f` by name); the ownedRunSession carries the outcome +
// event stream the structured/oneshot consumers drive.
func startContainerOwnedRun(ctx context.Context, c *coord.Coordinator, policy isolation.Policy, ws isolation.Workspace, req *pb.RunStart, backendName, label string, verbosity int, harp, contextText, prompt string, mcpServers []agent.ChatMCPServer, perm agent.PermissionMode, mode pb.ExecutionMode, structured bool, runnerEnv map[string]string) (*isolation.RunnerHandle, *ownedRunSession, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("container structured/oneshot run needs the hosted session coordinator, which failed to stand up")
	}
	stampHostTerminalEnv(req)

	var handle *isolation.RunnerHandle
	starter := func(sctx context.Context, spawnEnv map[string]string) (func(), error) {
		h, err := policy.StartRunner(sctx, backendName, label, verbosity, ws, spawnEnv)
		if err != nil {
			return nil, err
		}
		handle = h
		return h.Kill, nil
	}

	owner, ok := c.Identify(runnerEnv[coord.EnvCoordCred])
	if !ok {
		// The owner Identity is used only for lineage journaling (ParentHarp /
		// Depth); if the owner token can't be resolved, the session harp at
		// depth 0 is the honest fallback.
		owner = coord.Identity{Harp: harp}
	}

	// Subscribe BEFORE StartOwnedRun so no delta from the first turn is missed.
	_, events, cancel := c.WatchRuns(nil)

	lead := operations.JoinLeadBlocks(contextText, prompt)
	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnerRunSpec{
		Harp:       harp,
		Backend:    backendName,
		Label:      label,
		Model:      req.GetOptions().GetModel(),
		WorkDir:    req.GetOptions().GetWorkDir(),
		Env:        req.GetOptions().GetEnv(),
		MCPServers: mcpServers,
		Permission: perm,
		Oneshot:    mode == pb.ExecutionMode_ONESHOT && !structured,
	}, starter, lead)
	if err != nil {
		cancel()
		return handle, nil, err
	}
	return handle, &ownedRunSession{coord: c, outcome: outcome, events: events, cancel: cancel}, nil
}

// runStructuredREPLViaCoord is the Transport 2 counterpart of runStructuredREPL
// (plan §5.B1): instead of client.Chat, it renders the owner-owned run's event
// stream from WatchRuns and feeds one stdin line as one SendOwnedRunTurn.
// EOF closes input; the loop then drains any in-flight turn to its boundary
// before returning. A terminal run event (RunCompleted) ends the exchange.
func runStructuredREPLViaCoord(ctx context.Context, sess *ownedRunSession, format string, stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runID := sess.outcome.RunID

	// Buffered and drained only for its arrival signal: the REPL counts turn
	// boundaries, it does not collect the answer (capture=false, so every value
	// is empty).
	turnIdle := make(chan string, 256)
	renderErr := make(chan error, 1)
	go func() {
		_, err := renderOwnedRunEvents(ctx, stdout, format, runID, sess.events, turnIdle, false)
		renderErr <- err
	}()

	lines := make(chan string)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- decodeMessageLine(scanner.Text()):
			case <-ctx.Done():
				scanDone <- ctx.Err()
				return
			}
		}
		scanDone <- scanner.Err()
	}()

	// The StartRun first turn is already in flight (pending=1); each stdin line
	// adds one, each turn boundary retires one. At EOF we return once every
	// issued turn has reached its boundary.
	pending := 1
	stdinOpen := true
	for {
		if !stdinOpen && pending <= 0 {
			return nil
		}
		select {
		case <-turnIdle:
			pending--
		case line := <-lines:
			if err := sess.coord.SendOwnedRunTurn(runID, line); err != nil {
				return err
			}
			pending++
		case err := <-scanDone:
			stdinOpen = false
			lines = nil
			if err != nil {
				return err
			}
		case err := <-renderErr:
			// The run terminated (RunCompleted) or the stream errored.
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runOneshotViaCoord drives a --print container oneshot over Transport 2: it
// streams the run's FINAL-channel answer to stdout as it arrives, and at the
// turn boundary records the canonical two-entry oneshot transcript
// (transcript.RecordOneshot — the silent-no-op guard) from the collected text.
// The run's warm engine does not emit RunCompleted after a single turn, so the
// turn boundary (ctxloom/turn_idle) is the completion signal; a RunCompleted
// (engine exit) is honored too. The exit code follows the run's status.
func runOneshotViaCoord(ctx context.Context, sess *ownedRunSession, harp, backend, prompt string, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runID := sess.outcome.RunID

	// The answer buffer belongs to the RENDER goroutine, and the accumulated
	// text is handed over the turn-boundary channel (U041-F04). It used to be a
	// strings.Builder shared with this goroutine, read the instant turnIdle
	// fired — but that boundary is signalled from inside the very loop that
	// keeps appending, so the read raced every delta still in flight. A race
	// here does not merely trip the detector: it truncates or garbles the
	// answer of a run that reports success.
	turnIdle := make(chan string, 4)
	rendered := make(chan ownedRenderResult, 1)
	go func() {
		text, err := renderOwnedRunEvents(ctx, stdout, formatText, runID, sess.events, turnIdle, true)
		rendered <- ownedRenderResult{text: text, err: err}
	}()

	var text string
	var runErr error
	select {
	case text = <-turnIdle:
		// One turn completed: the answer is fully streamed. Stop the renderer
		// and WAIT for it before this goroutine touches stdout below — it is
		// still selecting on events, and two goroutines writing the same
		// stdout is how the trailing newline ends up in the middle of the
		// answer (the second half of U041-F04).
		cancel()
		<-rendered
	case res := <-rendered:
		// The run terminated before/at the first boundary.
		text, runErr = res.text, res.err
	case <-ctx.Done():
		return ctx.Err()
	}

	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(stdout)
	}
	// U041-F01/F02: BOTH --print arms close out through recordOneshotAnswer, so
	// "the engine answered nothing" is one rule with one message rather than a
	// warning on this arm and no check at all on the go-plugin one. Capture runs
	// even on a nonzero exit (partial prose is still real memory of what
	// happened), but the run's own failure takes precedence — it already said
	// what went wrong.
	captureErr := recordOneshotAnswer(harp, backend, prompt, text)
	if runErr != nil {
		return runErr
	}
	return captureErr
}

// ownedRenderResult carries the render goroutine's two outputs — the answer it
// accumulated and why it stopped — over one channel, so neither is read from
// the goroutine's own memory by anybody else.
type ownedRenderResult struct {
	text string
	err  error
}

// renderOwnedRunEvents renders one owner-owned run's AgentEvent stream: it
// forwards FINAL-channel message deltas (text mode → prose to out; json mode →
// the NDJSON entry contract runStructuredREPL's frontend already consumes),
// signals each turn boundary on turnIdle (non-blocking) carrying the answer
// text accumulated so far, and returns that text plus nil at RunCompleted or
// ctx.Err() on cancellation. REASONING/LOG channels are excluded — the host
// renders the answer, not the scratchpad, exactly like accumulateFinalText.
//
// The accumulator is LOCAL by construction (U041-F04): every read by another
// goroutine goes through turnIdle or the return value, so there is no shared
// buffer to race on. capture=false skips accumulation entirely for the REPL,
// which only counts boundaries.
func renderOwnedRunEvents(ctx context.Context, out io.Writer, format, runID string, events <-chan *agentcoordpb.AgentEvent, turnIdle chan<- string, capture bool) (string, error) {
	var answer strings.Builder
	final := map[string]bool{}
	enc := json.NewEncoder(out)
	for {
		select {
		case ev := <-events:
			if ev.GetRunId() != runID {
				continue
			}
			switch p := ev.GetPayload().(type) {
			case *agentcoordpb.AgentEvent_MessageStarted:
				if p.MessageStarted.GetChannel() == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL {
					final[p.MessageStarted.GetMessageId()] = true
				}
			case *agentcoordpb.AgentEvent_MessageDelta:
				if !final[p.MessageDelta.GetMessageId()] {
					continue
				}
				text := p.MessageDelta.GetText()
				if text == "" {
					continue
				}
				if capture {
					answer.WriteString(text)
				}
				switch format {
				case formatJSON:
					if err := enc.Encode(chatEventJSON{Type: "entry", Entry: &chatEntryJSON{
						Type: string(agent.EntryTypeAssistant), Content: text,
					}}); err != nil {
						return answer.String(), err
					}
				default:
					if _, err := io.WriteString(out, text); err != nil {
						return answer.String(), err
					}
				}
			case *agentcoordpb.AgentEvent_Custom:
				if p.Custom.GetName() == coord.CustomTurnIdle {
					select {
					case turnIdle <- answer.String():
					default:
					}
				}
			case *agentcoordpb.AgentEvent_RunCompleted:
				// SUCCESS IS AN ALLOW-LIST (U016-F06). This used to test
				// `== RUN_STATUS_FAILED`, which made the enum's zero value —
				// and CANCELLED, TIMED_OUT, BUDGET_EXCEEDED, and any value
				// proto3's open enums let through — exit 0. A run that was
				// killed or ran out of time reported success to the shell.
				// Same discipline as the approval ladder's
				// interactionResolution: only SUCCEEDED succeeds.
				r := p.RunCompleted.GetResult()
				if r.GetStatus() == agentcoordpb.Result_RUN_STATUS_SUCCEEDED {
					return answer.String(), nil
				}
				if msg := r.GetError().GetMessage(); msg != "" {
					clidiag.Warn("ctxloom", "run failed: %s", msg)
				} else if r == nil {
					clidiag.Warn("ctxloom", "run ended with no terminal result — treating as a failure")
				} else if r.GetStatus() != agentcoordpb.Result_RUN_STATUS_FAILED {
					clidiag.Warn("ctxloom", "run did not succeed: %s", r.GetStatus())
				}
				return answer.String(), &ExitError{Code: 1}
			}
		case <-ctx.Done():
			return answer.String(), ctx.Err()
		}
	}
}
