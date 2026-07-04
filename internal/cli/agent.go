package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/resources"
)

// agentSetupPrompt is the agent-assisted setup prompt `agent setup` emits
// for the LLM to follow: SCAN available engines/profiles → DISCUSS roles with the
// user → SET the chosen engine↔profile bindings. It is deliberately a markdown
// RESOURCE, not Go: the role palette, steering, and example names are data the
// taxonomy can evolve without a code change (see resources/prompts/agent-setup.md).
var agentSetupPrompt = resources.MustGetPromptText("agent-setup")

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Inspect local agents (engine↔profile bindings)",
	Long: `Inspect agents — named, LOCAL-ONLY bindings of an LLM engine to one or
more composed profiles.

An agent names an 'engine' (the LLM config label/backend, which overrides the
constituent profiles' own llm) and a list of 'profiles' that compose into one
assembled context. Agents are defined solely in your .ctxloom — under the
'agents:' key of config.yaml and/or as .ctxloom/agents/<name>.yaml files.
They are never shipped in bundles or remotes: the engine choice is yours.`,
}

var agentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all local agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		list := operations.ListAgents(cfg)
		return emit(cmd, list, func() error {
			return renderAgentList(cmd.OutOrStdout(), list)
		})
	},
}

// renderAgentList writes the human-readable summary of the agent list.
// Extracted from RunE so the formatting is testable without cobra/config.
func renderAgentList(out io.Writer, list []operations.AgentEntry) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Println("No agents defined.")
		w.Println("Define one under 'agents:' in .ctxloom/config.yaml or as .ctxloom/agents/<name>.yaml.")
		return w.Err()
	}
	w.Printf("Agents (%d):\n", len(list))
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
		if s.Runtime != "" {
			w.Printf("    runtime: %s\n", s.Runtime)
		}
		if s.Permissions != "" {
			w.Printf("    permissions: %s\n", s.Permissions)
		}
	}
	return w.Err()
}

var agentShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show an agent and its resolved engine",
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

		def, err := operations.GetAgent(cfg, name)
		if err != nil {
			return err
		}
		// Resolve the engine/backend (and compose the profiles) so the override
		// behavior is visible. Resolution is fault-tolerant for show: a failure
		// (e.g. a missing constituent profile) still prints the definition with a
		// warning rather than failing the command.
		resolved, rerr := operations.ResolveAgent(cmd.Context(), cfg, name, "")
		return emit(cmd, agentShowJSON{Definition: def, Resolved: resolved}, func() error {
			return renderAgentShow(cmd.OutOrStdout(), def, resolved, rerr)
		})
	},
}

// agentShowJSON is the --format json shape for `agent show`: the declared
// definition plus the resolved engine/backend (nil when resolution failed).
type agentShowJSON struct {
	Definition *operations.AgentEntry    `json:"definition"`
	Resolved   *operations.ResolvedAgent `json:"resolved,omitempty"`
}

func renderAgentShow(out io.Writer, def *operations.AgentEntry, resolved *operations.ResolvedAgent, rerr error) error {
	w := iox.NewErrWriter(out)
	w.Printf("Agent: %s\n", def.Name)
	if def.Source != "" {
		w.Printf("Source: %s\n", def.Source)
	}
	if def.Engine != "" {
		w.Printf("Engine (declared): %s\n", def.Engine)
	} else {
		w.Println("Engine (declared): (project default)")
	}
	if def.Runtime != "" {
		w.Printf("Runtime: %s\n", def.Runtime)
	}
	if def.Permissions != "" {
		w.Printf("Permissions: %s\n", def.Permissions)
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
	// The posture an unflagged interactive run actually uses — so a blank-declared
	// claude-code agent's real host-bypass is visible, not hidden behind "".
	if resolved.EffectivePermissions != "" {
		w.Printf("Resolved permissions: %s\n", resolved.EffectivePermissions)
	}
	w.Printf("Composed fragments: %d\n", len(resolved.Fragments))
	return w.Err()
}

// agentSetupCmd surfaces the agent-assisted setup prompt for the LLM to run.
// It writes the prompt to stdout; the agent (which has shell access) runs
// `ctxloom agent setup`, reads the emitted instructions, and follows them
// (scan engines/profiles → discuss roles with the user → `agent set`). This is
// the same surface the SessionStart nudge points the user at.
var agentSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Print the agent-assisted agent-setup prompt for the LLM to follow",
	Long: `Emit the agent-setup prompt: instructions for the LLM to interview you and
configure agents (engine↔profile bindings) collaboratively.

Agents are a general orchestration primitive — cheap finders, code-review
lenses, escalating developers, and more — that map/weave fan across. The prompt
has the agent SCAN what's available (engines via 'ctxloom llm list', profiles via
'ctxloom profile list' + search_library), DISCUSS which roles you want, then write
them with 'ctxloom agent set'. Engine choice stays yours.

Run this (or ask your agent to) when you have profiles but no agents yet.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// A bundle can ship its own `agent-setup` skill to override the built-in
		// onboarding/composition guidance (data, not baked into the binary); fall
		// back to the built-in when none is installed or config can't load.
		prompt := agentSetupPrompt
		if cfg, err := GetConfig(); err == nil {
			prompt = operations.ResolveSetupPrompt(cfg, agentSetupPrompt)
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Println(prompt)
		return w.Err()
	},
}

var (
	agentSetEngine      string
	agentSetProfiles    []string
	agentSetRuntime     string
	agentSetPermissions string
)

// agentSetCmd is the write half: add or update a LOCAL agent under the
// `agents:` config key. This is what the setup flow calls to persist the
// binding the user chose. Generic by design — it stores whatever name/engine/
// profiles are passed; no role/lens names are baked in.
var agentSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Add or update a local agent (engine↔profile binding)",
	Long: `Bind an LLM engine to one or more profiles under a name, written to the
'agents:' key of .ctxloom/config.yaml. Re-running with the same name updates it.

The engine (optional) overrides the profiles' own llm; omit it to use the project
default. Profiles compose into one assembled context. Runtime (optional:
host|container) sets WHERE this agent's engine process executes; omit it to
inherit the project's 'runtime:' default. The workspace axis (worktree vs shared
dir) is NOT set here — it is a session trait chosen at invocation time
(run/map/weave --workspace).

