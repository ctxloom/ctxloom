package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/lm/backends"
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
			fmt.Println(cfg.LM.GetDefaultPlugin())
			return nil
		}

		name := args[0]

		if !isKnownPlugin(cfg, name) {
			available := availablePluginNames(cfg)
			return fmt.Errorf("unknown plugin %q; available: %s", name, strings.Join(available, ", "))
		}

		current := cfg.LM.GetDefaultPlugin()
		if current == name {
			fmt.Printf("Default plugin is already %s\n", name)
			return nil
		}

		cfg.LM.SetDefaultPlugin(name)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Default plugin set to: %s\n", name)
		return nil
	},
}

// isKnownPlugin checks if a plugin is registered as a built-in or found as an external plugin.
func isKnownPlugin(cfg *config.Config, name string) bool {
	if backends.Exists(name) {
		return true
	}
	for _, p := range findExternalPlugins(cfg.GetPluginPaths()) {
		if p.name == name {
			return true
		}
	}
	return false
}

// availablePluginNames returns a sorted list of all known plugin names.
func availablePluginNames(cfg *config.Config) []string {
	names := backends.List()
	for _, p := range findExternalPlugins(cfg.GetPluginPaths()) {
		names = append(names, p.name)
	}
	sort.Strings(names)
	return names
}

func init() {
	pluginCmd.AddCommand(pluginDefaultCmd)
}
