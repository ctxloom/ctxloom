package main

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/watch"
)

// watchEvent is one line of the `taskloom watch` JSONL stream.
type watchEvent struct {
	Event   string `json:"event"`   // always "changed"
	Kind    string `json:"kind"`    // "tasks"
	Project string `json:"project"` // resolved project id the change belongs to
}

// watchDebounce coalesces the burst of filesystem events a single append
// produces (write + chmod, lock-file churn) into one logical change.
const watchDebounce = 100 * time.Millisecond

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Stream task-store change events as JSONL (one line per change)",
	// GUI-facing change stream (the VS Code client subscribes); hidden from help.
	Hidden: true,
	Long: `Watch the project's task log and emit a JSONL event whenever it changes.

Long-lived: runs until interrupted (Ctrl-C / SIGTERM). One line per change, e.g.
{"event":"changed","kind":"tasks","project":"swift-amber-falcon"}. A GUI
subscribes to this and re-queries the task list on each event, so it stays live
without polling or watching the store files itself (the watch plumbing lives in
the backend, not the client).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tc, err := taskContext()
		if err != nil {
			return err
		}
		projectID, logPath, err := operations.ResolveLogPath(tc)
		if err != nil {
			return err
		}

		// Watch the tasks directory (not the file directly): the log may not
		// exist yet, and watching the dir catches its creation too. Filter to
		// this project's log so other projects' churn — and the .lock — are
		// ignored.
		w, err := watch.New(filepath.Dir(logPath), false, func(p string) bool {
			return p == logPath
		})
		if err != nil {
			return err
		}
		defer func() { _ = w.Close() }()

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		enc := json.NewEncoder(cmd.OutOrStdout())
		emit := func() error {
			return enc.Encode(watchEvent{Event: "changed", Kind: "tasks", Project: projectID})
		}

		// Emit once up front so a subscriber renders current state immediately,
		// with no separate initial-query race.
		if err := emit(); err != nil {
			return err
		}

		var timer *time.Timer
		var timerC <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return nil
			case _, ok := <-w.Events():
				if !ok {
					return nil
				}
				if timer == nil {
					timer = time.NewTimer(watchDebounce)
					timerC = timer.C
				} else {
					timer.Reset(watchDebounce)
				}
			case <-timerC:
				if err := emit(); err != nil {
					return err
				}
			case err := <-w.Errors():
				if err != nil {
					return err
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(watchCmd)
}
