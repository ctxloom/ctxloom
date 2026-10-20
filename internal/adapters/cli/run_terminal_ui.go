package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// This file wires the split view (internal/adapters/cli/tui) onto an
// interactive tty run: it draws the human's terminal, takes the human's
// input, and holds the engine's terminal in its emulated pane. A failure to
// build it degrades to a plain terminal and never blocks the launch.

// terminalUIIdentity is what the panel's header names: this session and its
// transport.
type terminalUIIdentity struct {
	WorkDir string
	Harp    string
	Agent   string // the bound agent name ("" for a classic profile run)
	Backend string
	Model   string
}

// validateTerminalUIConfig records a fatal-by-default finding for an invalid
// ui.prefix_key or ui.list_window — a broken config fails loudly at the
// startup gate rather than silently launching with a viewer on the wrong key
// (or none), or a list cut to a window the user didn't configure.
func validateTerminalUIConfig(cfg *config.Config) {
	if _, err := tui.ParsePrefixKey(cfg.UIPrefixKey()); err != nil {
		strictness.Fail(report.KindConfig,
			`set ui.prefix_key to a control key (e.g. "ctrl-]") in config.yaml, or pass --degraded to launch anyway`,
			"invalid ui.prefix_key: %v", err)
	}
	if _, err := operations.ParseListWindow(cfg.UIListWindow()); err != nil {
		strictness.Fail(report.KindConfig,
			`set ui.list_window to a duration (e.g. "7d") or "all" in config.yaml, or pass --degraded to launch anyway`,
			"invalid ui.list_window: %v", err)
	}
}

// approvalSurface is what the approval presenter drives (tui.SplitView).
type approvalSurface interface {
	Summon(ctx context.Context, view, notice string) error
	SetApprovals(n int, oldest time.Time, arrived bool)
	Note(text string, d time.Duration)
}

var _ approvalSurface = (*tui.SplitView)(nil)

// SplitTranscriptLine is the line the split view leaves under the engine's
// final frame as it exits: the main screen keeps only that last screen, and
// the session's transcript keeps the rest.
const SplitTranscriptLine = "full session transcript: %s"

// splitEpilogue is SplitTranscriptLine for harp's canonical transcript; ""
// when no session dir can be named for it.
func splitEpilogue(harp string) string {
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(SplitTranscriptLine, p)
}

// splitIO is the human's terminal the split view runs on.
type splitIO struct {
	in  io.Reader
	out io.Writer
}

// setupSplitView starts the split view for an interactive tty run over the
// session's terminal term, and the session's presenters on it. It returns
// nil when the view can't be built (a bad prefix key under --degraded) — the
// caller then runs on the plain seams. port is THIS session's frontend port
// (operations.NewFrontendPort): everything the view and the presenters show
// or do rides it, and none of them holds the coordinator.
func setupSplitView(ctx context.Context, cfg *config.Config, port frontend.Port, id terminalUIIdentity, term frontend.Terminal, human splitIO) *tui.SplitView {
	prefix, err := tui.ParsePrefixKey(cfg.UIPrefixKey())
	if err != nil {
		// Only reachable in degraded mode (the gate aborts otherwise): run
		// plain rather than guess a key the user didn't configure.
		clidiag.Warn("ctxloom", splitViewDisabledWarning, err)
		return nil
	}
	// A window refused at the gate reaches here only under --degraded: it
	// is then no window, so nothing is hidden on a guess.
	window, _ := operations.ParseListWindow(cfg.UIListWindow())
	view, err := tui.StartSplit(ctx, tui.SplitOptions{
		Port:       port,
		Self:       id.Harp,
		Term:       term,
		Prefix:     prefix,
		Header:     tui.Header{Harp: id.Harp, Agent: id.Agent, Engine: id.Backend, Model: id.Model},
		Notify:     desktopAlert{host: platform.Current(), resolve: shellenv.Resolve},
		In:         human.in,
		Out:        human.out,
		Epilogue:   splitEpilogue(id.Harp),
		ListWindow: window,
	})
	if err != nil {
		clidiag.Warn("ctxloom", splitViewDisabledWarning, err)
		return nil
	}
	// The split view is how a pending approval reaches the human: the root's
	// queue is presented here, on the originator's terminal, and nowhere else.
	if approvals := port.Approvals(); approvals != nil {
		go func() { _ = modalPresenter{ui: view, clock: tui.RealClock{}}.Present(ctx, approvals) }()
	}
	// A rate limit or a refused credential parks the runs that share it; the
	// panel keeps the root human told until the hold releases them. A
	// session with no coordinator lists no holds, so says nothing.
	go presentCredentialHolds(ctx, &credentialPresenter{
		holds: func() []coord.CredentialHold {
			holds, _ := port.CredentialHolds(ctx)
			return holds
		},
		noteBar: view.Note, ring: view.Ring, session: id.Harp, clock: tui.RealClock{},
	})
	return view
}

// desktopAlertWait bounds one desktop notification: a notifier that hangs
// (no session bus to answer it) is abandoned, never waited on.
const desktopAlertWait = 5 * time.Second

// desktopAlert raises the split view's desktop notifications through the
// platform's notifier, its command found as the engines' are.
type desktopAlert struct {
	host    platform.DesktopNotifier
	resolve func(string) (string, error)
}

// Notify shows title and body, best effort: a notification that cannot be
// shown leaves the bell the split view rang beside it.
func (a desktopAlert) Notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), desktopAlertWait)
	defer cancel()
	_ = a.host.Notify(ctx, a.resolve, title, body)
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

// diagnosticsDivertedNotice tells the human, before the split view takes the
// terminal, where its diagnostics go instead.
const diagnosticsDivertedNotice = "ctxloom: diagnostics for this session go to %s\n"

// splitViewDisabledWarning says the split view could not be started and why.
const splitViewDisabledWarning = "terminal viewer disabled: %v"

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
	fmt.Fprintf(announce, diagnosticsDivertedNotice, path)
	restore := clidiag.SetSink(f)
	return func() {
		restore()
		_ = f.Close()
	}
}
