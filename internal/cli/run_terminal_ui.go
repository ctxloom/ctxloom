package cli

import (
	"context"
	"io"
	"os"

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
		FetchRoster: func() ([]termui.RosterEntry, error) { return surroundRoster(sessionCoord) },
		NewOverlay:  func() termui.Overlay { return tui.NewOverlay(ctx, src, prefix) },
		Warn:        func(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) },
	})
}

// terminalUISources wires the overlay's data seams to the session index, the
// per-harp feed resolver, and the harp session dir. The contexts the closures
// receive are the overlay's watch contexts (run-scoped via the factory).
// sessionCoord's roster/Inject calls are IN-PROCESS Go method calls (D2) —
// this run process hosts the coordinator itself, so there is no transport to
// dial for its OWN terminal viewer (unlike `ctxloom session watch`, a
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
	}
}

// surroundRoster adapts the coordinator's native roster onto the surround
// bar's local mirror type (termui stays dependency-light — its own
// documented convention). nil sessionCoord (no coordinator hosted) is an
// empty roster, not an error: the bar degrades to showing just this session.
func surroundRoster(sessionCoord *coord.Coordinator) ([]termui.RosterEntry, error) {
	if sessionCoord == nil {
		return nil, nil
	}
	held := sessionCoord.Roster()
	rows := make([]termui.RosterEntry, len(held))
	for i, b := range held {
		rows[i] = termui.RosterEntry{Harp: b.Harp, Agent: b.Agent, State: b.State, LastActivityUnix: b.LastActivityUnix}
	}
	return rows, nil
}
