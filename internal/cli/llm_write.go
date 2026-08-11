package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// `llm create`/`llm edit` — the write half of the parity gap with `agent`
// (agent.go's agentCreateCmd/agentEditCmd/writeAgentBinding is the template
// this mirrors). agent create --engine <label> draws from a vocabulary this
// closes the gap on: previously enumerable (`llm list`) but not manageable.

var (
	llmSetType        string
	llmSetModel       string
	llmSetPermissions string
	llmSetEnvFile     string
)

// llmWriteLong is the shared body for `llm create`/`llm edit`: the fields an
// entry carries are identical either way.
const llmWriteLong = `--type is the backend discriminator (claude-code|codex|kiro|...);
omit it to keep claude-code's default. --model sets the model string. --permissions
sets the launch-time posture (default|acceptEdits|plan|bypass).

CREDENTIALS ARE WITHHELD. --env-file reads KEY=VALUE lines (one per line;
blank lines and #-comment lines are skipped) from a file, or from stdin with
"-", and REPLACES the entry's entire declared env block — there is no
--env flag, and no other way to set one: a credential must never travel
through argv, where it would reach shell history, the process table, and
any CI log capturing the command line. The values themselves are written
straight to your PER-MACHINE user config (~/.ctxloom/config.yaml, never a
committed project file — llm.configs.*.env is machine-scoped by design).
'llm list' and this command's own confirmation report only which keys are
declared, never their values.`

var llmCreateCmd = &cobra.Command{
	Use:   "create <label>",
	Short: "Create a new LLM engine config",
	Long: `Create a NEW labeled LLM engine config under the 'llm.configs' key of
.ctxloom/config.yaml. Refuses a label that already names a config entry OR a
registered backend (claude-code, codex, kiro, ...) — change an
existing one with 'ctxloom llm edit'.

` + llmWriteLong + `

Examples:
  ctxloom llm create big --type codex --model o1
  ctxloom llm create fast --type claude-code --permissions bypass`,
	Args: cobra.ExactArgs(1),
	RunE: runLLMCreate,
}

var llmEditCmd = &cobra.Command{
	Use:   "edit <label>",
	Short: "Edit an existing LLM engine config",
	Long: `Change an EXISTING labeled LLM engine config. Refuses a label neither
config.yaml nor a registered backend defines — create one with 'ctxloom llm
create'. A registered backend name with no config.yaml entry yet (e.g.
"claude-code") may still be edited: that is how you turn a built-in into an
explicit entry.

Only the flags you pass are applied; every unnamed field keeps its current
value.

` + llmWriteLong + `

Examples:
  ctxloom llm edit big --model o1-pro
  ctxloom llm edit big --env-file secrets.env`,
	Args: cobra.ExactArgs(1),
	RunE: runLLMEdit,
}

func runLLMCreate(cmd *cobra.Command, args []string) error { return writeLLM(cmd, args[0], false) }
func runLLMEdit(cmd *cobra.Command, args []string) error   { return writeLLM(cmd, args[0], true) }

// writeLLM is `llm create`/`llm edit`'s shared body — mirrors
// agent.go's writeAgentBinding.
func writeLLM(cmd *cobra.Command, label string, mustExist bool) error {
	cfg, err := GetConfigForUpdate()
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

	entry, err := operations.SetLLM(config.NewManager(), req)
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
// labels) — the exact set `agent create --engine`/`llm default` already
// accept, so a bare backend name like "claude-code" counts as existing even
// with no config.yaml entry (create refuses it — it would shadow the
// built-in; edit accepts it — that is how a built-in becomes explicit).
func checkLLMExistence(cfg *config.Config, label string, mustExist bool) error {
	exists := false
	for _, n := range operations.AvailableLLMNames(cfg) {
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
	if cmd.Flags().Changed("env-file") {
		env, err := readLLMEnvFile(cmd, llmSetEnvFile)
		if err != nil {
			return operations.SetLLMRequest{}, err
		}
		req.Env = env
	}
	return req, nil
}

// readLLMEnvFile reads KEY=VALUE lines from path, or from cmd's stdin when
// path is "-" — signer trust's "-" convention for keeping a secret out of
// argv. This is llm create/edit's ONLY way to set an entry's env block.
func readLLMEnvFile(cmd *cobra.Command, path string) (map[string]string, error) {
	var r io.Reader
	if path == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(path) //nolint:gosec // an operator-supplied --env-file path
		if err != nil {
			return nil, fmt.Errorf("--env-file %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	env, err := parseEnvFile(r)
	if err != nil {
		return nil, fmt.Errorf("--env-file %s: %w", path, err)
	}
	return env, nil
}

// parseEnvFile parses dotenv-shaped KEY=VALUE lines: blank lines and lines
// starting with "#" are skipped; every other line must contain "=" (the
// first "=" splits key from value, so a value may itself contain "=", e.g.
// a base64-encoded secret). Leading/trailing whitespace around the key and
// value is trimmed.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid line %q (want KEY=VALUE)", line)
		}
		env[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

// renderLLMWritten writes the one-line confirmation for a created/edited
// llm, naming which of the two happened. EnvKeys — key NAMES only, never
// values, see operations.LLMEntry's doc — is the only env-related thing
// this ever prints: a credential's presence is confirmed, its value never
// is.
func renderLLMWritten(out io.Writer, entry *operations.LLMEntry, edited bool) error {
	w := iox.NewErrWriter(out)
	verb := "Created"
	if edited {
		verb = "Updated"
	}
	typ := entry.Type
	if typ == "" {
		typ = config.DefaultLLM
	}
	w.Printf("%s llm %q (type: %s", verb, entry.Label, typ)
	if entry.Model != "" {
		w.Printf(", model: %s", entry.Model)
	}
	if entry.Permissions != "" {
		w.Printf(", permissions: %s", entry.Permissions)
	}
	if len(entry.EnvKeys) > 0 {
		w.Printf(", env: %s", strings.Join(entry.EnvKeys, ", "))
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
	cmd.Flags().StringVar(&llmSetType, "type", "", "backend discriminator: claude-code|codex|kiro|... (empty = claude-code)")
	cmd.Flags().StringVar(&llmSetModel, "model", "", "model string")
	cmd.Flags().StringVar(&llmSetPermissions, "permissions", "", "permission posture: default|acceptEdits|plan|bypass")
	cmd.Flags().StringVar(&llmSetEnvFile, "env-file", "", "read KEY=VALUE env/credential lines from this file ('-' for stdin); REPLACES the entry's whole env block")
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return backends.List(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
}
