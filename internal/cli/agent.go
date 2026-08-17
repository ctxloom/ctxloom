package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/resources"
)

// ctxloomInitPrompt is ctxloom's built-in FIVE-PHASE setup body (orient+scan,
// companions, profiles+content, agents, close) — ONE text used by THREE doors
// (init-as-skill plan §4.2/ADDITION "one body, N doors"): `ctxloom init`'s
// discovery launch (discoverySessionPrompt, init.go), `ctxloom init prompt`'s
// re-entry pointer, and the `/ctxloom-init` slash command available in
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

// Bare `ctxloom agent` lists the configured agents: the collection is the
// one thing the noun is about, and reading it touches nothing.
var agentCmd = groupNodeDefault(&cobra.Command{
	Use:   "agent",
	Short: "Inspect local agents (engine↔profile bindings)",
	Long: `Inspect agents — named, LOCAL-ONLY bindings of an LLM engine to one or
more composed profiles.

An agent names an 'engine' (the LLM config label/backend, which overrides the
constituent profiles' own llm) and a list of 'profiles' that compose into one
assembled context. Agents are defined solely in your .ctxloom — under the
'agents:' key of config.yaml and/or as .ctxloom/agents/<name>.yaml files.
They are never shipped in bundles or remotes: the engine choice is yours.`,
}, "list")

var agentListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all local agents",
	RunE:    runAgentList,
}

func runAgentList(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	list := operations.ListAgents(cfg)
	return emit(cmd, list, func() error {
		return renderAgentList(cmd.OutOrStdout(), list)
	})
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
		if s.LLM != "" {
			w.Printf(" (llm: %s)", s.LLM)
		} else {
			w.Printf(" (llm: project default)")
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
		if s.Driving != "" {
			w.Printf("    driving: %s\n", s.Driving)
		}
		if s.ConfigHome != "" {
			w.Printf("    config_home: %s\n", s.ConfigHome)
		}
		if len(s.Escalation) > 0 {
			w.Printf("    escalation: %d rung(s)\n", len(s.Escalation))
		}
	}
	return w.Err()
}

var agentShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show an agent and its resolved engine",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentShow,
}

func runAgentShow(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	def, err := operations.GetAgent(cfg, name)
	if err != nil {
		// "help" is a legal agent name; the courtesy shortcut fires only
		// when there is no such agent (see runBundleShow).
		if name == helpArgName {
			return cmd.Help()
		}
		return err
	}
	// Resolve the engine/backend (and compose the profiles) so the override
	// behavior is visible. Resolution is fault-tolerant for show: a failure
	// (e.g. a missing constituent profile) still prints the definition with a
	// warning rather than failing the command.
	resolved, rerr := operations.ResolveAgent(cmd.Context(), cfg, name, "")
	payload := agentShowJSON{Definition: def, Resolved: resolved}
	if rerr != nil {
		payload.ResolutionError = rerr.Error()
	} else {
		// The loss half of the report, read the SAME way `profile materialize`
		// already reads it (operations.CapabilityLoss wraps the identical
		// backends.UncarriedSurfaces call) — so switching an agent's engine
		// binding names what the NEW engine cannot carry that the resolved
		// profiles' hooks configuration actually uses, instead of `agent show`
		// reporting only the swap succeeded (trusting-ambiguity).
		payload.CapabilityLoss = operations.CapabilityLoss(cfg, resolved.Backend, resolved.Profiles)
	}
	return emit(cmd, payload, func() error {
		return renderAgentShow(cmd.OutOrStdout(), def, resolved, rerr, payload.CapabilityLoss)
	})
}

