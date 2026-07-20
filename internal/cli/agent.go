package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/resources"
)

// ctxloomInitPrompt is ctxloom's built-in FIVE-PHASE setup body (orient+scan,
// companions, profiles+content, agents, close) — ONE text used by THREE doors
// (init-as-skill plan §4.2/ADDITION "one body, N doors"): `ctxloom init`'s
// discovery launch (discoverySessionPrompt, init.go), `ctxloom init prompt`'s
// re-entry pointer below, and the `/ctxloom-init` slash command available in
// every ordinary session (resources/commands/ctxloom-init.md, exported via
// internal/lm/backends.builtinCommands — unconditional, on ALL backends'
// command catalogs, loaded by the engine only on invocation, never injected
// into always-on assembled context). ACP (server or client) is intentionally
// NOT one of the five phases — it is optional, out-of-band configuration
// handed off to the acp-setup Agent Skill; the working outcome of these five
// phases alone is a functioning CLI/TUI. It is deliberately a markdown
// RESOURCE, not Go: the role palette and example names are data that can
// evolve (e.g. via a ctxloom-default augmentation) without a code change.
// Read via GetBuiltinCommandBody (not GetPromptText) because the SAME file
// also carries the frontmatter `description:` the slash-command export uses
// — one file, two consumption paths, never two copies to drift.
var ctxloomInitPrompt = resources.MustGetBuiltinCommandBody("ctxloom-init")

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

// runSetupPromptCmd prints the full five-phase ctxloom setup prompt for the
// LLM to run. It writes the prompt to stdout; the agent (which has shell
// access) runs `ctxloom init prompt`, reads the emitted instructions, and
// follows them. This is a re-entry POINTER onto the SAME body `/ctxloom-init`
// and the `ctxloom init` discovery launch use (ctxloomInitPrompt) — not a
// separate/duplicated prompt — so all doors evolve together. Shared by
// initPromptCmd (the real home, Decision 6 of the CLI-primary reorg plan) and
// agentSetupCmd (kept as a Deprecated alias — 'setup' printing the whole
// interview was misfiled under 'agent').
func runSetupPromptCmd(cmd *cobra.Command, args []string) error {
	// A bundle (or installed companion) can ship its own `agent-setup` command
	// to AUGMENT the built-in onboarding/composition guidance (data, not
	// baked into the binary); every match's content adds to the built-in,
	// never replaces it. Falls back to the built-in alone when none is
	// installed or config can't load.
	prompt := ctxloomInitPrompt
	if cfg, err := GetConfig(); err == nil {
		prompt = operations.ResolveSetupPrompt(cfg, ctxloomInitPrompt)
	}
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Println(prompt)
	return w.Err()
}

// agentSetupCmd is a Deprecated alias for `ctxloom init prompt` (Decision 6):
// 'setup' printing the whole setup interview was misfiled under 'agent'.
// Still runs — same RunE, runSetupPromptCmd — so nothing breaks abruptly.
var agentSetupCmd = &cobra.Command{
	Use:        "setup",
	Short:      "Print ctxloom's setup prompt (companions, profiles, agents) for the LLM to follow",
	Deprecated: agentSetupDeprecation,
	Args:       cobra.NoArgs,
	RunE:       runSetupPromptCmd,
}

// agentSetupDeprecation is the one-line pointer cobra prints whenever
// `ctxloom agent setup` still runs (see memory.go's memoryListDeprecation for
// the same shape/rationale — additive deprecation shim, not yet a removal).
const agentSetupDeprecation = "use `ctxloom init prompt` instead"

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
		cfg, err := GetConfigForUpdate()
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

// agentDefaultCmd shows or sets the always-bound DEFAULT AGENT — the binding a
// bare `ctxloom run` (no --agent, no -p/-f/-t) resolves. It is the replacement
// for the retired `profile default`: the default context is now whatever the
// default agent composes.
var agentDefaultCmd = &cobra.Command{
	Use:   "default [name]",
	Short: "Show or set the always-bound default agent",
	Long: `Show or set the DEFAULT AGENT: the agent a bare 'ctxloom run' (no --agent, no
-p/-f/-t) binds — its composed profiles become the context and its engine +
runtime + permissions the transport. This replaces the retired 'profile default'.

With no argument, prints the current default agent. With a name, sets it (written
as 'default_agent' in .ctxloom/config.yaml). The named agent should exist under
'agents:' or as .ctxloom/agents/<name>.yaml — an unknown name is accepted with a
warning (a bare run then degrades to empty context until it is defined).

Examples:
  ctxloom agent default            # show the current default agent
  ctxloom agent default dev        # make 'dev' the default agent`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfigForUpdate()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())

		// No argument: report the current default agent.
		if len(args) == 0 {
			if cfg.GetDefaultAgent() == "" {
				w.Println("No default agent set.")
				return w.Err()
			}
			w.Printf("Default agent: %s\n", cfg.GetDefaultAgent())
			return w.Err()
		}

		name := args[0]
		if name == "help" {
			return cmd.Help()
		}
		// Advisory only (fault tolerance): warn but don't block when the named
		// agent isn't defined yet — a bare run degrades gracefully and the user
		// may define it next.
		if _, ok := cfg.Agent(name); !ok {
			clidiag.Warn("ctxloom", "agent %q is not defined yet; a bare `ctxloom run` will degrade to empty context until it is", name)
		}
		cfg.DefaultAgent = name
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		w.Printf("Set default agent to %q.\n", name)
		return w.Err()
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
		cfg, err := GetConfigForUpdate()
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
	agentCmd.AddCommand(agentDefaultCmd)
	agentCmd.AddCommand(agentRemoveCmd)

	agentSetCmd.Flags().StringVar(&agentSetEngine, "engine", "", "LLM engine/label to bind (overrides the profiles' llm; empty = project default)")
	agentSetCmd.Flags().StringSliceVar(&agentSetProfiles, "profiles", nil, "Comma-separated profile name(s)/ref(s) to compose")
	agentSetCmd.Flags().StringVar(&agentSetRuntime, "runtime", "", "Runtime axis: where this agent's engine executes (host|container; empty = project default)")
	agentSetCmd.Flags().StringVar(&agentSetPermissions, "permissions", "", "Permission posture: default|acceptEdits|plan|bypass (empty = engine/built-in default)")

	agentShowCmd.ValidArgsFunction = completeAgentNames
	agentRemoveCmd.ValidArgsFunction = completeAgentNames
	agentDefaultCmd.ValidArgsFunction = completeAgentNames
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
