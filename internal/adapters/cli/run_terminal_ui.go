package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// This file wires the agent-observation terminal layer (internal/adapters/termui +
// internal/adapters/cli/tui — plan §4/§4a, slice S1b) around the interactive run's
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
		strictness.Fail(report.KindConfig,
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
func setupTerminalUI(ctx context.Context, cfg *config.Config, sessionCoord *coord.Coordinator, id terminalUIIdentity, stdin io.Reader, resize <-chan *agent.WindowSize) *termui.Controller {
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
		FetchRoster: func() ([]termui.RosterEntry, error) { return surroundRoster(sessionCoord) },
		NewOverlay:  func() termui.Overlay { return tui.NewOverlay(ctx, src, prefix) },
		Warn:        func(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) },
	})
}

// terminalUISources wires the overlay's data seams to the session index, the
// per-harp feed resolver, and the harp session dir. The contexts the closures
// receive are the overlay's watch contexts (run-scoped via the factory).
// sessionCoord's roster/Control calls are IN-PROCESS Go method calls (D2) —
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
				held = sessionCoord.Roster(sessionCoord.Owner())
			}
			return tui.BuildRoster(index, held, selfHarp), nil
		},
		Watch: func(ctx context.Context, harp string) (*tui.Feed, error) {
			wctx, cancel := context.WithCancel(ctx)
			feed, err := operations.WatchSessionFeed(wctx, App().Engines(), operations.SessionFeedRequest{Harp: harp})
			if err != nil {
				cancel()
				return nil, err
			}
			return &tui.Feed{Source: feed.Source, Events: feed.Events, Errs: feed.Errs, Cancel: cancel}, nil
		},
		Control: func(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error) {
			if sessionCoord == nil {
				return coord.ControlResult{}, coord.ErrNotInjectable
			}
			return sessionCoord.Control(ctx, coord.ControlInitiator{Kind: coord.InitiatorHuman}, req)
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
	held := sessionCoord.Roster(sessionCoord.Owner())
	rows := make([]termui.RosterEntry, len(held))
	for i, b := range held {
		rows[i] = termui.RosterEntry{Harp: b.Harp, State: b.State, LastActivityUnix: b.LastActivityUnix}
	}
	return rows, nil
}

// diagnosticsLogPath is where a TUI-owning session parks its clidiag warnings.
const diagnosticsLogName = "diagnostics.log"

// diagnosticsLogPath is harp's diagnostics log, its session dir created.
func diagnosticsLogPath(harp string) (string, error) {
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return "", fmt.Errorf("could not resolve a session dir for harp %q: %w", harp, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create %s: %w", dir, err)
	}
	return filepath.Join(dir, diagnosticsLogName), nil
}

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
	path, err := diagnosticsLogPath(harp)
	if err != nil {
		return decline("%v", err)
	}
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
