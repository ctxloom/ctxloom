package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// attachReadOnly backs --read-only: watch a run without being able to type
// into it.
var attachReadOnly bool

var attachCmd = &cobra.Command{
	Use:   "attach <harp>",
	Short: "Attach your terminal to a run's live pane",
	Long: `Attach your terminal to a run's live pane.

The pane keeps running when you leave: detaching does not stop the agent.

This runs a tmux client against the run's pane rather than relaying bytes
through the coordinator. For a containerized run the client is started inside
that run's container, alongside the tmux server it needs to reach.

Attaching from inside your own tmux nests one session in another, so the inner
session takes the doubled prefix (Ctrl-b Ctrl-b by default).`,
	Args: cobra.ExactArgs(1),
	RunE: runAttach,
}

func init() {
	attachCmd.Flags().BoolVar(&attachReadOnly, "read-only", false,
		"Watch without being able to type into the pane (tmux attach -r)")
	rootCmd.AddCommand(attachCmd)
}

// attachTarget is everything needed to reach one run's pane. Resolving a harp
// to this is the whole of attach's work; once it exists, the rest is an exec.
type attachTarget struct {
	// Runtime is the container runtime that hosts the run, or "" for a run
	// that has no container and whose tmux server is on this machine.
	Runtime string
	// ContainerName is the run's container, required when Runtime is set.
	ContainerName string
	// Socket is the tmux server socket name (`tmux -L`).
	Socket string
	// Window is the tmux target (`tmux attach -t`), "<session>:<window>".
	Window string
}

// ErrPaneTargetUnknown reports that a run's tmux window cannot be named.
//
// It is typed and distinct from "no such run" because the remedy is entirely
// different: the run may well exist and be perfectly healthy: what is missing
// is a RECORD of which window it was given. See runAttach's refusal.
var ErrPaneTargetUnknown = errors.New("this run's pane window is not recorded")

// attachArgv renders the command that attaches to t.
//
// Host and container differ only in a prefix. That is the point of choosing
// exec over a relay: one mechanism, one code path, and the container case adds
// a hop rather than a protocol.
//
//	host       tmux -L <socket> attach -t <window>
//	container  <runtime> exec -it <container> tmux -L <socket> attach -t <window>
//
// -it is not optional on the container hop: a tmux client without a tty
// attached refuses to start, and WITHOUT -i the operator's keystrokes never
// reach it, which yields a display that renders correctly and ignores input.
//
// -r is tmux's own read-only attach. The flag is enforced by the tmux server
// rather than by us dropping keystrokes locally, which is the stronger of the
// two: there is no client-side filter to bypass or forget.
func attachArgv(t attachTarget, readOnly bool) ([]string, error) {
	if t.Socket == "" {
		return nil, errors.New("attach: no tmux socket name")
	}
	if t.Window == "" {
		return nil, fmt.Errorf("attach: %w", ErrPaneTargetUnknown)
	}
	tmux := []string{"tmux", "-L", t.Socket, "attach", "-t", t.Window}
	if readOnly {
		tmux = append(tmux, "-r")
	}
	if t.Runtime == "" {
		return tmux, nil
	}
	if t.ContainerName == "" {
		return nil, fmt.Errorf("attach: runtime %q names no container to exec into", t.Runtime)
	}
	return append([]string{t.Runtime, "exec", "-it", t.ContainerName}, tmux...), nil
}

// runAttach is DELIBERATELY INCOMPLETE and refuses rather than guessing.
//
// Three of the four things attachTarget needs already resolve from a harp: the
// runtime axis and container name are journaled (RunRecord.Runtime,
// RunRecord.ContainerName via factRunContainer) and the socket name is a
// constant (tmuxhost.TmuxSocketName).
//
// THE WINDOW TARGET DOES NOT EXIST TO BE LOOKED UP. A pane's window is named
// "<session>:<runToken>-h<n>" where runToken is four random bytes minted per
// localTerminals and journaled nowhere. Its randomness is load-bearing, not
// incidental: a deterministic per-harp name would let two ctxloom processes
// mint the same window, and tmux permits duplicate window names, so the second
// SHADOWS the first and leaves kill-window and the wait target ambiguous —
// measured previously as a 30-minute hang. So the fix is not to derive the
// name here; it is to journal the one that was actually minted.
//
// Guessing a window would produce this project's characteristic failure in its
// worst form: `tmux attach -t` against a wrong-but-existing target succeeds and
// shows the operator a DIFFERENT run's pane, with no error anywhere. Refusing
// is the only honest answer available until the fact exists.
func runAttach(cmd *cobra.Command, args []string) error {
	return fmt.Errorf("attach %s: %w: a run's tmux window name is random per process and is not journaled, so it cannot be resolved from a harp; record it when the pane is created (a run.pane fact beside run.container, carrying the socket and the \"<session>:<window>\" target) and resolve it here",
		args[0], ErrPaneTargetUnknown)
}

// attachSocket is the socket every ctxloom pane lives on. It exists so the
// resolution above has one named source when it is completed, rather than the
// constant being re-derived at the call site.
func attachSocket() string { return tmuxhost.TmuxSocketName() }
