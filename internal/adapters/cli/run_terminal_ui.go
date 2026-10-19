package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
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
	if _, err := tui.ParsePrefixKey(cfg.UIPrefixKey()); err != nil {
		strictness.Fail(report.KindConfig,
			`set ui.prefix_key to a control key (e.g. "ctrl-]") in config.yaml, or pass --degraded to launch anyway`,
			"invalid ui.prefix_key: %v", err)
	}
}

// setupTerminalUI builds the observation layer for an interactive tty run:
// prefix interceptor, surround bar, and the bubbletea overlay factory. It
// returns nil when the layer can't be built (e.g. a bad prefix key under
// --degraded) — the caller then runs on the unwrapped seams. port is THIS
// session's frontend port (operations.NewFrontendPort): everything the bar,
// the overlay and the presenters show or do rides it, and none of them holds
// the coordinator.
func setupTerminalUI(ctx context.Context, cfg *config.Config, port frontend.Port, id terminalUIIdentity, stdin io.Reader, resize <-chan *agent.WindowSize) *termui.Controller {
	prefix, err := termui.ParsePrefixKey(cfg.UIPrefixKey())
	if err != nil {
		// Only reachable in degraded mode (the gate aborts otherwise): run
		// plain rather than guess a key the user didn't configure.
		clidiag.Warn("ctxloom", "terminal viewer disabled: %v", err)
		return nil
	}
	src := tui.Sources{Port: port, Self: id.Harp}
	ui := termui.New(termui.Options{
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
		FetchRoster: func() ([]termui.RosterEntry, error) { return surroundRoster(ctx, port) },
		NewOverlay:  func(start termui.OverlayStart) termui.Overlay { return tui.NewOverlay(ctx, src, prefix, start) },
		Warn:        func(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) },
	})
	// The modal is how a pending approval reaches the human: the root's queue
	// is presented here, on the originator's terminal, and nowhere else.
	if approvals := port.Approvals(); approvals != nil {
		go func() { _ = modalPresenter{ui: ui, clock: tui.RealClock{}}.Present(ctx, approvals) }()
	}
	// A rate limit or a refused credential parks the runs that share it; the
	// bar keeps the root human told until the hold releases them — or, with
	// no bar, a line on the terminal as each hold opens and releases. A
	// session with no coordinator lists no holds, so says nothing.
	p := &credentialPresenter{
		holds: func() []coord.CredentialHold {
			holds, _ := port.CredentialHolds(ctx)
			return holds
		},
		noteBar: ui.NoteBar, ring: ui.Ring, session: id.Harp, clock: tui.RealClock{},
	}
	if !cfg.UISurroundEnabled() {
		p.announce = ui.Announce
	}
	go presentCredentialHolds(ctx, p)
	return ui
}

// surroundRoster is the surround bar's roster over the port: the
// coordinator's runs, ended ones included, on the bar's local mirror type
// (termui stays dependency-light — its own documented convention). A session
// with no coordinator lists none, and the bar shows just this session.
func surroundRoster(ctx context.Context, port frontend.Port) ([]termui.RosterEntry, error) {
	runs, err := port.ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	return surroundRows(runs.Runs), nil
}

// surroundRows maps the coordinator's runs onto the bar's mirror rows, a
// hold worded as the overlay words it (tui.HoldLabel).
func surroundRows(runs []coord.RunInfo) []termui.RosterEntry {
	rows := make([]termui.RosterEntry, 0, len(runs))
	for _, r := range runs {
		if r.Agent == nil {
			continue
		}
		row := termui.RosterEntry{Harp: r.Agent.AgentID, State: r.Phase, Hold: tui.HoldLabel(r.Hold), Unread: r.Unread}
		if !r.LastEventAt.IsZero() {
			row.LastActivityUnix = r.LastEventAt.Unix()
		}
		rows = append(rows, row)
	}
	return rows
}

// diagnosticsLogPath is harp's diagnostics log (the session's
// diagnostics.log member), its session dir created.
func diagnosticsLogPath(harp string) (string, error) {
	p, err := paths.HarpDiagnosticsLogPath(harp)
	if err != nil {
		return "", fmt.Errorf("could not resolve a session dir for session %q: %w", harp, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("could not create %s: %w", filepath.Dir(p), err)
	}
	return p, nil
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
		return decline("this session has no session name, so there is no per-session log to divert them to")
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
