package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/cli/tui"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/termui"
)

// This file wires the agent-observation terminal layer (internal/termui +
// internal/cli/tui — plan §4/§4a, slice S1b) around the interactive run's
// existing seams: the raw stdin reader, the stdout writer, and the resize
// channel handed to client.Run. It only ever wraps those seams; a failure
// here degrades to a plain terminal and never blocks the launch.

// terminalUIIdentity is what the surround bar names: this session and its
// transport.
type terminalUIIdentity struct {
	WorkDir string
	Harp    string
	Agent   string // the bound agent name ("" for a classic profile run)
	Backend string
	Model   string
}

// validateTerminalUIConfig records a fatal-by-default finding for an invalid
// ui.prefix_key — a broken config fails loudly at the startup gate rather
// than silently launching with a viewer on the wrong key (or none).
func validateTerminalUIConfig(cfg *config.Config) {
	if _, err := termui.ParsePrefixKey(cfg.UIPrefixKey()); err != nil {
		strictness.Fail(strictness.ClassConfig,
			`set ui.prefix_key to a control key (e.g. "ctrl-]") in config.yaml, or pass --degraded to launch anyway`,
			"invalid ui.prefix_key: %v", err)
	}
}

// setupTerminalUI builds the observation layer for an interactive tty run:
// prefix interceptor, surround bar, and the bubbletea overlay factory. It
// returns nil when the layer can't be built (e.g. a bad prefix key under
// --degraded) — the caller then runs on the unwrapped seams. sessionCoord is
// THIS run's own hosted coordinator (nil when coordinator startup failed or
// was skipped, e.g. no activeHarp) — D2: the terminal viewer reaches
// roster/inject IN-PROCESS now (this run process IS the coordinator), never
// over a socket.
func setupTerminalUI(ctx context.Context, cfg *config.Config, sessionCoord *coord.Coordinator, id terminalUIIdentity, stdin io.Reader, resize <-chan *pb.WindowSize) *termui.Controller {
	prefix, err := termui.ParsePrefixKey(cfg.UIPrefixKey())
	if err != nil {
		// Only reachable in degraded mode (the gate aborts otherwise): run
		// plain rather than guess a key the user didn't configure.
		clidiag.Warn("ctxloom", "terminal viewer disabled: %v", err)
		return nil
	}
	src := terminalUISources(sessionCoord, id.WorkDir, id.Harp)
	return termui.New(termui.Options{
		Stdin:    stdin,
		TTY:      os.Stdout,
		Resize:   resize,
		Prefix:   prefix,
		Surround: cfg.UISurroundEnabled(),
		Bar: termui.BarInfo{
			Harp:       id.Harp,
			Agent:      id.Agent,
			Engine:     id.Backend,
			Model:      id.Model,
			PrefixHint: termui.CaretHint(prefix),
		},
		FetchRoster:    func() ([]termui.RosterEntry, error) { return surroundRoster(sessionCoord) },
		FetchApprovals: surroundFetchApprovals(sessionCoord),
		NewOverlay:     func() termui.Overlay { return tui.NewOverlay(ctx, src, prefix) },
		Warn:           func(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) },
	})
}

// terminalUISources wires the overlay's data seams to the session index, the
// per-harp feed resolver, and the harp session dir. The contexts the closures
// receive are the overlay's watch contexts (run-scoped via the factory).
// sessionCoord's roster/Inject calls are IN-PROCESS Go method calls (D2) —
// this run process hosts the coordinator itself, so there is no transport to
// dial for its OWN terminal viewer (unlike `ctxloom session transcript watch`, a
// separate process, which reaches a coordinator over ConsumerService —
// operations.WatchSessionFeed).
func terminalUISources(sessionCoord *coord.Coordinator, workDir, selfHarp string) tui.Sources {
	return tui.Sources{
		Roster: func(ctx context.Context) ([]tui.RosterRow, error) {
			index, err := operations.ListSessionsForProject(workDir)
			if err != nil {
				return nil, err
			}
			// The coordinator roster is enrichment (children lineage/state)
			// — its absence (no coordinator hosted) never blanks the pane.
			var held []coord.RosterEntry
			if sessionCoord != nil {
				held = sessionCoord.Roster()
			}
			return tui.BuildRoster(index, held, selfHarp), nil
		},
		Watch: func(ctx context.Context, harp string) (*tui.Feed, error) {
			wctx, cancel := context.WithCancel(ctx)
			feed, err := operations.WatchSessionFeed(wctx, operations.SessionFeedRequest{Harp: harp})
			if err != nil {
				cancel()
				return nil, err
			}
			return &tui.Feed{Source: feed.Source, Events: feed.Events, Errs: feed.Errs, Cancel: cancel}, nil
		},
		ExportDir: func(harp string) (string, error) {
			dir, err := paths.HarpDir(harp)
			if err != nil {
				return "", err
			}
			return dir, os.MkdirAll(dir, 0o755)
		},
		Inject: func(harp, text string) (string, error) {
			if sessionCoord == nil {
				return "", coord.ErrNotInjectable
			}
			return sessionCoord.Inject(harp, text)
		},
		PendingApprovals: func() []coord.PendingApproval {
			if sessionCoord == nil {
				return nil
			}
			return sessionCoord.PendingApprovals()
		},
		// AnswerApproval resolves one parked approval over the SAME
		// AgentSend(in_reply_to) path a relayed approval's parent answers
		// over (surfaceApprovalToHuman's doc comment): structured carries the
		// ApprovalDecision projection decisionFromStructured decodes — the
		// exact shape TestApproval_SurfaceToHumanRoundTrip and
		// TestApproval_SurfaceToHumanClosureShapeMirrorsCLIWiring pin
		// (internal/agentcoord/coord/surface_to_human_test.go). The park's
		// own targetHarp check (pa.targetHarp == caller.Harp) is what
		// authorizes the answer, and it targets the ROOT SESSION of the
		// delegation tree (coord.Coordinator.rootSessionHarp) — selfHarp, at
		// every depth. It used to target the asking run's immediate parent,
		// which made a grandchild's approval unanswerable from here; this
		// wiring did not change when that was fixed, because selfHarp was
		// always the identity a human answers as.
		AnswerApproval: func(messageID, childHarp string, d agentcoordpb.ApprovalDecision_Decision, note string) error {
			if sessionCoord == nil {
				// Unreachable via the TUI in practice — PendingApprovals above
				// already returns empty for a nil coordinator, and the model
				// refuses to open the approvals view on an empty list — but
				// guarded directly rather than trusting that path, exactly like
				// Inject's own nil-coordinator guard above.
				return fmt.Errorf("approvals: no coordinator hosted for this session")
			}
			structured, err := json.Marshal(map[string]any{
				"decision": d.String(), "note": note,
			})
			if err != nil {
				return err
			}
			_, err = sessionCoord.AgentSend(
				coord.Identity{Harp: selfHarp, Depth: 0, Project: workDir},
				childHarp, "", "approval decision", structured, messageID)
			return err
		},
	}
}