Examples:
  ctxloom agent set finder --engine claude-fast --profiles finder
  ctxloom agent set dev --engine claude-code --profiles default,go-developer --runtime container
  ctxloom agent set reviewer --profiles cr-correctness-golang   # default engine`,
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
		entry, err := operations.SetAgent(cfg, operations.SetAgentRequest{
			Name:        name,
			Engine:      agentSetEngine,
			Profiles:    agentSetProfiles,
			Runtime:     agentSetRuntime,
			Permissions: agentSetPermissions,
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
			w.Printf("Set agent %q (engine: %s", entry.Name, engine)
			if len(entry.Profiles) > 0 {
				w.Printf(", profiles: %s", strings.Join(entry.Profiles, ", "))
			}
			if entry.Runtime != "" {
				w.Printf(", runtime: %s", entry.Runtime)
			}
			if entry.Permissions != "" {
				w.Printf(", permissions: %s", entry.Permissions)
			}
			w.Println(")")
			return w.Err()
		})
	},
}

// agentRemoveCmd deletes a config-key agent and persists the removal.
var agentRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a local agent from config.yaml",
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
		if err := operations.RemoveAgent(cfg, name); err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Removed agent %q\n", name)
		return w.Err()
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentShowCmd)
	agentCmd.AddCommand(agentSetupCmd)
	agentCmd.AddCommand(agentSetCmd)
	agentCmd.AddCommand(agentRemoveCmd)

	agentSetCmd.Flags().StringVar(&agentSetEngine, "engine", "", "LLM engine/label to bind (overrides the profiles' llm; empty = project default)")
	agentSetCmd.Flags().StringSliceVar(&agentSetProfiles, "profiles", nil, "Comma-separated profile name(s)/ref(s) to compose")
	agentSetCmd.Flags().StringVar(&agentSetRuntime, "runtime", "", "Runtime axis: where this agent's engine executes (host|container; empty = project default)")
	agentSetCmd.Flags().StringVar(&agentSetPermissions, "permissions", "", "Permission posture: default|acceptEdits|plan|bypass (empty = engine/built-in default)")

	agentShowCmd.ValidArgsFunction = completeAgentNames
	agentRemoveCmd.ValidArgsFunction = completeAgentNames
	_ = agentSetCmd.RegisterFlagCompletionFunc("engine", completeLLMNames)
	_ = agentSetCmd.RegisterFlagCompletionFunc("profiles", completeProfileNames)
	_ = agentSetCmd.RegisterFlagCompletionFunc("runtime", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return isolation.RuntimeNames(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = agentSetCmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
}

// completeWorkspaceNames completes the session-level --workspace flag values
// (run/map/weave) from the isolation package's single source.
func completeWorkspaceNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return isolation.WorkspaceNames(), cobra.ShellCompDirectiveNoFileComp
}

// completeAgentNames completes positional agent-name args.
func completeAgentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, s := range operations.ListAgents(cfg) {
		names = append(names, s.Name)
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}
