package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// `llm create`/`llm edit` — the write half of the parity gap with `agent`
// (agent.go's agentCreateCmd/agentEditCmd/writeAgentBinding is the template
// this mirrors). agent create --llm <label> draws from a vocabulary this
// closes the gap on: previously enumerable (`llm list`) but not manageable.

var (
	llmSetType        string
	llmSetModel       string
	llmSetPermissions string
	llmSetFields      []string
	// llmHome writes the user-global ~/.ctxloom/config.yaml instead of the
	// project's — the scope a personal label (a LAN endpoint) belongs in.
	// Shared by create, edit and remove; `init --home` is the same switch.
	llmHome bool
)

// llmWriteLong is the shared body for `llm create`/`llm edit`: the fields an
// entry carries are identical either way.
//
// It is a FUNCTION, not a package var, and that is load-bearing. It names the
// REGISTERED engines, and registration is explicit rather than init-time — the
// composition root runs in Run(), after every package var has already been
// evaluated. A var here freezes the engine list at init, when the registry is
// still empty, and the help ships reading "the backend discriminator ()".
func llmWriteLong(reg engine.Registry) string {
	return `--type is the backend discriminator (` + userEngineNames(reg) + `);
omit it to keep ` + operations.DefaultEngineName(reg) + `'s default. --model sets the model string. --permissions
sets the label's mode, in its engine's vocabulary (` + strings.Join(operations.PostureNames(reg), "|") + ` across the
engines ctxloom knows); the engine refuses one it does not take.

--set <dotted.key>=<value> (repeatable, applied after the flags above) sets
any other field the label's engine takes — context_window,
permissions.approver, or opencode's provider.base_url, provider.output_limit,
model_options, ... (type has its own flag). The value is read as YAML (131072
is a number, true a boolean, {k: v} an object); an empty value removes the
key. The whole entry is checked against the config schema, and its
permission keys by its engine, before anything is written: an unknown key, a
value of the wrong type, or a missing required field is refused with the
schema's message and writes nothing.

--home writes the user-global ~/.ctxloom/config.yaml instead of the
project's .ctxloom/config.yaml: the place for a label every project on this
machine shares (a LAN endpoint, say). Every project reads it, beneath its own.

An entry carries NO credentials and no environment: the engine authenticates
itself and reads its environment from the shell that runs ctxloom, so export
a variable there — ctxloom's config is not where it goes.`
}

var llmCreateCmd = &cobra.Command{
	Use:   "create <label>",
	Short: "Create a new LLM engine config",
	Long:  "", // set by applyLLMWriteHelp, after the registry is composed
	Args:  cobra.ExactArgs(1),
	RunE:  runLLMCreate,
}

var llmEditCmd = &cobra.Command{
	Use:   "edit <label>",
	Short: "Edit an existing LLM engine config",
	Long:  "", // set by applyLLMWriteHelp, after the registry is composed
	Args:  cobra.ExactArgs(1),
	RunE:  runLLMEdit,
}

func runLLMCreate(cmd *cobra.Command, args []string) error { return writeLLM(cmd, args[0], false) }
func runLLMEdit(cmd *cobra.Command, args []string) error   { return writeLLM(cmd, args[0], true) }

