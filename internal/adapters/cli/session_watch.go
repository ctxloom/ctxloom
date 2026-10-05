package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// watchBoundaryRule is the text-mode separator drawn at each response boundary.
const watchBoundaryRule = "──────────────────────────────────────────"

// sessionWatchCmd is ctxloom's per-session observation contract: the stream
// structured frontends (the VSCode companion, the terminal viewer) consume.
//
// The live source taps a delegation child's event stream over its
// orchestrator's agent bus socket. Only children are tappable: an
// orchestrator does not drive its own serving session's engine. Recorded
// entries replay first as scrollback, then live events follow, and the watch
// ends when the child's engine exits. The store source re-reads the backend
// session on a ~250ms poll and diffs it. A session with a hook-bound session
// id is tailed through its backend; one with no bound backend session whose
// transcript lives in its own session dir (a containerized run's native/
// history) is tailed by file location. Delivery to the agent is never delayed
// for a slow watcher, which is what the live source's gap event reports.
var sessionWatchCmd = &cobra.Command{
	Use:   "watch <session-name>",
	Short: "Stream a session's transcript as structured turns (messages, not raw bytes)",
	Long: `Tail a session as structured turns: each new entry as it appears, with a
boundary where a response completes. Entries are complete and untruncated.

--source picks the feed: live (a delegated child's event stream, while its
orchestrator holds it; ends when the child exits), store (the recorded
transcript, polled; runs until Ctrl-C), or auto (live when available, else
store). It errors when the session has no transcript and no live feed.

Text output pretty-prints each turn, draws a rule at each response boundary
and marks a subagent's entries with "↳". With --format json the stream is
NDJSON, one event per line, each carrying exactly one of:

  {"entry": {...}}     a new turn: type (user | assistant | thinking |
                       tool_use | tool_result | system), content, toolName,
                       toolInput (raw JSON arguments, base64), toolOutput,
                       isError, timestampUnix, sidechain (a subagent's entry)
  {"boundary": {...}}  a completed response: entries[fromIndex, toIndex),
                       feed-relative on the live source
  {"heartbeat": {}}    idle keepalive, about every 2s (store only)
  {"gap": {...}}       this viewer fell behind and missed {"dropped": N}
                       events (live only)`,
	Example: `  ctxloom session transcript watch amber-swift-owl`,
	Args:    cobra.ExactArgs(1),
	RunE:    runSessionWatch,
}

func init() {
	sessionWatchCmd.Flags().String("source", "auto", "Feed source: auto (live tap when held, else store tail), live, or store")
}

// runSessionWatch resolves the harp to an observation feed (operations owns
// the two-source resolution) and renders it until it ends or the user
// interrupts.
func runSessionWatch(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	source, err := watchFeedSource(cmd)
	if err != nil {
		return err
	}
	format, err := streamFormat(cmd)
	if err != nil {
		return err
	}

	// Clean Ctrl-C: cancelling the stream context returns the watch.
	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	feed, err := operations.WatchSessionFeed(ctx, App().Engines(), operations.SessionFeedRequest{Harp: harpName, Source: source})
	if err != nil {
		return err
	}
	return streamWatchEvents(cmd.OutOrStdout(), format, feed.Events, feed.Errs)
}

// watchFeedSource resolves --source into a feed source. A lookup failure is an
// error rather than a "" that ParseFeedSource would happily read as auto:
// `--source live` silently served from the store tail is a different feed with
// different semantics (scrollback plus live events vs. a poll-and-diff tail),
// and the whole point of forcing the flag is to be told when the live tap is
// not available instead of being quietly given the other one.
func watchFeedSource(cmd *cobra.Command) (operations.FeedSource, error) {
	sourceFlag, err := cmd.Flags().GetString("source")
	if err != nil {
		return "", fmt.Errorf("read --source: %w", err)
	}
	return operations.ParseFeedSource(sourceFlag)
}

// streamWatchEvents renders an observation feed until it closes. json mode
// emits NDJSON (one event per line — the structured-frontend contract); text
// mode pretty-prints each turn, rules off each response boundary, notes live
// gaps, and stays silent on idle heartbeats. A fatal mid-stream error (from
// errs) is returned after the events channel drains.
func streamWatchEvents(out io.Writer, format clifmt.Format, events <-chan operations.SessionFeedEvent, errs <-chan error) error {
	switch format {
	case formatJSON:
		if err := writeWatchNDJSON(out, events); err != nil {
			return err
		}
	case formatText:
		if err := writeWatchText(out, events); err != nil {
			return err
		}
	default:
		return unknownFormatError(string(format))
	}
	if e := <-errs; e != nil {
		return e
	}
	return nil
}

