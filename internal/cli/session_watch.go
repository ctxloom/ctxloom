package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/signal"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// watchBoundaryRule is the text-mode separator drawn at each response boundary.
const watchBoundaryRule = "──────────────────────────────────────────"

var sessionWatchCmd = &cobra.Command{
	Use:   "watch <harp-name>",
	Short: "Stream a session's transcript as structured turns (messages, not raw bytes)",
	Long: `Tail a harp's backend session as a structured turn stream: each new
transcript entry arrives as it appears (within ~250ms), with a boundary
marking where a response completes. This is ctxloom's per-session observation
contract — the stream structured frontends (the VSCode companion, the
terminal viewer) consume.

With --format json the stream is NDJSON: one WatchEvent per line, in protojson
field names, carrying exactly one of

  {"entry": {...}}     a newly-appended normalized turn — type (user |
                       assistant | thinking | tool_use | tool_result |
                       system), content, toolName, toolInput (the raw JSON
                       arguments, base64-encoded), toolOutput, isError,
                       timestampUnix, and sidechain (true marks an engine
                       subagent's interior entry, not the main thread)
  {"boundary": {...}}  a completed response: entries[fromIndex, toIndex)
  {"heartbeat": {}}    idle keepalive, roughly every 2s while nothing is new

The stream is lossless: each entry is the backend's full normalized form —
complete tool inputs/outputs and thinking content, untruncated. Text mode
pretty-prints each turn, draws a rule at each response boundary, prefixes
subagent-interior entries with "↳", and stays silent on heartbeats.

The watch runs until interrupted; Ctrl-C ends the stream cleanly. A harp
whose bind hook recorded a session id is tailed through the owning backend;
a harp with no bound session whose transcript lives in its own session store
(a containerized run's persist/ mount) is tailed by file location. Errors if
the harp has neither yet.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionWatch,
}

// runSessionWatch resolves the harp to a watchable transcript and renders the
// stream until it ends or the user interrupts. Two locators, one contract: a
// hook-bound session id is tailed through the owning backend's agent server
// (WatchSession); an entry bound only by location — a transcript discovered in
// the harp's own persist/ store, where the bind hook never fired — is tailed
// host-side by path (WatchHistoryByPath), since the agent server's
// self-situated store lookup cannot see a file that lives in ctxloom's
// session dir.
func runSessionWatch(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	entry, err := operations.GetSession(harpName)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harpName)
	}

	backendName := entry.Backend
	if backendName == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		backendName = cfg.GetDefaultLLM()
	}

	// Clean Ctrl-C: cancelling the stream context returns the watch (and tears
	// the plugin down on the by-id path).
	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	var events <-chan *pb.WatchEvent
	var errs <-chan error
	switch {
	case entry.SessionID != "":
		events, errs, err = pb.NewSessionReader(backendName, 0).WatchSession(ctx, entry.SessionID)
		if err != nil {
			return fmt.Errorf("watch %s: %w", harpName, err)
		}
	case entry.TranscriptPath != "":
		hist, err := historyForBackend(backendName)
		if err != nil {
			return fmt.Errorf("watch %s: %w", harpName, err)
		}
		events, errs = pb.WatchHistoryByPath(ctx, hist, entry.TranscriptPath, 0)
	default:
		return fmt.Errorf("harp %q has no session bound and no transcript in its session store; nothing to watch yet (the SessionStart bind hook records the id for sessions launched via ctxloom run; containerized runs surface their transcript once the engine writes it)", harpName)
	}
	return streamWatchEvents(cmd.OutOrStdout(), outputFormatOf(cmd), events, errs)
}

// historyForBackend returns the named backend's in-process transcript reader,
// used only for host-located (by-location) transcripts.
func historyForBackend(name string) (agent.SessionHistory, error) {
	b := backends.Get(name)
	if b == nil {
		return nil, fmt.Errorf("unknown backend %q", name)
	}
	h := b.History()
	if h == nil {
		return nil, fmt.Errorf("backend %q has no session history", name)
	}
	return h, nil
}

// streamWatchEvents renders a WatchSession stream until it closes. json mode
// emits NDJSON (one WatchEvent per line — the structured-frontend contract);
// text mode pretty-prints each turn and rules off each response boundary,
// staying silent on idle heartbeats. A fatal mid-stream error (from errs) is
// returned after the events channel drains.
func streamWatchEvents(out io.Writer, format string, events <-chan *pb.WatchEvent, errs <-chan error) error {
	switch format {
	case formatJSON:
		if err := writeWatchNDJSON(out, events); err != nil {
			return err
		}
	case "", formatText:
		writeWatchText(out, events)
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
// unstable whitespace so each line is deterministic.
func writeWatchNDJSON(out io.Writer, events <-chan *pb.WatchEvent) error {
	for ev := range events {
		raw, err := protojson.Marshal(ev)
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

// writeWatchText pretty-prints turns and rules off response boundaries.
func writeWatchText(out io.Writer, events <-chan *pb.WatchEvent) {
	w := iox.NewErrWriter(out)
	for ev := range events {
		switch e := ev.GetEvent().(type) {
		case *pb.WatchEvent_Entry:
			renderWatchEntryText(w, e.Entry)
		case *pb.WatchEvent_Boundary:
			w.Println(watchBoundaryRule)
		case *pb.WatchEvent_Heartbeat:
			// Idle keepalive — nothing to show a human.
		}
	}
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