// agentShowJSON is the --format json shape for `agent show`: the declared
// definition, the resolved engine/backend (absent when resolution failed), and
// — when it failed — WHY.
//
// Resolution is fault-tolerant here: a missing constituent profile still prints
// the definition. The text view says so ("Resolved engine: unavailable (…)"),
// so the structured view has to as well, or the two formats of one command
// disagree about whether anything went wrong. Omitted entirely on success, so a
// consumer can test for the key itself.
type agentShowJSON struct {
	Definition      *operations.AgentEntry    `json:"definition"`
	Resolved        *operations.ResolvedAgent `json:"resolved,omitempty"`
	ResolutionError string                    `json:"resolution_error,omitempty"`
	// CapabilityLoss names the parts of the resolved profiles' hooks
	// configuration the resolved backend has no structural place for — empty
	// (omitted) when resolution failed or nothing configured is lost. Additive
	// field, same shape MaterializeProfileResult.NotCarried already carries.
	CapabilityLoss []agent.SurfaceLoss `json:"capability_loss,omitempty"`
}

// renderAgentShow writes the human view of one agent: what the definition
// DECLARES, then what resolution made of it. The two halves are separate
// functions because they answer different questions — "what did I write down?"
// and "what will actually run?" — and each is a run of independent
// omit-when-empty arms.
func renderAgentShow(out io.Writer, def *operations.AgentEntry, resolved *operations.ResolvedAgent, rerr error, losses []agent.SurfaceLoss) error {
	w := iox.NewErrWriter(out)
	renderAgentDeclaration(w, def)
	renderAgentResolution(w, resolved, rerr, losses)
	return w.Err()
}

// renderAgentDeclaration writes the agent AS DECLARED: identity, source, the
// declared engine (with the project-default hint when unset), and the optional
// axes, each omitted when empty.
func renderAgentDeclaration(w *iox.ErrWriter, def *operations.AgentEntry) {
	w.Printf("Agent: %s\n", def.Name)
	if def.Source != "" {
		w.Printf("Source: %s\n", def.Source)
	}
	if def.LLM != "" {
		w.Printf("Engine (declared): %s\n", def.LLM)
	} else {
		w.Println("Engine (declared): (project default)")
	}
	if def.Runtime != "" {
		w.Printf("Runtime: %s\n", def.Runtime)
	}
	if def.Permissions != "" {
		w.Printf("Permissions: %s\n", def.Permissions)
	}
	if def.Driving != "" {
		w.Printf("Driving: %s\n", def.Driving)
	}
	if def.ConfigHome != "" {
		w.Printf("Config home (declared): %s\n", def.ConfigHome)
	}
	renderAgentEscalation(w, def.Escalation)
	writeBulletList(w, "Profiles", def.Profiles)
}

// renderAgentEscalation writes the approval ladder, one line per rung. A rung
// naming no kinds is a catch-all, so it says so rather than printing nothing.
func renderAgentEscalation(w *iox.ErrWriter, ladder []agents.EscalationRung) {
	if len(ladder) == 0 {
		return
	}
	w.Printf("Escalation: %d rung(s)\n", len(ladder))
	for _, r := range ladder {
		kinds := "all kinds"
		if len(r.Kinds) > 0 {
			kinds = strings.Join(r.Kinds, ",")
		}
		w.Printf("  - %s: %s\n", kinds, r.Action)
	}
}

// renderAgentResolution writes what the declaration actually resolves to. A
// resolution FAILURE is reported and ends the view: there is no resolved engine
// to describe, and `agent show` stays fault-tolerant rather than erroring.
//
// losses is the capability-loss report (operations.CapabilityLoss) for this
// same resolved engine binding — printed alongside the resolution, not in a
// separate pass, for the same reason profile_materialize.go interleaves NOT
// carried lines with wrote lines: a reader who scans only the top of `agent
// show` must not come away with "the swap succeeded" as the whole story when
// the new engine silently drops something the old one carried (j002000).
func renderAgentResolution(w *iox.ErrWriter, resolved *operations.ResolvedAgent, rerr error, losses []agent.SurfaceLoss) {
	if rerr != nil {
		w.Printf("Resolved llm: unavailable (%v)\n", rerr)
		return
	}
	w.Printf("Resolved llm: %s", resolved.Label)
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
	if resolved.ConfigHome != "" {
		w.Printf("Resolved config home: %s\n", resolved.ConfigHome)
	}
	w.Printf("Composed fragments: %d\n", len(resolved.Fragments))
	for _, loss := range losses {
		w.Printf("  NOT carried: %s (%s) — %s\n", loss.Surface, loss.Detail, loss.Reason)
	}
}

