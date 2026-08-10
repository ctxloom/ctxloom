package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Bare `ctxloom remote` lists the configured remotes: the registry is the one
// thing the noun is about, and reading it touches nothing.
var remoteCmd = groupNodeDefault(&cobra.Command{
	Use:   "remote",
	Short: "Register and browse the sources content comes from",
	Long: `Register the Git repositories (GitHub or generic git) this project draws shared
bundles from, and browse what they publish.

A remote is an ADDRESS. Registering one is local bookkeeping over
.ctxloom/remotes.yaml — no fetch, no credential, nothing installed.

Registry:
  ctxloom remote list                    List configured remotes
  ctxloom remote show <name>             Show one remote and the bundles it publishes
  ctxloom remote create <name> <url>     Register a remote
  ctxloom remote remove <name> --yes     Remove a remote
  ctxloom remote default <name>          Set the default remote

Discovery:
  ctxloom search <query>                 Search local and remote content
  ctxloom remote discover                Find ctxloom repositories

What this project has INSTALLED from those remotes is the other noun:
'ctxloom deps --help'.

A remote carries no trust: its content takes the review path whatever address
it came from. To auto-trust a publisher's content, trust their signing key
('ctxloom signer trust') — a key is verified over the bytes, a URL is not.

Examples:
  ctxloom remote create alice alice/ctxloom
  ctxloom search "golang testing"
  ctxloom remote show ctxloom-default`,
}, "list")

var remoteAddForge string

var remoteCreateCmd = &cobra.Command{
	Use:   "create <name> <url>",
	Short: "Register a remote source",
	Long: `Register a remote repository as a source for fragments and prompts.

URL formats:
  alice/ctxloom                      GitHub shorthand (expands to https://github.com/alice/ctxloom)
  https://github.com/alice/ctxloom   Full GitHub URL
  https://git.example.com/corp/ctxloom   Generic git host URL
  git@github.com:alice/ctxloom.git   SSH URL (converted to HTTPS)

Forge selection:
  Without --forge, the forge resolves from the URL host: github.com (and the
  owner/repo shorthand) use the rich GitHub adapter; every other host uses the
  generic git adapter (clone + local read, ambient git auth). Pass --forge to
  override — "github", "git", or the label of a forges: entry (e.g. a GitHub
  Enterprise instance).

Examples:
  ctxloom remote create alice alice/ctxloom
  ctxloom remote create corp https://git.example.com/corp/ctxloom
  ctxloom remote create corp https://git.example.com/corp/ctxloom --forge git
  ctxloom remote create work https://github.mycorp.com/me/ctxloom --forge work-ghe`,
	Args: cobra.ExactArgs(2),
	RunE: runRemoteCreate,
}

func runRemoteCreate(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	result, err := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
		Name:  args[0],
		URL:   args[1],
		Forge: remoteAddForge,
	})
	if err != nil {
		return err
	}

	if result.Warning != "" {
		clidiag.Warn("ctxloom", "%s", result.Warning)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Added remote '%s' → %s\n", result.Name, result.URL)
	return nil
}

var remoteRemoveYes bool

var remoteRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a remote source",
	Long: `Remove a remote source from the registry.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it. This only unregisters the remote — it never touches
content already pulled from it.`,
	Args: cobra.ExactArgs(1),
	RunE: runRemoteRemove,
}

func runRemoteRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	applyCmd := fmt.Sprintf("ctxloom remote remove %s --yes", name)
	if !remoteRemoveYes {
		list, err := operations.ListRemotes(cmd.Context(), cfg, operations.ListRemotesRequest{})
		if err != nil {
			return err
		}
		found := false
		for _, r := range list.Remotes {
			if r.Name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no remote named %q", name)
		}
		target := fmt.Sprintf("remote %q", name)
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}

	result, err := operations.RemoveRemote(cmd.Context(), cfg, operations.RemoveRemoteRequest{
		Name: name,
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed remote '%s'\n", result.Name)
		return nil
	})
}

var remoteListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured remotes",
	RunE:    runRemoteList,
}

func runRemoteList(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	result, err := operations.ListRemotes(cmd.Context(), cfg, operations.ListRemotesRequest{})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error {
		return renderRemoteList(cmd.OutOrStdout(), result)
	})
}

// renderRemoteList writes the human listing: the empty-store guidance, or one
// row per remote with the default marked. Split out of the RunE so the
// rendering can be driven from a value, without a project or a config load.
func renderRemoteList(out io.Writer, result *operations.ListRemotesResult) error {
	if result.Count == 0 {
		fmt.Fprintln(out, "No remotes configured.")
		fmt.Fprintln(out, "Use 'ctxloom remote create <name> <url>' to add a remote.")
		fmt.Fprintln(out, "Use 'ctxloom remote discover' to find public repositories.")
		return nil
	}

	fmt.Fprintln(out, "Configured remotes:")
	for _, r := range result.Remotes {
		var marks string
		if r.Name == result.Default {
			marks += " (default)"
		}
		fmt.Fprintf(out, "  %-15s %s%s\n", r.Name, r.URL, marks)
	}
	return nil
}

// A remote carries no trust of its own — it is an address to fetch from, and
// nothing more. Trusting a publisher is `ctxloom signer trust <principal> --key
// <path>`, which trusts a KEY, verified over the bytes, rather than a LOCATION,
// which is hash-blind. Publishing needs no separate blessing either: registering
// a remote is the act that names it as a destination.
//
// Which remotes a caller may PUBLISH to is therefore exactly which remotes are
// registered here.

var remoteDefaultCmd = &cobra.Command{
	Use:   "default <name>",
	Short: "Set the default remote",
	Long: `Set the default remote for push operations.

The current default is shown by 'ctxloom remote list' (marked "(default)").
Use --clear to remove the default.

Examples:
  ctxloom remote default ctxloom-default   # Set default to ctxloom-default
  ctxloom remote default --clear           # Clear the default`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRemoteDefault,
}

var remoteDefaultClear bool

func runRemoteDefault(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Clear the default.
	if remoteDefaultClear {
		if _, err := operations.SetDefaultRemote(cmd.Context(), cfg, operations.DefaultRemoteRequest{Name: ""}); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Cleared default remote.")
		return nil
	}

	// A name is required to set the default; the current default is visible via
	// `ctxloom remote list`.
	if len(args) == 0 {
		return fmt.Errorf("remote name required (or use --clear); see the current default in 'ctxloom remote list'")
	}

	// Set a new default.
	name := args[0]
	if _, err := operations.SetDefaultRemote(cmd.Context(), cfg, operations.DefaultRemoteRequest{Name: name}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Set default remote to: %s\n", name)
	return nil
}

func init() {
	rootCmd.AddCommand(remoteCmd)

	remoteCmd.AddCommand(remoteCreateCmd)
	remoteCmd.AddCommand(remoteRemoveCmd)
	remoteCmd.AddCommand(remoteListCmd)
	remoteCmd.AddCommand(remoteDefaultCmd)

	remoteCreateCmd.Flags().StringVar(&remoteAddForge, "forge", "",
		"Forge to bind this remote to: github, git, or a configured forges: label (default: resolve from URL host)")

	remoteRemoveCmd.Flags().BoolVarP(&remoteRemoveYes, "yes", "y", false,
		"Apply the removal this invocation would report (default: report only)")

	remoteDefaultCmd.Flags().BoolVar(&remoteDefaultClear, "clear", false,
		"Clear the default remote")

}
