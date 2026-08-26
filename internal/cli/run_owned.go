package cli

import (
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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Phase 2a-B host side: a TOP-LEVEL container run that is a
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

// ownedRunLaunch is startContainerOwnedRun's request. It is a struct rather
// than a positional list because the launch needs fourteen inputs, three of
// them adjacent strings (harp, context, prompt) that a transposition at the
// call site would swap silently — a run named after its own prompt, with the
// assembled context delivered as the harp, and nothing in the type system
// objecting. A keyed literal makes each value say what it is.
type ownedRunLaunch struct {
	Policy      isolation.Policy
	Workspace   isolation.Workspace
	Req         *pb.RunStart
	BackendName string
	Label       string
	Verbosity   int
	Harp        string
	ContextText string
	Prompt      string
	MCPServers  []agent.ChatMCPServer
	Permission  agent.PermissionMode
	Mode        pb.ExecutionMode
	RunnerEnv   map[string]string
}

// startContainerOwnedRun is the Phase 2a-B launch: subscribe to the coordinator
// event stream, then mint the owner-owned run and spawn its runner via the
// StartRunner keepalive-with-run-id primitive (the runner runs `ctxloom llm
// host` WITH the run-id trio, so EngineHost drives the engine and emits
// AgentEvents over the RunChannel — byte-for-byte the Phase-1 delegated
// runner). The returned RunnerHandle is the caller's teardown handle
// (isolation `docker rm -f` by name); the ownedRunSession carries the outcome +
// event stream the oneshot consumer drives.
func startContainerOwnedRun(ctx context.Context, c *coord.Coordinator, spec ownedRunLaunch) (*isolation.RunnerHandle, *ownedRunSession, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("container oneshot run needs the hosted session coordinator, which failed to stand up")
	}
	stampHostTerminalEnv(spec.Req)

	var handle *isolation.RunnerHandle
	starter := func(sctx context.Context, spawnEnv map[string]string) (func(), error) {
		h, err := spec.Policy.StartRunner(sctx, spec.BackendName, spec.Label, spec.Verbosity, spec.Workspace, spawnEnv)
		if err != nil {
			return nil, err
		}
		handle = h
		// Await the container HERE, inside the starter, not after
		// StartOwnedRun returns. StartRunner returning is not the container
		// running, and the very next thing StartOwnedRun does is wait for the
		// runner to dial home — which a container that never came up can never
		// do, so a check placed after the call would never be reached. The
		// interactive arm learned this; the oneshot arm was left unguarded,
		// which is the arm every CI and acceptance run takes.
		//
		// Kill is returned ALONGSIDE the error so the caller still registers
		// teardown: a container that failed to reach running may still exist.
		if rerr := isolation.AwaitContainerRunning(operations.RuntimeForPolicy(spec.Policy), h); rerr != nil {
			strictness.FailAlways(strictness.ClassIsolation,
				"check the container runtime and the agent image can start (`docker logs `/`podman logs ` the named container); this run cannot fall back to the host without silently dropping the boundary it was given",
				"container %q was started but never reached running state, so the isolation it promised does not exist: %v", h.Name, rerr)
			return h.Kill, rerr
		}
		return h.Kill, nil
	}

	owner, ok := c.Identify(spec.RunnerEnv[coord.EnvCoordCred])
	if !ok {
		// The owner Identity is used only for lineage journaling (ParentHarp /
		// Depth); if the owner token can't be resolved, the session harp at
		// depth 0 is the honest fallback.
		owner = coord.Identity{Harp: spec.Harp}
	}

	// Subscribe BEFORE StartOwnedRun so no delta from the first turn is
	// missed — StartOwnedRun mints the run's ID internally, mid-call, so it
	// cannot be known yet: subscribe(nil) is genuinely hub-wide at this
	// instant. narrow() re-scopes this SAME subscription down to
	// just this run the moment StartOwnedRun returns with the real ID below,
	// so this run only shares its ring's delivery budget with every other
	// concurrent run on this coordinator for the brief window before that —
	// not for its whole lifetime.
	_, events, cancel, narrow := c.WatchRuns(nil)

	lead := operations.JoinLeadBlocks(spec.ContextText, spec.Prompt)
	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnerRunSpec{
		Harp:       spec.Harp,
		Backend:    spec.BackendName,
		Label:      spec.Label,
		Model:      spec.Req.GetOptions().GetModel(),
		WorkDir:    spec.Req.GetOptions().GetWorkDir(),
		Env:        spec.Req.GetOptions().GetEnv(),
		MCPServers: spec.MCPServers,
		Permission: spec.Permission,
		Oneshot:    spec.Mode == pb.ExecutionMode_ONESHOT,
	}, starter, lead)
	if err != nil {
		cancel()
		return handle, nil, err
	}
	narrow(outcome.RunID)
	return handle, &ownedRunSession{coord: c, outcome: outcome, events: events, cancel: cancel}, nil
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
	// text is handed over the turn-boundary channel. It used to be a
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
		// answer (the same race as above, on the flush side).
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
	// BOTH --print arms close out through recordOneshotAnswer, so
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
// the same NDJSON entry contract renderChatEvents/chatEventToJSON emit for the
// go-plugin arm — run_structured.go), signals each turn boundary on turnIdle
// (non-blocking) carrying the answer text accumulated so far, and returns
// that text plus nil at RunCompleted or ctx.Err() on cancellation.
// REASONING/LOG channels are excluded — the host renders the answer, not the
// scratchpad, exactly like accumulateFinalText.
//
// The accumulator is LOCAL by construction: every read by another
// goroutine goes through turnIdle or the return value, so there is no shared
// buffer to race on. capture is always true for this arm's one remaining
// caller (runOneshotViaCoord); it stays a parameter because every test below
// drives this renderer directly and several assert on the RETURNED text.
func renderOwnedRunEvents(ctx context.Context, out io.Writer, format, runID string, events <-chan *agentcoordpb.AgentEvent, turnIdle chan<- string, capture bool) (string, error) {
	// Reject a format this renderer cannot honor BEFORE consuming the stream,
	// on the same text/json pair renderChatEvents enforces (format.go). Which
	// arm a container run takes is an isolation-policy decision, not the
	// user's, so a --format value must mean the same thing on both: falling
	// through to raw prose here made an unsupported format an error on the
	// go-plugin arm and a silent downgrade on this one.
	switch format {
	case formatJSON, formatText, "":
	default:
		return "", unknownFormatError(format)
	}

	var answer strings.Builder
	final := map[string]bool{}
	enc := json.NewEncoder(out)
	// The watchHub ring (consumer.go) this channel is fed from is a
	// lossy, per-subscriber buffer that a busy coordinator's OTHER concurrent
	// runs can also fill (this run's subscription is scoped, but the ring
	// still drops under sustained overload); nothing upstream re-sends a
	// dropped event. Every event this run's own EngineHost emits carries a
	// strictly contiguous per-run Seq (home.go's emitEvent), so a hole in
	// that sequence is the only signal available that this stream has
	// silently lost data. lastSeq tracks the highest Seq observed for THIS
	// run; Seq 0 (unset — e.g. test fixtures, or any future event type that
	// does not route through emitEvent) is never treated as a gap.
	var lastSeq uint64
	for {
		select {
		case ev := <-events:
			if ev.GetRunId() != runID {
				continue
			}
			if seq := ev.GetSeq(); seq != 0 {
				if lastSeq != 0 && seq > lastSeq+1 {
					missed := seq - lastSeq - 1
					clidiag.Warn("ctxloom", "run %s: lost %d event(s) between seq %d and %d — the coordinator's live event buffer was full (likely other concurrent runs competing for it) and dropped them; this run's rendered output may be INCOMPLETE", runID, missed, lastSeq, seq)
				}
				lastSeq = seq
			}
			switch p := ev.GetPayload().(type) {
			case *agentcoordpb.AgentEvent_MessageStarted:
				if p.MessageStarted.GetChannel() == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL {
					final[p.MessageStarted.GetMessageId()] = true
				}
			case *agentcoordpb.AgentEvent_MessageCompleted:
				// The item contract is started -> delta* -> completed, so a
				// completed id is spent: releasing it keeps this map sized to
				// the messages currently OPEN rather than to every message the
				// session has ever produced (a warm engine's run is long-lived
				// and its message ids are monotonic, so nothing here is ever
				// reused). Mirrors the sibling adapter over this same stream,
				// internal/operations/sessionfeed.go, which drops its
				// per-message state at the same point.
				delete(final, p.MessageCompleted.GetMessageId())
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
					if err := enc.Encode(chatEventJSON{Type: chatEventTypeEntry, Entry: &chatEntryJSON{
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
				// SUCCESS IS AN ALLOW-LIST. This used to test
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