// runSetupPromptCmd prints the full five-phase ctxloom setup prompt for the
// LLM to run. It writes the prompt to stdout; the agent (which has shell
// access) runs `ctxloom init prompt`, reads the emitted instructions, and
// follows them. This is a re-entry POINTER onto the SAME body `/ctxloom-init`
// and the `ctxloom init` discovery launch use (ctxloomInitPrompt) — not a
// separate/duplicated prompt — so all doors evolve together. This is
// initPromptCmd's RunE (`ctxloom init prompt`); the `agent setup` spelling
// that used to share it was deleted with the rest of the deprecated aliases.
func runSetupPromptCmd(cmd *cobra.Command, args []string) error {
	// A bundle (or installed companion) can ship its own `agent-setup` command
	// to AUGMENT the built-in onboarding/composition guidance (data, not
	// baked into the binary); every match's content adds to the built-in,
	// never replaces it. discoverySessionPrompt (init.go) is the single
	// resolver every door shares, so this one cannot drift from the body the
	// discovery session receives; it degrades to the built-in alone for the
	// nil config GetConfig returns on a load failure.
	cfg, _ := GetConfig()
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Println(discoverySessionPrompt(cfg))
	return w.Err()
}

var (
	agentSetLLM         string
	agentSetProfiles    []string
	agentSetRuntime     string
	agentSetSurfaces    []string
	agentSetPermissions string
	agentSetConfigHome  string
)

// agentWriteLong is the shared body text for `agent create` and `agent edit`:
// the axes an agent binding carries are identical either way, so the two help
// pages cannot drift about what they mean.
const agentWriteLong = `The engine (optional) overrides the profiles' own llm; omit it to use the project
default. Profiles compose into one assembled context. Runtime (optional:
host|container-rootless|container-rootful) sets WHERE this agent's engine
process executes; omit it to inherit the project's 'runtime:' default — which
is a default rather than a decision, so 'ctxloom init' always writes one
explicitly. Not every engine can take a container axis: one with no way to
authenticate inside a container is refused here, and 'ctxloom llm list' reports
per engine which values it can be given. The workspace axis (worktree vs shared
dir) is NOT set here — it is a session trait chosen at invocation time
(run/acp --workspace, or an agent_run spawn's workspace field). Driving
(optional: conversational|oneshot) sets
the per-turn execution axis; omit it to keep the default conversational
(warm-engine) model. oneshot requires a resume-capable engine and is
EXPERIMENTAL in this release — executable, but its interfaces and behavior
may change. Config-home (optional: project|host) decides which engine config
home this agent's claude-code/codex/kiro runs get on the in-tree (workspace:
none) axis: "project" points the engine at a ctxloom-controlled, PER-SESSION
home under .ctxloom/state/<session>/home/, isolated from your own and thrown
away with the session; "host" (also the default when omitted) keeps the
engine's real host home, which ctxloom never writes. It wins on every
invocation path this binding resolves through — a bare run under
default_agent, run --agent, a delegated child, a oneshot fan member alike.`

