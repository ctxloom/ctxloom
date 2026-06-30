package cmd

import (
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
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

	res, err := operations.ListFragments(cmd.Context(), cfg, operations.ListFragmentsRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, f := range res.Fragments {
		names = append(names, f.Name)
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeProfileNames returns a completion function for profile names.
func completeProfileNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	res, err := operations.ListProfiles(cmd.Context(), cfg, operations.ListProfilesRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, p := range res.Profiles {
		names = append(names, p.Name)
	}

	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeLLMNames completes the --llm flag with config labels (the keys under
// llm.configs). If the config can't be loaded, it falls back to registered
// backend names.
func completeLLMNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load()
	if err != nil {
		return filterPrefix(backends.List(), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	labels := make([]string, 0, len(cfg.LM.Configs))
	for label := range cfg.LM.Configs {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return filterPrefix(labels, toComplete), cobra.ShellCompDirectiveNoFileComp
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

	if res, err := operations.ListProfiles(cmd.Context(), cfg, operations.ListProfilesRequest{}); err == nil {
		for _, p := range res.Profiles {
			for _, tag := range p.Tags {
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

	res, err := operations.ListSkills(cmd.Context(), cfg, operations.ListSkillsRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	for _, p := range res.Skills {
		names = append(names, p.Name)
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
