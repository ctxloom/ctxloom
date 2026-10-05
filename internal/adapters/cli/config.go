package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// configCmd is the top-level home of ctxloom configuration; the old `manage
// config *` path was removed, not kept as an alias.
//
// Bare `ctxloom config` shows the configuration: config is a singleton, not
// a collection, so the read-one view IS the default — there is no `list` to
// default to.
var configCmd = groupNodeDefault(&cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration",
	Long:  `Show or modify ctxloom configuration.`,
	Example: `  ctxloom config show              # Show the effective configuration
  ctxloom config show --raw        # Show only what the configuration sets
  ctxloom config show llm          # Show one section
  ctxloom config edit              # Open config.yaml in $EDITOR
  ctxloom config create            # Scaffold a default config.yaml`,
}, "show")

// configShowLong is configShowCmd's Long text. It deliberately does not
// enumerate section names: a wrong section name gets the true list from
// resolveConfigSection itself, which reads it off the rendered document.
const configShowLong = `Show the effective configuration, or one section of it.

Bare, prints the whole document. With a section, prints only that top-level
section; an unknown section is refused with the list of available ones.`

var configShowCmd = &cobra.Command{
	Use:   "show [section]",
	Short: "Show the effective configuration, or one section",
	Long:  configShowLong,
	Example: `  ctxloom config show
  ctxloom config show llm`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigShow,
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	doc, err := configDocument()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		payload, err := configPayload(doc)
		if err != nil {
			return err
		}
		return emit(cmd, payload, func() error { return renderConfigYAML(doc, cmd.OutOrStdout()) })
	}
	section, err := resolveConfigSection(doc, args[0])
	if err != nil {
		return err
	}
	payload, err := configPayload(section)
	if err != nil {
		return err
	}
	return emit(cmd, payload, func() error { return renderConfigSection(doc, args[0], cmd.OutOrStdout()) })
}

// configRawHelp is the --raw flag's usage.
const configRawHelp = "Show only what the configuration sets, without the shipped default engine registry"

// configDocument loads the config and picks the view show renders: the
// effective configuration, or with --raw the authored one a save writes. Both
// marshal through the same serializer.
func configDocument() (yaml.Marshaler, error) {
	cfg, err := GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if configRaw {
		return cfg.Authored(), nil
	}
	return cfg, nil
}

// configPayload re-expresses a config value as a plain map/slice/scalar tree by
// round-tripping it through its OWN yaml encoding, so every --format encoding
// carries the same keys `config show` has always printed.
//
// The round-trip is load-bearing, not ceremony: Config's fields are all
// unexported and it renders through a custom MarshalYAML, so handing the struct
// straight to a reflective or json encoder yields "{}" — a zero-byte payload
// with a 0 exit, which is exactly the failure the format contract exists to
// prevent. The section values `config show <section>` returns carry yaml tags but no json
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

// renderConfigYAML marshals doc to YAML and writes it to out. Extracted
// from configShowCmd's RunE so the marshal + write composition is
// testable without invoking cobra.
func renderConfigYAML(doc yaml.Marshaler, out io.Writer) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	_, err = out.Write(data)
	return err
}

// resolveConfigSection returns the named top-level section of doc, or an
// error whose message lists the valid section names.
//
// The valid sections are not a second, hand-maintained list: they are read
// off doc's own MarshalYAML document — the SAME configDoc value `config show`
// marshals to render the whole configuration (renderConfigYAML calls
// yaml.Marshal(doc), which yaml.v3 routes through this exact Marshaler). A
// field reflected out of that document by its yaml tag is returned as-is, so
// adding a section to configDoc makes it showable both whole and by section
// in one edit — there is no second list to fall behind.
func resolveConfigSection(doc yaml.Marshaler, name string) (any, error) {
	rendered, err := doc.MarshalYAML()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	v := reflect.ValueOf(rendered)
	t := v.Type()
	available := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tagName, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if tagName == "" || tagName == "-" {
			continue
		}
		if tagName == name {
			return v.Field(i).Interface(), nil
		}
		available = append(available, tagName)
	}
	return nil, fmt.Errorf("unknown section: %s\n\nAvailable: %s", name, strings.Join(available, ", "))
}

// renderConfigSection resolves the named section and writes it to out as
// YAML, so the resolve + marshal + write composition is testable without
// invoking cobra.
func renderConfigSection(doc yaml.Marshaler, name string, out io.Writer) error {
	data, err := resolveConfigSection(doc, name)
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
	Use:     "edit",
	Short:   "Open config.yaml in $EDITOR",
	Example: `  ctxloom config edit`,
	Args:    cobra.NoArgs,
	RunE:    runConfigEdit,
}

func runConfigEdit(cmd *cobra.Command, _ []string) error {
	path := projectConfigPath()
	exists, err := configFileExists(afero.NewOsFs(), path)
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
func configFileExists(fsys afero.Fs, path string) (bool, error) {
	if _, err := fsys.Stat(path); err != nil {
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
	Use:     "create",
	Short:   "Scaffold a default config.yaml (and remotes.yaml)",
	Long:    configCreateLong,
	Example: `  ctxloom config create --engine claude-code`,
	Args:    cobra.NoArgs,
	RunE:    runConfigCreate,
}

func runConfigCreate(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	path := paths.ConfigPath(appDir)
	exists, err := configFileExists(afero.NewOsFs(), path)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("config already exists: %s", path)
	}
	engine := configCreateEngine
	if engine == "" {
		// The flag's default is a registry fact: the engine shipped by
		// default, resolved here rather than spelled at declaration.
		engine = operations.DefaultEngineName(App().Engines())
	}
	if _, err := operations.InitializeProject(cmd.Context(), App().Engines(), operations.InitializeProjectRequest{
		AppDir: appDir,
		Engine: engine,
	}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", path)
	return nil
}

var (
	configCreateEngine string
	// configRaw is --raw on show.
	configRaw bool
)

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
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configCreateCmd)
	// Empty means the engine shipped by default, resolved at run time from
	// the registry (runConfigCreate); flags are declared at init, before
	// any engine is registered, and the help names the default once it is.
	configCreateCmd.Flags().StringVar(&configCreateEngine, "engine", "", "AI engine to record in the scaffolded config")
	configShowCmd.Flags().BoolVar(&configRaw, "raw", false, configRawHelp)
}
