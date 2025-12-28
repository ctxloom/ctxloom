package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/lm/backends"
)

// pluginPrefix is the naming convention for external plugin binaries.
const pluginPrefix = "scm-plugin-"

var pluginListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available plugins",
	Long:    `Lists all available AI backend plugins, both built-in and external.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get built-in backends
		builtinNames := backends.List()
		sort.Strings(builtinNames)

		// Get external plugins from plugin paths
		cfg, err := config.Load()
		if err != nil {
			// If config fails, just show built-in plugins
			fmt.Println("Built-in plugins:")
			for _, name := range builtinNames {
				fmt.Printf("  %s\n", name)
			}
			return nil
		}

		// Find external plugins
		externalPlugins := findExternalPlugins(cfg.GetPluginPaths())

		// Print built-in plugins
		fmt.Println("Built-in plugins:")
		for _, name := range builtinNames {
			fmt.Printf("  %s\n", name)
		}

		// Print external plugins if any
		if len(externalPlugins) > 0 {
			fmt.Println("\nExternal plugins:")
			for _, p := range externalPlugins {
				fmt.Printf("  %s (%s)\n", p.name, p.path)
			}
		}

		return nil
	},
}

type externalPlugin struct {
	name string
	path string
}

// findExternalPlugins searches for plugin binaries in the given paths.
func findExternalPlugins(paths []string) []externalPlugin {
	var plugins []externalPlugin
	seen := collections.NewSet[string]()

	for _, dir := range paths {
		dir = expandTilde(dir)

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if strings.HasPrefix(name, pluginPrefix) {
				pluginName := strings.TrimPrefix(name, pluginPrefix)
				if !seen.Has(pluginName) {
					seen.Add(pluginName)
					plugins = append(plugins, externalPlugin{
						name: pluginName,
						path: filepath.Join(dir, name),
					})
				}
			}
		}
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].name < plugins[j].name
	})

	return plugins
}

func init() {
	pluginCmd.AddCommand(pluginListCmd)
}
