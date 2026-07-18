package main

import (
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/spf13/cobra"
)

// taskloom owns plans alongside tasks: `plan list` enumerates the session plan
// documents under ~/.ctxloom/sessions/<harp>/*.plan.md (via the shared plans
// package, so the location/frontmatter logic isn't duplicated), and `plan show`
// prints one plan's content. The Plan view in the ctxloom VS Code extension
// reads these.

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Browse session plans (~/.ctxloom/sessions/<harp>/*.plan.md)",
}

var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "List session plans",
	RunE: func(cmd *cobra.Command, args []string) error {
		list, err := plans.ListHome()
		if err != nil {
			return err
		}
		return cliemit.Emit(cmd, list, func() error {
			return renderPlanTable(cmd.OutOrStdout(), list)
		})
	},
}

var planShowCmd = &cobra.Command{
	Use:   "show <path>",
	Short: "Print a plan's content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		content, err := plans.Show(args[0])
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write([]byte(content))
		return err
	},
}

// renderPlanTable writes the human-readable plan listing: one row per plan as
// "<session>\t<name>\t<title>", grouped naturally by the sorted session order.
func renderPlanTable(out io.Writer, list []plans.Plan) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Printf("(no plans)\n")
		return w.Err()
	}
	for _, p := range list {
		w.Printf("%s\t%s\t%s\n", p.Session, p.Name, p.Title)
	}
	return w.Err()
}

func init() {
	planListCmd.Flags().Bool("json", false, "shorthand for --format json")
	planCmd.AddCommand(planListCmd, planShowCmd)
	rootCmd.AddCommand(planCmd)
}
