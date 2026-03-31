package cmd

import (
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

var completionCmd = &cobra.Command{
	Use:    "completion [bash|zsh|fish|powershell]",
	Short:  "Generate shell completion scripts",
	Hidden: true, // Available but not shown in main help
	Long: `Generate shell completion scripts for ctxloom.

To load completions:

Bash:
  $ source <(ctxloom completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ ctxloom completion bash > /etc/bash_completion.d/ctxloom
  # macOS:
  $ ctxloom completion bash > $(brew --prefix)/etc/bash_completion.d/ctxloom

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ ctxloom completion zsh > "${fpath[1]}/_ctxloom"

  # You will need to start a new shell for this setup to take effect.

Fish:
  $ ctxloom completion fish | source

  # To load completions for each session, execute once:
  $ ctxloom completion fish > ~/.config/fish/completions/ctxloom.fish

PowerShell:
  PS> ctxloom completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> ctxloom completion powershell > ctxloom.ps1
  # and source this file from your PowerShell profile.
`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return cmd.Root().GenBashCompletion(os.Stdout)
		case "zsh":
			return cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			return cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
		return nil
	},
}


// completeFragmentNames returns a completion function for fragment names.
func completeFragmentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	infos, err := loader.ListAllFragments()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeProfileNames returns a completion function for profile names.
func completeProfileNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	loader := profiles.NewLoader(profileDirs)
	profileList, err := loader.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, p := range profileList {
		names = append(names, p.Name)
	}

	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completePluginNames returns a completion function for plugin names.
func completePluginNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	plugins := backends.List()
	return filterPrefix(plugins, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeTagNames returns a completion function for tag names.
// Note: This requires loading fragment files so it may be slow with many fragments.
// For now, return common tags from config profiles as a fast approximation.
func completeTagNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Collect tags from profile definitions (fast - already in memory)
	tagSet := make(map[string]bool)

	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) > 0 {
		loader := profiles.NewLoader(profileDirs)
		profileList, _ := loader.List()
		for _, profile := range profileList {
			for _, tag := range profile.Tags {
				tagSet[tag] = true
			}
		}
	}

	var tags []string
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	return filterPrefix(tags, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completePromptNames returns a completion function for prompt names.
func completePromptNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	infos, err := loader.ListAllPrompts()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// filterPrefix returns only strings that start with the given prefix.
func filterPrefix(items []string, prefix string) []string {
	if prefix == "" {
		return items
	}
	var result []string
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
