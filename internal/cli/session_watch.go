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
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// watchBoundaryRule is the text-mode separator drawn at each response boundary.
const watchBoundaryRule = "──────────────────────────────────────────"

var sessionWatchCmd = &cobra.Command{
	Use:   "watch <harp-name>",
	Short: "Stream a session's transcript as structured turns (messages, not raw bytes)",
	// GUI-facing structured stream (the chat frontend renders it); hidden from help.
	Hidden: true,
	Long: `Tail a harp's bound backend session as a structured turn stream: each new
transcript entry arrives as it appears, with a boundary marking where a
response completes. This is the read side of the structured chat interface a
frontend renders.

With --format json the stream is emitted as NDJSON — one WatchEvent per line —
which is the contract structured frontends (e.g. the VSCode companion) consume.
Text mode pretty-prints each turn and draws a rule at each response boundary.

Ctrl-C ends the stream cleanly. Errors if the harp has no session_id bound
(the SessionStart bind hook records it for sessions launched via ctxloom run).`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionWatch,
}

// runSessionWatch resolves the harp's backend + bound session_id (mirroring
// session distill), opens a WatchSession stream, and renders it until the stream
// ends or the user interrupts.
func runSessionWatch(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	entry, err := operations.GetSession(harpName)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harpName)
	}
	sessionID := entry.SessionID
	if sessionID == "" {
		return fmt.Errorf("harp %q has no session_id bound; nothing to watch (the SessionStart bind hook records the ID for sessions launched via ctxloom run)", harpName)
	}

	backendName := entry.Backend
	if backendName == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		backendName = cfg.GetDefaultLLM()
	}

	// Clean Ctrl-C: cancelling the stream context returns WatchSession and tears
	// the plugin down.
	ctx, stop := signal.NotifyContext(cmd.Context(), shutdownSignals...)
	defer stop()

	events, errs, err := pb.NewSessionReader(backendName, 0).WatchSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("watch %s: %w", harpName, err)
	}
	return streamWatchEvents(cmd.OutOrStdout(), outputFormatOf(cmd), events, errs)
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
func renderWatchEntryText(w *iox.ErrWriter, e *pb.SessionEntry) {
	switch e.GetType() {
	case "tool_use":
		w.Printf("  → %s\n", e.GetToolName())
	case "tool_result":
		marker := "✓"
		if e.GetIsError() {
			marker = "✗"
		}
		w.Printf("  %s %s\n", marker, e.GetToolName())
	default:
		w.Printf("%s: %s\n", e.GetType(), e.GetContent())
	}
}
