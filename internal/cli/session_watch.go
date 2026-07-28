package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/signal"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// watchBoundaryRule is the text-mode separator drawn at each response boundary.
const watchBoundaryRule = "──────────────────────────────────────────"

var sessionWatchCmd = &cobra.Command{
	Use:   "watch <harp-name>",
	Short: "Stream a session's transcript as structured turns (messages, not raw bytes)",
	Long: `Tail a harp's session as a structured turn stream: each new entry arrives
as it appears, with a boundary marking where a response completes. This is
ctxloom's per-session observation contract — the stream structured frontends
(the VSCode companion, the terminal viewer) consume.

One feed, two sources behind it (--source, default auto):

  live   the harp is a delegation child whose orchestrator currently holds
         its event stream — the watch taps it over the orchestrator's agent
         bus socket for zero-lag events. Only such CHILDREN are tappable
         (an orchestrator does not drive its own serving session's engine).
         Recorded transcript entries replay first as scrollback, then live
         events follow; the watch ENDS when the child's engine exits.
  store  the transcript tail: the backend session is re-read on a short poll
         (~250ms) and diffed. Works for any harp with a transcript
         association, live or not, and runs until interrupted.

auto prefers the live tap and falls back to the store tail; forcing --source
live errors when no orchestrator holds the harp.

With --format json the stream is NDJSON: one event per line, in protojson
field names, carrying exactly one of

  {"entry": {...}}     a newly-appended normalized turn — type (user |
                       assistant | thinking | tool_use | tool_result |
                       system), content, toolName, toolInput (the raw JSON
                       arguments, base64-encoded), toolOutput, isError,
                       timestampUnix, and sidechain (true marks an engine
                       subagent's interior entry, not the main thread)
  {"boundary": {...}}  a completed response: entries[fromIndex, toIndex)
                       (live-source indexes are feed-relative)
  {"heartbeat": {}}    idle keepalive, roughly every 2s (store source only)
  {"gap": {...}}       live source only: this viewer fell behind and missed
                       {"dropped": N} events (delivery to the agent is never
                       delayed for a slow watcher)

The stream is lossless while the viewer keeps up: each entry is the backend's
full normalized form — complete tool inputs/outputs and thinking content,
untruncated. Text mode pretty-prints each turn, draws a rule at each response
boundary, prefixes subagent-interior entries with "↳", and stays silent on
heartbeats.

Ctrl-C ends the stream cleanly. A harp with a hook-bound session id is tailed
through the owning backend; a harp with no bound session whose transcript
lives in its own session store (a containerized run's persist/ mount) is
tailed by file location. Errors if the harp has neither and no live tap holds
it.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionWatch,
}

func init() {
	sessionWatchCmd.Flags().String("source", "auto", "Feed source: auto (live tap when held, else store tail), live, or store")
}

// runSessionWatch resolves the harp to an observation feed (operations owns
// the two-source resolution) and renders it until it ends or the user
// interrupts.
func runSessionWatch(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	sourceFlag, _ := cmd.Flags().GetString("source")
	source, err := operations.ParseFeedSource(sourceFlag)
	if err != nil {
		return err
	}

	// Clean Ctrl-C: cancelling the stream context returns the watch (and tears
	// the plugin down on the by-id store path).
	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	feed, err := operations.WatchSessionFeed(ctx, operations.SessionFeedRequest{Harp: harpName, Source: source})
	if err != nil {
		return err
	}
	return streamWatchEvents(cmd.OutOrStdout(), outputFormatOf(cmd), feed.Events, feed.Errs)
}

// streamWatchEvents renders an observation feed until it closes. json mode
// emits NDJSON (one event per line — the structured-frontend contract); text
// mode pretty-prints each turn, rules off each response boundary, notes live
// gaps, and stays silent on idle heartbeats. A fatal mid-stream error (from
// errs) is returned after the events channel drains.
func streamWatchEvents(out io.Writer, format string, events <-chan operations.SessionFeedEvent, errs <-chan error) error {
	switch format {
	case formatJSON:
		if err := writeWatchNDJSON(out, events); err != nil {
			return err
		}
	case "", formatText:
		if err := writeWatchText(out, events); err != nil {
			return err
		}
	default:
		return unknownFormatError(format)
	}
	if e := <-errs; e != nil {
		return e
	}
	return nil
}

// writeWatchNDJSON emits each event as one compact JSON line. protojson encodes
// the oneof and field names; json.Compact normalizes protojson's intentionally
// unstable whitespace so each line is deterministic. Gap markers (live source)
// are hand-rolled onto the same line vocabulary.
func writeWatchNDJSON(out io.Writer, events <-chan operations.SessionFeedEvent) error {
	for fe := range events {
		if fe.Event == nil {
			if fe.Gap > 0 {
				if _, err := fmt.Fprintf(out, "{\"gap\":{\"dropped\":%d}}\n", fe.Gap); err != nil {
					return err
				}
			}
			continue
		}
		raw, err := protojson.Marshal(fe.Event)
		if err != nil {
			return fmt.Errorf("encode watch event: %w", err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return fmt.Errorf("compact watch event: %w", err)
		}
		if _, err := fmt.Fprintf(out, "%s\n", compact.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// writeWatchText pretty-prints turns, rules off response boundaries, and
// makes a live gap explicit (a viewer must know it missed events).
func writeWatchText(out io.Writer, events <-chan operations.SessionFeedEvent) error {
	w := iox.NewErrWriter(out)
	for fe := range events {
		if fe.Event == nil {
			if fe.Gap > 0 {
				w.Printf("… missed %d live events (viewer lagged; delivery to the agent was not delayed)\n", fe.Gap)
			}
			continue
		}
		switch e := fe.Event.GetEvent().(type) {
		case *pb.WatchEvent_Entry:
			renderWatchEntryText(w, e.Entry)
		case *pb.WatchEvent_Boundary:
			w.Println(watchBoundaryRule)
		case *pb.WatchEvent_Heartbeat:
			// Idle keepalive — nothing to show a human.
		}
	}
	// U113-F03: ErrWriter's write methods are void-returning by design (so
	// call sites above can chain freely without per-line error checks), which
	// makes an unchecked Err() invisible to errcheck. Surfacing it here is
	// the one place that must not skip it: a failed write must not silently
	// drain the rest of the event stream to nothing while this command
	// exits 0.
	return w.Err()
}

// renderWatchEntryText writes one normalized entry in a human-readable shape.
// Subagent-interior (sidechain) entries carry a "↳ " prefix so a human can
// tell them from the main thread.
func renderWatchEntryText(w *iox.ErrWriter, e *pb.SessionEntry) {
	prefix := ""
	if e.GetSidechain() {
		prefix = "↳ "
	}
	switch e.GetType() {
	case "tool_use":
		w.Printf("  %s→ %s\n", prefix, e.GetToolName())
	case "tool_result":
		marker := "✓"
		if e.GetIsError() {
			marker = "✗"
		}
		w.Printf("  %s%s %s\n", prefix, marker, e.GetToolName())
	default:
		w.Printf("%s%s: %s\n", prefix, e.GetType(), e.GetContent())
	}
}
