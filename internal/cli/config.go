package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// configCmd is the top-level home of ctxloom configuration; the old `manage
// config *` path was removed, not kept as an alias.
var configCmd = groupNode(&cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration",
	Long: `Show or modify ctxloom configuration.

Examples:
  ctxloom config show              # Show full configuration
  ctxloom config get defaults      # Get a specific section
  ctxloom config edit              # Open config.yaml in $EDITOR
  ctxloom config create            # Scaffold a default config.yaml`,
})

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show full configuration",
	RunE:  runConfigShow,
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	payload, err := configPayload(cfg)
	if err != nil {
		return err
	}
	return emit(cmd, payload, func() error { return renderConfigYAML(cfg, cmd.OutOrStdout()) })
}

// configPayload re-expresses a config value as a plain map/slice/scalar tree by
// round-tripping it through its OWN yaml encoding, so every --format encoding
// carries the same keys `config show` has always printed.
//
// The round-trip is load-bearing, not ceremony: Config's fields are all
// unexported and it renders through a custom MarshalYAML, so handing the struct
// straight to a reflective or json encoder yields "{}" — a zero-byte payload
// with a 0 exit, which is exactly the failure the format contract exists to
// prevent. The section values `config get` returns carry yaml tags but no json
// tags, and would otherwise render Go field names in json/toml while yaml kept
// snake_case.
func configPayload(v any) (any, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	var payload any
	if err := yaml.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("failed to re-read marshaled config: %w", err)
	}
	return payload, nil
}

// configGetLong is configGetCmd's Long text.
const configGetLong = `Get a specific configuration section.

Available sections:
  config      Behavioral settings (use_distilled, compaction_chunks)
  llm         Language model configuration (labeled configs + role map)
  mcp         MCP server configuration
  profiles    Profile defaults and definitions`

var configGetCmd = &cobra.Command{
	Use:   "get <section>",
	Short: "Get a configuration section",
	Long:  configGetLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigGet,
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	section, err := resolveConfigSection(cfg, args[0])
	if err != nil {
		return err
	}
	payload, err := configPayload(section)
	if err != nil {
		return err
	}
	return emit(cmd, payload, func() error { return renderConfigSection(cfg, args[0], cmd.OutOrStdout()) })
}

// renderConfigYAML marshals cfg to YAML and writes it to out. Extracted
// from configShowCmd's RunE so the marshal + write composition is
// testable without invoking cobra.
func renderConfigYAML(cfg *config.Config, out io.Writer) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	_, err = out.Write(data)
	return err
}

// resolveConfigSection returns the named top-level section of cfg, or an
// error whose message lists the valid section names. The switch is the
// load-bearing surface — adding a new section here is the only place
// the `config get <section>` CLI surface changes.
func resolveConfigSection(cfg *config.Config, name string) (any, error) {
	switch name {
	case "config":
		return cfg.GetSettings(), nil
	case "llm":
		return cfg.GetLMConfig(), nil
	case "mcp":
		return cfg.GetMCPConfig(), nil
	case "profiles":
		return cfg.GetProfilesConfig(), nil
	default:
		return nil, fmt.Errorf("unknown section: %s\n\nAvailable: config, llm, mcp, profiles", name)
	}
}

// renderConfigSection resolves the named section and writes it to out as
// YAML. Extracted from configGetCmd's RunE.
func renderConfigSection(cfg *config.Config, name string, out io.Writer) error {
	data, err := resolveConfigSection(cfg, name)
	if err != nil {
		return err
	}
	output, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal section: %w", err)
	}
	_, err = out.Write(output)
	return err
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open config.yaml in $EDITOR",
	Args:  cobra.NoArgs,
	RunE:  runConfigEdit,
}

func runConfigEdit(cmd *cobra.Command, _ []string) error {
	path := projectConfigPath()
	exists, err := configFileExists(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("no config at %s — run 'ctxloom config create' first", path)
	}
	return openInEditor(path)
}

// configFileExists reports whether path is there, keeping "it is genuinely
// absent" separate from "the answer is unknown". Both config commands hinge on
// that answer — edit refuses to run without a config, init refuses to
// overwrite one — and os.Stat has a third outcome besides yes and no: a
// permission-denied parent, a non-directory path component, a symlink loop. A
// boolean reading of Stat resolves every one of those to the WRONG branch
// (edit launches $EDITOR on a path it could not read; init proceeds as if the
// config it must not clobber were absent), so an inconclusive stat is reported,
// never guessed.
func configFileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cannot determine whether %s exists: %w", path, err)
	}
	return true, nil
}

// configCreateLong is configCreateCmd's Long text.
const configCreateLong = `Write a default config.yaml AND a default remotes.yaml into the project
.ctxloom directory.

Refuses to overwrite an existing config.yaml, but the accompanying remotes.yaml
is (re)written with defaults — back up a customized remotes.yaml first. For a
fuller project scaffold (hooks, discovery), use 'ctxloom init'.`

var configCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold a default config.yaml (and remotes.yaml)",
	Long:  configCreateLong,
	Args:  cobra.NoArgs,
	RunE:  runConfigCreate,
}

func runConfigCreate(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	path := paths.ConfigPath(appDir)
	exists, err := configFileExists(path)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("config already exists: %s", path)
	}
	if _, err := operations.InitializeProject(cmd.Context(), operations.InitializeProjectRequest{
		AppDir: appDir,
		Engine: configCreateEngine,
	}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", path)
	return nil
}

var configCreateEngine string

// projectConfigPath returns the path to the project's config.yaml.
func projectConfigPath() string {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return paths.ConfigPath(config.AppDirName)
	}
	return paths.ConfigPath(appDir)
}

// openInEditor launches the environment-resolved editor (VISUAL → EDITOR →
// nano; multi-word values are split) on path, wired to the current terminal.
// This command edits the config file itself — possibly a broken one — so it
// must not depend on config load; it uses the shared env-only half of the
// editor policy instead of cfg.GetEditorCommand. (The last-resort fallback
// changed from vi to nano to match the config-aware path.)
func openInEditor(path string) error {
	editor, args := config.EditorFromEnv()
	c := exec.Command(editor, append(args, path)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func init() {
	// Top-level: `manage config` used to be the only home; it has since been
	// removed in favor of this command.
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configCreateCmd)
	configCreateCmd.Flags().StringVar(&configCreateEngine, "engine", "claude-code", "AI engine to record in the scaffolded config")
}