// writeLLM is `llm create`/`llm edit`'s shared body — mirrors
// agent.go's writeAgentBinding, but first picks the file: an llm label may be
// machine-scoped (--home, the user-global config), where an agent binding is
// the project's. That asymmetry is a property of the two entities, not drift.
// reprise:accept-drift
func writeLLM(cmd *cobra.Command, label string, mustExist bool) error {
	if llmHome {
		if err := pinHomeConfig(cmd); err != nil {
			return err
		}
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := checkLLMExistence(cfg, label, mustExist); err != nil {
		return err
	}
	req, err := buildSetLLMRequest(cmd, label)
	if err != nil {
		return err
	}

	entry, err := operations.SetLLM(cmd.Context(), App(), req)
	if err != nil {
		return err
	}
	return emit(cmd, entry, func() error {
		return renderLLMWritten(cmd.OutOrStdout(), entry, mustExist)
	})
}

// checkLLMExistence enforces create-vs-edit's differing precondition
// against the MERGED "does this name resolve to anything" view
// (operations.AvailableLLMNames: registered backends UNION config-declared
// labels) — the exact set `agent create --llm`/`llm default` already
// accept, so a bare backend name like "claude-code" counts as existing even
// with no config.yaml entry (create refuses it — it would shadow the
// built-in; edit accepts it — that is how a built-in becomes explicit).
func checkLLMExistence(cfg *config.Config, label string, mustExist bool) error {
	exists := false
	for _, n := range operations.AvailableLLMNames(App().Engines(), cfg) {
		if n == label {
			exists = true
			break
		}
	}
	switch {
	case mustExist && !exists:
		return fmt.Errorf("no llm named %q — create it with `ctxloom llm create %s`", label, label)
	case !mustExist && exists:
		return fmt.Errorf("llm %q already exists — change it with `ctxloom llm edit %s`", label, label)
	}
	return nil
}

// buildSetLLMRequest sends only the flags the user actually TYPED. A nil
// field means "not named", which SetLLM keeps at its existing value; an
// explicitly-supplied empty value (--model "") still clears it. Mirrors
// buildSetAgentRequest.
func buildSetLLMRequest(cmd *cobra.Command, label string) (operations.SetLLMRequest, error) {
	req := operations.SetLLMRequest{Label: label}
	if cmd.Flags().Changed("type") {
		req.Type = &llmSetType
	}
	if cmd.Flags().Changed("model") {
		req.Model = &llmSetModel
	}
	if cmd.Flags().Changed("permissions") {
		req.Permissions = &llmSetPermissions
	}
	if cmd.Flags().Changed("set") {
		req.Set = llmSetFields
	}
	return req, nil
}

// renderLLMWritten writes the one-line confirmation for a created/edited
// llm, naming which of the two happened.
func renderLLMWritten(out io.Writer, entry *operations.LLMEntry, edited bool) error {
	w := errwriter.New(out)
	verb := "Created"
	if edited {
		verb = "Updated"
	}
	typ := entry.Type
	if typ == "" {
		typ = operations.DefaultEngineName(App().Engines())
	}
	w.Printf("%s llm %q (type: %s", verb, entry.Label, typ)
	if entry.Model != "" {
		w.Printf(", model: %s", entry.Model)
	}
	if !entry.Permissions.IsZero() {
		w.Printf(", permissions: %s", entry.Permissions)
	}
	w.Println(")")
	return w.Err()
}

func init() {
	llmCmd.AddCommand(llmCreateCmd)
	llmCmd.AddCommand(llmEditCmd)

	for _, c := range []*cobra.Command{llmCreateCmd, llmEditCmd} {
		registerLLMWriteFlags(c)
	}
}

// registerLLMWriteFlags binds the entry-axis flags to cmd (shared by `llm
// create` and `llm edit`).
func registerLLMWriteFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&llmSetType, "type", "", "backend discriminator (empty = claude-code)")
	cmd.Flags().StringVar(&llmSetModel, "model", "", "model string")
	cmd.Flags().StringVar(&llmSetPermissions, "permissions", "", "permission mode, in the label's engine's vocabulary (empty clears it)")
	cmd.Flags().StringArrayVar(&llmSetFields, "set", nil, "set a field by dotted key: <dotted.key>=<value>, the value read as YAML, empty removes it (repeatable; checked against the config schema)")
	registerLLMHomeFlag(cmd)
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return operations.EngineNames(App().Engines()), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
}

// registerLLMHomeFlag binds --home (the user-global scope) to cmd.
func registerLLMHomeFlag(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&llmHome, "home", false, "act on the user-global ~/.ctxloom/config.yaml instead of the project's")
}

// applyEngineNamedHelp fills in every piece of help that names the registered
// engines — `llm create`/`llm edit`, and `init --engine`. It must run AFTER the composition root has
// registered them, which is why none of this is a package var or an init():
// package vars and init() both run before Run() composes the registry, so the
// engine list would be empty and the help would ship saying so. rootCommand()
// is the one place that is reached only after registration.
func applyEngineNamedHelp(reg engine.Registry) {
	engines := userEngineNames(reg)
	// The scaffolding flags' HELP names the engine shipped by default — a
	// registry fact; the value itself is resolved where each command runs.
	for _, f := range []*pflag.Flag{configCreateCmd.Flags().Lookup("engine"), manageInstallCmd.Flags().Lookup("engine")} {
		f.DefValue = operations.DefaultEngineName(reg)
	}
	llmCreateCmd.Long = `Create a NEW labeled LLM engine config under the 'llm.configs' key of
.ctxloom/config.yaml (or ~/.ctxloom/config.yaml with --home). Refuses a label that already names a config entry OR a
registered backend (` + engines + `) — change an
existing one with 'ctxloom llm edit'.

` + llmWriteLong(reg)
	llmCreateCmd.Example = `  ctxloom llm create big --type claude-code --model opus
  ctxloom llm create fast --type claude-code --permissions bypass
  ctxloom llm create qwen --home --type opencode --model qwen3.8-27b --set context_window=131072 --set provider.base_url=http://192.168.1.20:8081/v1 --set provider.output_limit=32768`

	llmEditCmd.Long = `Change an EXISTING labeled LLM engine config. Refuses a label neither
config.yaml nor a registered backend defines — create one with 'ctxloom llm
create'. A registered backend name with no config.yaml entry yet (e.g.
"claude-code") may still be edited: that is how you turn a built-in into an
explicit entry.

Only the flags you pass are applied; every unnamed field keeps its current
value.

` + llmWriteLong(reg)
	llmEditCmd.Example = `  ctxloom llm edit big --model o1-pro
  ctxloom llm edit big --permissions plan
  ctxloom llm edit qwen --home --set provider.header_timeout_ms=0
  ctxloom llm edit qwen --home --set model_options.chat_template_kwargs.enable_thinking=false`

	for _, c := range []*cobra.Command{llmCreateCmd, llmEditCmd} {
		if f := c.Flags().Lookup("type"); f != nil {
			f.Usage = "backend discriminator: " + engines + " (empty = " + operations.DefaultEngineName(reg) + ")"
		}
	}
	if f := initCmd.Flags().Lookup("engine"); f != nil {
		f.Usage = "Pre-select AI engine (" + engines + ")"
	}
}