// agentCreateCmd and agentEditCmd are the write half, split off the retired
// upsert `agent set` (verb-spine reorg §5): the spine has `create` (fails if
// the name is taken) and `edit` (fails if it is not), and blind
// create-or-update is not a third verb. Both are LOCAL agents under the
// `agents:` config key. Generic by design — whatever name/engine/profiles are
// passed is what gets stored; no role/lens names are baked in.
var agentCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a local agent (engine↔profile binding)",
	Long: `Bind an LLM engine to one or more profiles under a NEW name, written to the
'agents:' key of .ctxloom/config.yaml. Refuses a name that already names an
agent — change an existing one with 'ctxloom agent edit'.

` + agentWriteLong + `

Examples:
  ctxloom agent create finder --engine claude-fast --profiles finder
  ctxloom agent create dev --engine claude-code --profiles default,go-developer --runtime container-rootless
  ctxloom agent create reviewer --profiles cr-correctness-golang   # default engine`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentCreate,
}

var agentEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit an existing local agent (engine↔profile binding)",
	Long: `Change an EXISTING agent's binding in the 'agents:' key of
.ctxloom/config.yaml. Refuses a name no agent has — create one with
'ctxloom agent create'.

Only the flags you pass are applied; every unnamed field keeps its current
value, so 'ctxloom agent edit dev --runtime container-rootless' does not wipe dev's
llm, profiles, posture or escalation ladder. An explicitly-supplied empty
value (--llm "") clears that field.

driving has NO flag: it is agent DATA, authored in config.yaml under
agents.<name>. A flag that only writes a config field is a second way to say
the same thing, and the two spellings drift.

` + agentWriteLong + `

Examples:
  ctxloom agent edit dev --runtime container-rootless
  ctxloom agent edit reviewer --profiles cr-correctness-golang,cr-security`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentEdit,
}

func runAgentCreate(cmd *cobra.Command, args []string) error {
	return writeAgentBinding(cmd, args[0], false)
}

func runAgentEdit(cmd *cobra.Command, args []string) error {
	return writeAgentBinding(cmd, args[0], true)
}

// writeAgentBinding is `agent create`/`agent edit`'s shared body. mustExist
// picks which precondition is enforced: edit refuses a name nothing defines,
// create refuses one something already does. Enforcing it here — rather than
// letting the upsert through — is the whole point of the split: a typo'd
// `edit` used to silently mint a brand-new agent, and a `create` over a live
// name used to silently overwrite it.
func writeAgentBinding(cmd *cobra.Command, name string, mustExist bool) error {
	// No help shortcut: the positional arg NAMES the agent to write, and the
	// shortcut made an agent called "help" impossible to create.
	cfg, err := GetConfigForUpdate()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := checkAgentExistence(cfg, name, mustExist); err != nil {
		return err
	}
	entry, err := operations.SetAgent(config.NewManager(), cfg, buildSetAgentRequest(cmd, name))
	if err != nil {
		return err
	}
	return emit(cmd, entry, func() error {
		return renderAgentWritten(cmd.OutOrStdout(), entry, mustExist)
	})
}

// checkAgentExistence enforces create-vs-edit's differing precondition against
// the MERGED agent view (config key + .ctxloom/agents/*.yaml), so a
// directory-defined agent counts as existing for both verbs.
func checkAgentExistence(cfg *config.Config, name string, mustExist bool) error {
	_, exists := cfg.Agent(name)
	switch {
	case mustExist && !exists:
		return fmt.Errorf("no agent named %q — create it with `ctxloom agent create %s`", name, name)
	case !mustExist && exists:
		return fmt.Errorf("agent %q already exists — change it with `ctxloom agent edit %s`", name, name)
	}
	return nil
}

