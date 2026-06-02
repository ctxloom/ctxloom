package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var pluginDefaultCmd = &cobra.Command{
	Use:   "default [name]",
	Short: "Show or set the default plugin",
	Long: `Show or set the default AI backend plugin.

Without arguments, prints the current default plugin name.
With a plugin name argument, sets that plugin as the default.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completePluginNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(args) == 0 {
			fmt.Println(cfg.GetDefaultLLM())
			return nil
		}

		name := args[0]

		if !isKnownPlugin(cfg, name) {
			available := availablePluginNames(cfg)
			return fmt.Errorf("unknown plugin %q; available: %s", name, strings.Join(available, ", "))
		}

		res, err := operations.SetDefaultLLM(cmd.Context(), cfg, operations.SetDefaultLLMRequest{Name: name})
		if err != nil {
			return err
		}
		if res.Status == "unchanged" {
			fmt.Printf("Default plugin is already %s\n", name)
			return nil
		}
		fmt.Printf("Default plugin set to: %s\n", name)
		return nil
	},
}

// isKnownPlugin checks if a plugin is a registered built-in or has a config entry.
func isKnownPlugin(cfg *config.Config, name string) bool {
	if backends.Exists(name) {
		return true
	}
	_, ok := cfg.LM.Configs[name]
	return ok
}

// availablePluginNames returns a sorted list of all known plugin names:
// registered built-ins plus any with an explicit config entry.
func availablePluginNames(cfg *config.Config) []string {
	seen := map[string]bool{}
	var names []string
	for _, n := range backends.List() {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range cfg.LM.Configs {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func init() {
	pluginCmd.AddCommand(pluginDefaultCmd)
}
