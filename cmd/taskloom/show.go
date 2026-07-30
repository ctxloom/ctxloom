package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

var showCmd = &cobra.Command{
	Use:   "show <harp-id>",
	Short: "Show one task's full detail",
	Long: `Show a single task in full — its status, tags, trigger, and complete
(never-truncated) text. This is the full-text companion to ` + "`taskloom list`" + `,
which prints one-line summaries: copy a harp id from the list and pass it here
to read the whole task. --format json/yaml/toml/markdown emit the structured
task for scripting.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Search every status: a harp id shown by `list --all` (Done/Archived
		// included) must still resolve here.
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.ListTasks(tc, nil, "", true, false, 0)
		if err != nil {
			return err
		}
		task, ok := findTask(res.Tasks, args[0])
		if !ok {
			return fmt.Errorf("no task with harp id %q (see `taskloom list`)", args[0])
		}
		noteTaskProject(res.ProjectDir, res.ProjectID)
		cfg := hideConfigFor(tc)
		return cliemit.Emit(cmd, task, func() error {
			return renderTaskDetail(cmd.OutOrStdout(), task, cfg)
		})
	},
}

func init() {
	rootCmd.AddCommand(showCmd)
}

// renderTaskDetail prints one task's full human view: a header line (harp id +
// status), its tags and trigger if present, then the complete text — the
// detail `taskloom list` deliberately summarizes into a single line. cfg is
// applied to t's tags via visibleTags before printing — see hideConfigFor.
func renderTaskDetail(out io.Writer, t tasks.Task, cfg tagma.HideConfig) error {
	w := iox.NewErrWriter(out)
	check := " "
	if t.Checked {
		check = "x"
	}
	w.Printf("[%s] %s  %s\n", check, t.HarpID, t.Status)
	if visible := visibleTags(t.Tags, cfg); len(visible) > 0 {
		w.Printf("    tags: %s\n", strings.Join(visible, ", "))
	}
	if t.Trigger != "" {
		w.Printf("    trigger: %s\n", t.Trigger)
	}
	w.Println("")
	w.Println(t.Text)
	return w.Err()
}
