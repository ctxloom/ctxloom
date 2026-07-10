package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/agentbus"
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
// --degraded) — the caller then runs on the unwrapped seams.
func setupTerminalUI(ctx context.Context, cfg *config.Config, id terminalUIIdentity, stdin io.Reader, resize <-chan *pb.WindowSize) *termui.Controller {
	prefix, err := termui.ParsePrefixKey(cfg.UIPrefixKey())
	if err != nil {
		// Only reachable in degraded mode (the gate aborts otherwise): run
		// plain rather than guess a key the user didn't configure.
		clidiag.Warn("ctxloom", "terminal viewer disabled: %v", err)
		return nil
	}
	src := terminalUISources(id.WorkDir, id.Harp)
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
		FetchRoster: func() ([]termui.RosterEntry, error) { return surroundRoster(id.Harp) },
		NewOverlay:  func() termui.Overlay { return tui.NewOverlay(ctx, src, prefix) },
		Warn:        func(format string, args ...any) { clidiag.Warn("ctxloom", format, args...) },
	})
}

// terminalUISources wires the overlay's data seams to the session index, the
// per-harp feed resolver, and the harp session dir. The contexts the closures
// receive are the overlay's watch contexts (run-scoped via the factory).
func terminalUISources(workDir, selfHarp string) tui.Sources {
	return tui.Sources{
		Roster: func(ctx context.Context) ([]tui.RosterRow, error) {
			index, err := operations.ListSessionsForProject(workDir)
			if err != nil {
				return nil, err
			}
			// The bus roster is enrichment (children lineage/state) — its
			// absence never blanks the pane.
			bus, _ := sessionBusRoster(selfHarp)
			return tui.BuildRoster(index, bus, selfHarp), nil
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
			return sessionBusInject(selfHarp, harp, text)
		},
	}
}

// sessionBusSocket resolves the viewer-verb socket the coordinator binds under
// THIS session's harp dir (coord.BindSessionSocket → agent-bus.sock). The
// ambient-env candidate died with the executor shim — children carry no
// socket path now. A missing socket is the normal no-coordinator-yet case.
func sessionBusSocket(selfHarp string) (string, error) {
	if selfHarp == "" {
		return "", os.ErrNotExist
	}
	dir, err := paths.HarpDir(selfHarp)
	if err != nil {
		return "", err
	}
	sock := filepath.Join(dir, "agent-bus.sock")
	if _, err := os.Stat(sock); err != nil {
		return "", err
	}
	return sock, nil
}

// sessionBusRoster fetches the roster from this session's orchestrator.
func sessionBusRoster(selfHarp string) ([]agentbus.RosterEntry, error) {
	sock, err := sessionBusSocket(selfHarp)
	if err != nil {
		return nil, err
	}
	return agentbus.FetchRoster(sock)
}

// sessionBusInject delivers user-typed text into harp through this session's
// orchestrator — the viewer's inject seam. Unlike the roster (enrichment), a
// missing orchestrator here is a real failure the viewer must show: there is
// no other channel into a delegated child.
func sessionBusInject(selfHarp, harp, text string) (string, error) {
	sock, err := sessionBusSocket(selfHarp)
	if err != nil {
		return "", fmt.Errorf("no agent orchestrator is reachable for this session: %w", err)
	}
	return agentbus.Inject(sock, harp, text)
}

// surroundRoster adapts the bus roster onto the surround's local mirror type
// (the hot-path package stays dependency-light).
func surroundRoster(selfHarp string) ([]termui.RosterEntry, error) {
	bus, err := sessionBusRoster(selfHarp)
	if err != nil {
		return nil, err
	}
	rows := make([]termui.RosterEntry, len(bus))
	for i, b := range bus {
		rows[i] = termui.RosterEntry{Harp: b.Harp, Agent: b.Agent, State: b.State, LastActivityUnix: b.LastActivityUnix}
	}
	return rows, nil
}
