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
			fmt.Println(cfg.GetDefaultLLMPlugin())
			return nil
		}

		name := args[0]

		if !isKnownPlugin(cfg, name) {
			available := availablePluginNames(cfg)
			return fmt.Errorf("unknown plugin %q; available: %s", name, strings.Join(available, ", "))
		}

		res, err := operations.SetDefaultPlugin(cmd.Context(), cfg, operations.SetDefaultPluginRequest{Name: name})
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