// writeWatchNDJSON emits each event as one compact JSON line, discriminated
// by its one populated key (watchEventJSON). Gap markers (live source) ride
// the same line vocabulary.
func writeWatchNDJSON(out io.Writer, events <-chan operations.SessionFeedEvent) error {
	enc := json.NewEncoder(out)
	for fe := range events {
		if fe.Event == nil {
			if fe.Gap > 0 {
				if _, err := fmt.Fprintf(out, "{\"gap\":{\"dropped\":%d}}\n", fe.Gap); err != nil {
					return err
				}
			}
			continue
		}
		if err := enc.Encode(watchEventJSON(fe.Event)); err != nil {
			return fmt.Errorf("encode watch event: %w", err)
		}
	}
	return nil
}

// watchEventLine is the NDJSON line of one watch event: exactly one of its
// keys is set. The field names are the stream's documented vocabulary (the
// command's long help); a rename here is a change of contract.
type watchEventLine struct {
	Entry     *watchEntryJSON     `json:"entry,omitempty"`
	Boundary  *watchBoundaryJSON  `json:"boundary,omitempty"`
	Heartbeat *watchHeartbeatJSON `json:"heartbeat,omitempty"`
}

type watchEntryJSON struct {
	Type          string `json:"type"`
	Content       string `json:"content,omitempty"`
	ToolName      string `json:"toolName,omitempty"`
	ToolInput     []byte `json:"toolInput,omitempty"` // the raw JSON arguments, base64-encoded
	ToolOutput    string `json:"toolOutput,omitempty"`
	IsError       bool   `json:"isError,omitempty"`
	TimestampUnix int64  `json:"timestampUnix,omitempty"`
	Sidechain     bool   `json:"sidechain,omitempty"`
}

type watchBoundaryJSON struct {
	FromIndex int `json:"fromIndex,omitempty"`
	ToIndex   int `json:"toIndex,omitempty"`
}

type watchHeartbeatJSON struct{}

// watchEventJSON projects one transcript watch event onto its NDJSON line.
func watchEventJSON(ev *transcript.WatchEvent) watchEventLine {
	switch {
	case ev.Entry != nil:
		e := ev.Entry
		var ts int64
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Unix()
		}
		return watchEventLine{Entry: &watchEntryJSON{
			Type:          string(e.Type),
			Content:       e.Content,
			ToolName:      e.ToolName,
			ToolInput:     []byte(e.ToolInput),
			ToolOutput:    e.ToolOutput,
			IsError:       e.IsError,
			TimestampUnix: ts,
			Sidechain:     e.Sidechain,
		}}
	case ev.Boundary != nil:
		return watchEventLine{Boundary: &watchBoundaryJSON{FromIndex: ev.Boundary.FromIndex, ToIndex: ev.Boundary.ToIndex}}
	default:
		return watchEventLine{Heartbeat: &watchHeartbeatJSON{}}
	}
}

// writeWatchText pretty-prints turns, rules off response boundaries, and
// makes a live gap explicit (a viewer must know it missed events).
func writeWatchText(out io.Writer, events <-chan operations.SessionFeedEvent) error {
	w := errwriter.New(out)
	for fe := range events {
		if fe.Event == nil {
			if fe.Gap > 0 {
				w.Printf("… missed %d live events (viewer lagged; delivery to the agent was not delayed)\n", fe.Gap)
			}
			continue
		}
		switch {
		case fe.Event.Entry != nil:
			renderWatchEntryText(w, fe.Event.Entry)
		case fe.Event.Boundary != nil:
			w.Println(watchBoundaryRule)
		case fe.Event.Heartbeat:
			// Idle keepalive — nothing to show a human.
		}
	}
	// errwriter.Writer's print methods are void-returning by design (so
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
func renderWatchEntryText(w *errwriter.Writer, e *agent.SessionEntry) {
	prefix := ""
	if e.Sidechain {
		prefix = "↳ "
	}
	switch e.Type {
	case agent.EntryTypeToolUse:
		w.Printf("  %s→ %s\n", prefix, e.ToolName)
	case agent.EntryTypeToolResult:
		marker := "✓"
		if e.IsError {
			marker = "✗"
		}
		w.Printf("  %s%s %s\n", prefix, marker, e.ToolName)
	default:
		w.Printf("%s%s: %s\n", prefix, e.Type, e.Content)
	}
}
