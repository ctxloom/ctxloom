package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var subagentCmd = &cobra.Command{
	Use:   "subagent",
	Short: "Inspect local subagents (engine↔profile bindings)",
	Long: `Inspect subagents — named, LOCAL-ONLY bindings of an LLM engine to one or
more composed profiles.

A subagent names an 'engine' (the LLM config label/backend, which overrides the
constituent profiles' own llm) and a list of 'profiles' that compose into one
assembled context. Subagents are defined solely in your .ctxloom — under the
'subagents:' key of config.yaml and/or as .ctxloom/subagents/<name>.yaml files.
They are never shipped in bundles or remotes: the engine choice is yours.`,
}

var subagentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all local subagents",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		list := operations.ListSubagents(cfg)
		return emit(cmd, list, func() error {
			return renderSubagentList(cmd.OutOrStdout(), list)
		})
	},
}

// renderSubagentList writes the human-readable summary of the subagent list.
// Extracted from RunE so the formatting is testable without cobra/config.
func renderSubagentList(out io.Writer, list []operations.SubagentEntry) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Println("No subagents defined.")
		w.Println("Define one under 'subagents:' in .ctxloom/config.yaml or as .ctxloom/subagents/<name>.yaml.")
		return w.Err()
	}
	w.Printf("Subagents (%d):\n", len(list))
	for _, s := range list {
		w.Printf("  %s", s.Name)
		if s.Engine != "" {
			w.Printf(" (engine: %s)", s.Engine)
		} else {
			w.Printf(" (engine: project default)")
		}
		w.Println()
		if len(s.Profiles) > 0 {
			w.Printf("    profiles: %s\n", strings.Join(s.Profiles, ", "))
		}
	}
	return w.Err()
}

var subagentShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a subagent and its resolved engine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		def, err := operations.GetSubagent(cfg, name)
		if err != nil {
			return err
		}
		// Resolve the engine/backend (and compose the profiles) so the override
		// behavior is visible. Resolution is fault-tolerant for show: a failure
		// (e.g. a missing constituent profile) still prints the definition with a
		// warning rather than failing the command.
		resolved, rerr := operations.ResolveSubagent(cmd.Context(), cfg, name, "")
		return emit(cmd, subagentShowJSON{Definition: def, Resolved: resolved}, func() error {
			return renderSubagentShow(cmd.OutOrStdout(), def, resolved, rerr)
		})
	},
}

// subagentShowJSON is the --format json shape for `subagent show`: the declared
// definition plus the resolved engine/backend (nil when resolution failed).
type subagentShowJSON struct {
	Definition *operations.SubagentEntry    `json:"definition"`
	Resolved   *operations.ResolvedSubagent `json:"resolved,omitempty"`
}

func renderSubagentShow(out io.Writer, def *operations.SubagentEntry, resolved *operations.ResolvedSubagent, rerr error) error {
	w := iox.NewErrWriter(out)
	w.Printf("Subagent: %s\n", def.Name)
	if def.Source != "" {
		w.Printf("Source: %s\n", def.Source)
	}
	if def.Engine != "" {
		w.Printf("Engine (declared): %s\n", def.Engine)
	} else {
		w.Println("Engine (declared): (project default)")
	}
	writeBulletList(w, "Profiles", def.Profiles)
	if rerr != nil {
		w.Printf("Resolved engine: unavailable (%v)\n", rerr)
		return w.Err()
	}
	w.Printf("Resolved engine: %s", resolved.Label)
	if resolved.Backend != "" {
		w.Printf(" (backend: %s", resolved.Backend)
		if resolved.Model != "" {
			w.Printf(", model: %s", resolved.Model)
		}
		w.Printf(")")
	}
	w.Println()
	w.Printf("Composed fragments: %d\n", len(resolved.Fragments))
	return w.Err()
}

func init() {
	rootCmd.AddCommand(subagentCmd)
	subagentCmd.AddCommand(subagentListCmd)
	subagentCmd.AddCommand(subagentShowCmd)

	subagentShowCmd.ValidArgsFunction = completeSubagentNames
}

// completeSubagentNames completes positional subagent-name args.
func completeSubagentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, s := range operations.ListSubagents(cfg) {
		names = append(names, s.Name)
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}
