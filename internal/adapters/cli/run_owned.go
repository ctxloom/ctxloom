package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The owner-owned run: `ctxloom run` mints a run in the coordinator it hosts
// in-process (Coordinator.StartOwnedRun), starts its runner through the
// launch's starter, and drives the session by WATCHING that run's event
// stream over the in-process Coordinator.WatchRuns. The runner dials out on
// the RunnerChannel; nothing listens in the cell.

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

// ownedRunLaunch is startOwnedRun's request: the owner's resolved launch,
// the composed MCP names the enqueue journal records, the credential the
// coordinator registered this session's owner under (it identifies the
// owner), and the rebind that answers a runner which cannot bind the launch's
// endpoint (coord.OwnerRun.Rebind). A keyed literal makes each value say what
// it is.
type ownedRunLaunch struct {
	Launch      launch.Launch
	MCPServers  []agent.ChatMCPServer
	OwnerToken  string
	MayDelegate []string // coord.OwnerRun.MayDelegate
	Rebind      func(ctx context.Context, l launch.Launch) (launch.Launch, error)
}

// endpointRebinder is the OwnerRun.Rebind for a launch resolved against deps.
func endpointRebinder(deps launch.Deps) func(context.Context, launch.Launch) (launch.Launch, error) {
	return func(ctx context.Context, l launch.Launch) (launch.Launch, error) {
		return launch.RebindEndpoint(ctx, deps, l)
	}
}

// processStarter is the owner run's starter for a launch whose runner is a
// plain process (operations.RunnerStarter over this run's environment); the
// handle is recorded on the state for teardown.
func (st *runState) processStarter() coord.OwnedRunStarter {
	return operations.RunnerStarter(st.env, st.backendName, st.label, runVerbosity,
		func(h *isolation.RunnerHandle) { st.runnerHandle = h })
}

// startOwnedRun subscribes to the coordinator's event stream, then mints the
// owner-owned run and starts its runner through start (with the per-run
// reach-back trio the coordinator mints). The ownedRunSession carries the
// outcome + event stream the drive consumes; the runner's teardown handle is
// whatever the starter recorded.
func startOwnedRun(ctx context.Context, c *coord.Coordinator, spec ownedRunLaunch, start coord.OwnedRunStarter) (*ownedRunSession, error) {
	if c == nil {
		return nil, fmt.Errorf("this run needs the session coordinator it hosts, which failed to stand up (a runner receives its launch from it)")
	}

	owner, ok := c.Identify(spec.OwnerToken)
	if !ok {
		// The owner Identity is used only for lineage journaling (ParentHarp /
		// Depth); if the owner token can't be resolved, the session harp at
		// depth 0 is the honest fallback.
		owner = coord.Identity{Harp: spec.Launch.Identity.Harp}
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

	// The runner leads the first turn with the package's context ahead of
	// the prompt; the prompt alone rides the launch.
	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnerRun{
		Launch:      spec.Launch,
		MCPServers:  spec.MCPServers,
		OneShot:     spec.Launch.Mode == engine.Structured,
		MayDelegate: spec.MayDelegate,
		Rebind:      spec.Rebind,
	}, start, spec.Launch.Prompt)
	if err != nil {
		cancel()
		return nil, err
	}
	narrow(outcome.RunID)
	stop := make(chan struct{})
	var stopOnce sync.Once
	end := func() { stopOnce.Do(func() { cancel(); close(stop) }) }
	return &ownedRunSession{coord: c, outcome: outcome, events: wireEvents(events, stop), cancel: end}, nil
}

// runOneshotViaCoord drives a --one-shot owner run: it
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
	// The --print arm closes out through recordOneshotAnswer, so "the engine
	// answered nothing" is one rule with one message. Capture runs
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
// the NDJSON entry contract chatEventToJSON defines — run_structured.go),
// signals each turn boundary on turnIdle
// (non-blocking) carrying the answer text accumulated so far, and returns
// that text plus nil at RunCompleted or ctx.Err() on cancellation.
// REASONING/LOG channels are excluded — the host renders the answer, not the
// scratchpad, exactly like accumulateFinalText.
//
// A run that did not succeed returns an *ExitError carrying
// failedRunExitCode.
//
// The accumulator is LOCAL by construction: every read by another
// goroutine goes through turnIdle or the return value, so there is no shared
// buffer to race on. capture is false only where the answer is not wanted:
// the interactive run reads just the terminal outcome (ownedRunOutcome).
func renderOwnedRunEvents(ctx context.Context, out io.Writer, format, runID string, events <-chan *agentcoordpb.AgentEvent, turnIdle chan<- string, capture bool) (string, error) {
	// Reject a format this renderer cannot honor BEFORE consuming the stream,
	// on the text/json pair the streaming commands support (format.go):
	// falling through to raw prose would make an unsupported format a silent
	// downgrade.
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
	// still overflows under sustained overload); nothing upstream re-sends a
	// lost event. SeqWatch accounts for the loss from both signals the wire
	// carries — the hub's EventsLost marker and a hole in the per-run Seq —
	// and each loss is warned about once: the renderer must say so rather
	// than finish as if a possibly truncated answer were complete.
	seqs := &agentcoordpb.SeqWatch{RunID: runID}
	for {
		select {
		case ev := <-events:
			lost, marker := seqs.Observe(ev)
			if lost.Count() > 0 {
				clidiag.Warn("ctxloom", "run %s: lost %d event(s) at seq %s — the coordinator's live event buffer was full (likely other concurrent runs competing for it) and could not queue them; this run's rendered output may be INCOMPLETE", runID, lost.Count(), lost.RangesText())
			}
			if marker || ev.GetRunId() != runID {
				continue
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
				// internal/adapters/operations/sessionfeed.go, which drops its
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
				return answer.String(), &ExitError{Code: failedRunExitCode(r)}
			}
		case <-ctx.Done():
			return answer.String(), ctx.Err()
		}
	}
}

// failedRunExitCode is the exit status of a run that did not succeed: the
// engine's own status when the engine ran and exited non-zero (its code, or
// 128+signum for a signal — exitstatus.Of), so `ctxloom run` is a
// transparent wrapper around it. Otherwise 1: the run failed without an engine
// status to report (cancelled, never launched, synthesized), and a zero one
// cannot stand for a failure. ctxloom's own refusals exit before the engine
// launches and never reach here.
func failedRunExitCode(r *agentcoordpb.Result) int {
	if r != nil && r.ExitCode != nil && *r.ExitCode != 0 {
		return int(*r.ExitCode)
	}
	return 1
}

// wireEvents projects the coordinator's in-process watch onto the wire's
// event shape the owned-run renderer reads: the host's interactive arm still
// renders the proto (the same frames a remote viewer receives over
// ConsumerService.WatchRuns). The hub never closes a subscriber's channel —
// cancel only deregisters it — so the projection ends on stop, which the
// session's cancel closes.
func wireEvents(events <-chan coord.Event, stop <-chan struct{}) <-chan *agentcoordpb.AgentEvent {
	out := make(chan *agentcoordpb.AgentEvent, cap(events))
	go func() {
		defer close(out)
		for {
			select {
			case ev := <-events:
				select {
				case out <- coordgrpc.EventToWire(ev):
				case <-stop:
					return
				}
			case <-stop:
				return
			}
		}
	}()
	return out
}