// surroundRoster adapts the coordinator's native roster onto the surround
// bar's local mirror type (termui stays dependency-light — its own
// documented convention). nil sessionCoord (no coordinator hosted) is an
// empty roster, not an error: the bar degrades to showing just this session.
// The error return is structurally always nil — it exists only to satisfy
// termui.Options.FetchRoster; there is no failure case to hunt for.
func surroundRoster(sessionCoord *coord.Coordinator) ([]termui.RosterEntry, error) {
	if sessionCoord == nil {
		return nil, nil
	}
	held := sessionCoord.Roster()
	rows := make([]termui.RosterEntry, len(held))
	for i, b := range held {
		rows[i] = termui.RosterEntry{Harp: b.Harp, State: b.State, LastActivityUnix: b.LastActivityUnix}
	}
	return rows, nil
}

// surroundFetchApprovals adapts the coordinator's PendingApprovals onto the
// surround bar's count-only seam (termui.Options.FetchApprovals stays
// coord-free per termui's own dependency-lightness rule — only the int
// crosses the boundary). Unlike surroundRoster (which always returns a
// closure so the bar can show an empty roster), a nil sessionCoord here
// returns a nil func: FetchApprovals documents nil as "no indicator", and
// there is no coordinator to ever have anything parked.
func surroundFetchApprovals(sessionCoord *coord.Coordinator) func() int {
	if sessionCoord == nil {
		return nil
	}
	return func() int { return len(sessionCoord.PendingApprovals()) }
}

// diagnosticsLogPath is where a TUI-owning session parks its clidiag warnings.
const diagnosticsLogName = "diagnostics.log"

// redirectDiagnosticsForTUI routes clidiag's stderr warnings into a per-session
// log for the lifetime of a terminal-UI session, and returns the restore.
//
// Under `ctxloom run` stderr IS the terminal the harness paints its TUI on, so
// an unconditional warning corrupts the display mid-frame — "run channel down
// (reconnecting)" and "runner dial-home failed" were landing straight on it
// (large-album). Warnings are DIVERTED, never dropped: silently discarding
// them would trade a visible corruption for an invisible loss, which is the
// worse bug in this codebase's own terms. The path is printed once, before the
// TUI takes the terminal, so the user knows where to look.
//
// A log file that cannot be opened leaves the sink alone: corrupting the TUI
// beats losing the diagnostics. That outcome is ANNOUNCED rather than returned
// as a bare no-op — warnings continuing to land on the terminal is exactly the
// visible symptom this exists to prevent, and a user watching their display
// break needs the reason, not a mystery. The announcement rides the same
// writer the success line does: it happens before the handover, and routing it
// through clidiag would push it into the sink this function just failed to
// redirect.
func redirectDiagnosticsForTUI(harp string, announce io.Writer) func() {
	noop := func() {}
	decline := func(format string, args ...any) func() {
		fmt.Fprintf(announce, "ctxloom: diagnostics stay on stderr and may disturb this session's display — "+format+"\n", args...)
		return noop
	}
	if harp == "" {
		return decline("this session has no harp, so there is no per-session log to divert them to")
	}
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return decline("could not resolve a session dir for harp %q: %v", harp, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return decline("could not create %s: %v", dir, err)
	}
	path := filepath.Join(dir, diagnosticsLogName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return decline("could not open %s: %v", path, err)
	}
	fmt.Fprintf(announce, "ctxloom: diagnostics for this session go to %s\n", path)
	restore := clidiag.SetSink(f)
	return func() {
		restore()
		_ = f.Close()
	}
}