// buildSetAgentRequest sends only the flags the user actually TYPED. A nil
// field means "not named", which SetAgent keeps at its existing value; an
// explicitly-supplied empty value (--llm "") still clears.
func buildSetAgentRequest(cmd *cobra.Command, name string) operations.SetAgentRequest {
	req := operations.SetAgentRequest{Name: name}
	if cmd.Flags().Changed("llm") {
		req.LLM = &agentSetLLM
	}
	if cmd.Flags().Changed("profiles") {
		req.Profiles = &agentSetProfiles
	}
	if cmd.Flags().Changed("runtime") {
		req.Runtime = &agentSetRuntime
	}
	if cmd.Flags().Changed("surface") {
		// Parsed with the SAME function `profile materialize --surface` uses, so
		// the two spellings cannot drift. Engine support is checked in SetAgent,
		// which is the only place that knows which engine this write results in.
		parsed, err := parseSurfaceOverrides(agentSetSurfaces)
		if err != nil {
			// Reported by SetAgent's own validation path; leaving the map nil
			// here would silently drop the flag instead.
			req.Surfaces = map[string]string{}
			for _, p := range agentSetSurfaces {
				if k, v, ok := strings.Cut(p, "="); ok {
					req.Surfaces[strings.TrimSpace(k)] = strings.TrimSpace(v)
				} else {
					req.Surfaces[p] = ""
				}
			}
		} else {
			req.Surfaces = make(map[string]string, len(parsed))
			for k, a := range parsed {
				req.Surfaces[k.String()] = a.String()
			}
		}
	}
	if cmd.Flags().Changed("permissions") {
		req.Permissions = &agentSetPermissions
	}
	if cmd.Flags().Changed("config-home") {
		req.ConfigHome = &agentSetConfigHome
	}
	return req
}

// renderAgentWritten writes the one-line confirmation for a created/edited
// agent, naming which of the two happened.
func renderAgentWritten(out io.Writer, entry *operations.AgentEntry, edited bool) error {
	w := iox.NewErrWriter(out)
	verb := "Created"
	if edited {
		verb = "Updated"
	}
	llmLabel := entry.LLM
	if llmLabel == "" {
		llmLabel = "project default"
	}
	w.Printf("%s agent %q (llm: %s", verb, entry.Name, llmLabel)
	if len(entry.Profiles) > 0 {
		w.Printf(", profiles: %s", strings.Join(entry.Profiles, ", "))
	}
	if entry.Runtime != "" {
		w.Printf(", runtime: %s", entry.Runtime)
	}
	if entry.Permissions != "" {
		w.Printf(", permissions: %s", entry.Permissions)
	}
	if entry.Driving != "" {
		w.Printf(", driving: %s", entry.Driving)
	}
	if entry.ConfigHome != "" {
		w.Printf(", config_home: %s", entry.ConfigHome)
	}
	w.Println(")")
	return w.Err()
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
	RunE: runAgentDefault,
}

func runAgentDefault(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfigForUpdate()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	w := iox.NewErrWriter(cmd.OutOrStdout())

	// No argument: report the current default agent.
	if len(args) == 0 {
		return renderDefaultAgent(w, cfg.GetDefaultAgent())
	}

	name := args[0]
	// Advisory only (fault tolerance): warn but don't block when the named
	// agent isn't defined yet — a bare run degrades gracefully and the user
	// may define it next. An UNDEFINED agent named "help" is read as the
	// courtesy help request instead, so `ctxloom agent default help` does
	// not quietly bind a default nobody asked for; a DEFINED one is bound.
	if _, ok := cfg.Agent(name); !ok {
		if name == helpArgName {
			return cmd.Help()
		}
		clidiag.Warn("ctxloom", "agent %q is not defined yet; a bare `ctxloom run` will degrade to empty context until it is", name)
	}
	if err := setDefaultAgent(name); err != nil {
		return err
	}
	w.Printf("Set default agent to %q.\n", name)
	return w.Err()
}

// renderDefaultAgent writes the no-argument report: which agent a bare
// `ctxloom run` binds, or that none is set.
func renderDefaultAgent(w *iox.ErrWriter, current string) error {
	if current == "" {
		w.Println("No default agent set.")
		return w.Err()
	}
	w.Printf("Default agent: %s\n", current)
	return w.Err()
}

