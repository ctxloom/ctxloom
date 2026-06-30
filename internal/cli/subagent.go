package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/resources"
)

// subagentSetupPrompt is the agent-assisted setup prompt `subagent setup` emits
// for the LLM to follow: SCAN available engines/profiles → DISCUSS roles with the
// user → SET the chosen engine↔profile bindings. It is deliberately a markdown
// RESOURCE, not Go: the role palette, steering, and example names are data the
// taxonomy can evolve without a code change (see resources/prompts/subagent-setup.md).
var subagentSetupPrompt = resources.MustGetPromptText("subagent-setup")

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

// subagentSetupCmd surfaces the agent-assisted setup prompt for the LLM to run.
// It writes the prompt to stdout; the agent (which has shell access) runs
// `ctxloom subagent setup`, reads the emitted instructions, and follows them
// (scan engines/profiles → discuss roles with the user → `subagent set`). This is
// the same surface the SessionStart nudge points the user at.
var subagentSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Print the agent-assisted subagent-setup prompt for the LLM to follow",
	Long: `Emit the subagent-setup prompt: instructions for the LLM to interview you and
configure subagents (engine↔profile bindings) collaboratively.

Subagents are a general orchestration primitive — cheap finders, code-review
lenses, escalating developers, and more — that map/weave fan across. The prompt
has the agent SCAN what's available (engines via 'ctxloom llm list', profiles via
'ctxloom profile list' + search_library), DISCUSS which roles you want, then write
them with 'ctxloom subagent set'. Engine choice stays yours.

Run this (or ask your agent to) when you have profiles but no subagents yet.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Println(subagentSetupPrompt)
		return w.Err()
	},
}

var (
	subagentSetEngine   string
	subagentSetProfiles []string
)

// subagentSetCmd is the write half: add or update a LOCAL subagent under the
// `subagents:` config key. This is what the setup flow calls to persist the
// binding the user chose. Generic by design — it stores whatever name/engine/
// profiles are passed; no role/lens names are baked in.
var subagentSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Add or update a local subagent (engine↔profile binding)",
	Long: `Bind an LLM engine to one or more profiles under a name, written to the
'subagents:' key of .ctxloom/config.yaml. Re-running with the same name updates it.

The engine (optional) overrides the profiles' own llm; omit it to use the project
default. Profiles compose into one assembled context.

Examples:
  ctxloom subagent set finder --engine claude-fast --profiles finder
  ctxloom subagent set dev --engine claude-code --profiles default,go-developer
  ctxloom subagent set reviewer --profiles cr-correctness-golang   # default engine`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		entry, err := operations.SetSubagent(cfg, operations.SetSubagentRequest{
			Name:     name,
			Engine:   subagentSetEngine,
			Profiles: subagentSetProfiles,
		})
		if err != nil {
			return err
		}
		return emit(cmd, entry, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			engine := entry.Engine
			if engine == "" {
				engine = "project default"
			}
			w.Printf("Set subagent %q (engine: %s", entry.Name, engine)
			if len(entry.Profiles) > 0 {
				w.Printf(", profiles: %s", strings.Join(entry.Profiles, ", "))
			}
			w.Println(")")
			return w.Err()
		})
	},
}

// subagentRemoveCmd deletes a config-key subagent and persists the removal.
var subagentRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a local subagent from config.yaml",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "help" {
			return cmd.Help()
		}
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if err := operations.RemoveSubagent(cfg, name); err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Removed subagent %q\n", name)
		return w.Err()
	},
}

func init() {
	rootCmd.AddCommand(subagentCmd)
	subagentCmd.AddCommand(subagentListCmd)
	subagentCmd.AddCommand(subagentShowCmd)
	subagentCmd.AddCommand(subagentSetupCmd)
	subagentCmd.AddCommand(subagentSetCmd)
	subagentCmd.AddCommand(subagentRemoveCmd)

	subagentSetCmd.Flags().StringVar(&subagentSetEngine, "engine", "", "LLM engine/label to bind (overrides the profiles' llm; empty = project default)")
	subagentSetCmd.Flags().StringSliceVar(&subagentSetProfiles, "profiles", nil, "Comma-separated profile name(s)/ref(s) to compose")

	subagentShowCmd.ValidArgsFunction = completeSubagentNames
	subagentRemoveCmd.ValidArgsFunction = completeSubagentNames
	_ = subagentSetCmd.RegisterFlagCompletionFunc("engine", completeLLMNames)
	_ = subagentSetCmd.RegisterFlagCompletionFunc("profiles", completeProfileNames)
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