// setDefaultAgent persists `default_agent` in .ctxloom/config.yaml.
func setDefaultAgent(name string) error {
	if err := config.NewManager().Update(func(d *config.Draft) error {
		d.DefaultAgent = name
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

var agentRemoveYes bool

// agentRemoveCmd removes a config-key agent and persists the removal. Bare
// invocation reports what would be removed and removes nothing (exit 0);
// --yes applies it.
var agentRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a local agent from config.yaml",
	Long: `Remove a local agent binding from config.yaml.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentRemove,
}

func runAgentRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := GetConfigForUpdate()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	applyCmd := fmt.Sprintf("ctxloom agent remove %s --yes", name)
	if !agentRemoveYes {
		if _, ok := cfg.Agent(name); !ok {
			// See runAgentShow: only an ABSENT "help" is the courtesy request.
			if name == helpArgName {
				return cmd.Help()
			}
			return fmt.Errorf("agent %q is not defined", name)
		}
		target := fmt.Sprintf("agent %q", name)
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}

	if err := operations.RemoveAgent(config.NewManager(), cfg, name); err != nil {
		// See runAgentShow: only an ABSENT "help" is the courtesy request.
		if name == helpArgName {
			return cmd.Help()
		}
		return err
	}
	return emit(cmd, struct {
		Status string `json:"status"`
		Name   string `json:"name"`
	}{Status: "removed", Name: name}, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Removed agent %q\n", name)
		return w.Err()
	})
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentShowCmd)
	agentCmd.AddCommand(agentCreateCmd)
	agentCmd.AddCommand(agentEditCmd)
	agentCmd.AddCommand(agentDefaultCmd)
	agentCmd.AddCommand(agentRemoveCmd)

	// create and edit take the SAME axis flags against the SAME flag vars:
	// they are one concept split by precondition, not two option sets.
	for _, c := range []*cobra.Command{agentCreateCmd, agentEditCmd} {
		registerAgentWriteFlags(c)
	}

	agentShowCmd.ValidArgsFunction = completeAgentNames
	agentEditCmd.ValidArgsFunction = completeAgentNames
	agentRemoveCmd.ValidArgsFunction = completeAgentNames
	agentDefaultCmd.ValidArgsFunction = completeAgentNames

	agentRemoveCmd.Flags().BoolVarP(&agentRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
}

// registerAgentWriteFlags binds the agent-binding axis flags to cmd (shared by
// `agent create` and `agent edit`).
func registerAgentWriteFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&agentSetLLM, "llm", "", "llm.configs label to bind (overrides the profiles' llm; empty = project default)")
	cmd.Flags().StringSliceVar(&agentSetProfiles, "profiles", nil, "Comma-separated profile name(s)/ref(s) to compose")
	cmd.Flags().StringVar(&agentSetRuntime, "runtime", "", "Runtime axis: where this agent's engine executes (host|container-rootless|container-rootful; empty = project default). `ctxloom llm list` reports which of these each engine can be given")
	cmd.Flags().StringArrayVar(&agentSetSurfaces, "surface", nil,
		"Delivery preference for this agent: kind=approach (repeatable). Validated against the agent's engine; run ctxloom profile materialize --help to see what each engine supports.")
	cmd.Flags().StringVar(&agentSetPermissions, "permissions", "", "Permission posture: default|acceptEdits|plan|bypass (empty = engine/built-in default)")
	cmd.Flags().StringVar(&agentSetConfigHome, "config-home", "",
		"Per-engine config-home policy on the in-tree axis: project|host (empty = host, the default — controlled homes are opt-in)")
	_ = cmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = cmd.RegisterFlagCompletionFunc("profiles", completeProfileNames)
	_ = cmd.RegisterFlagCompletionFunc("runtime", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return isolation.RuntimeNames(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
	_ = cmd.RegisterFlagCompletionFunc("config-home", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return agents.ConfigHomeNames(), cobra.ShellCompDirectiveNoFileComp
	})
}

// completeWorkspaceNames completes the session-level --workspace flag values
// (run/acp) from the isolation package's single source.
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
