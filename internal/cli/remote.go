package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var remoteCmd = groupNode(&cobra.Command{
	Use:   "remote",
	Short: "Manage remotes and discover content",
	Long: `Manage remote sources and discover bundles/profiles.

Remote sources are Git repositories (GitHub or generic git) containing shared
bundles and profiles.

Registry:
  ctxloom remote list                    List configured remotes
  ctxloom remote show <name>             Show one remote and its bundles
  ctxloom remote create <name> <url>     Register a remote
  ctxloom remote delete <name>           Delete a remote
  ctxloom remote default <name>          Set the default remote

A remote is just an address; its content takes the review path. To auto-trust a
publisher's content, trust their signing key (ctxloom trust signer create) — not
the URL.

Discovery:
  ctxloom search <query>                 Search local and remote content
  ctxloom remote discover                Find ctxloom repositories

Examples:
  ctxloom remote create alice alice/ctxloom
  ctxloom search "golang testing"
  ctxloom remote show ctxloom-default`,
})

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

var remoteDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a remote source",
	Args:  cobra.ExactArgs(1),
	RunE:  runRemoteDelete,
}

func runRemoteDelete(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	result, err := operations.RemoveRemote(cmd.Context(), cfg, operations.RemoveRemoteRequest{
		Name: args[0],
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Deleted remote '%s'\n", result.Name)
	return nil
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

// `ctxloom remote trust|untrust` are DELETED (signature-envelope spec §11).
// A remote no longer carries trust: it is just an address to fetch from.
// Trusting a publisher is `ctxloom trust signer create <principal> --key <path>`, which
// trusts a KEY (verified over the bytes) rather than a LOCATION (hash-blind).

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

var (
	remotePullForce bool
	remotePullLock  bool
)

var remotePullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull dependencies from profiles",
	Long: `Pull remote bundles and profiles referenced in your configuration.

This fetches all remote dependencies declared in your profiles, updates
the lockfile, and applies hooks.

Examples:
  ctxloom remote pull                    # Pull all dependencies
  ctxloom remote pull --force            # Re-pull even if already installed
  ctxloom remote pull --lock=false       # Skip lockfile update`,
	RunE: runRemotePull,
}

func runRemotePull(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Pulling dependencies...")

	result, err := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
		Force:      remotePullForce,
		Lock:       remotePullLock,
		ApplyHooks: true,
	})
	if err != nil {
		return err
	}

	renderPullSummary(cmd.OutOrStdout(), result)
	return pullResultErr(result)
}

// pullResultErr fixes the bug where `remote pull` used to exit 0 even when
// dependencies failed or were retracted — the failures were printed to
// stdout by renderPullSummary above, but RunE always returned nil regardless,
// so a caller scripting on exit code (not scraping stdout text) saw success.
// Skipped items are NOT a failure (pull never moves an existing pin, by
// design — see renderPullSummary's doc comment); Errors and Retracted are.
func pullResultErr(result *operations.SyncDependenciesResult) error {
	// Retracted is NOT a failure: it is the retraction mechanism working as
	// designed — a bad dependency detected and withheld automatically while
	// the rest of the sync proceeds (see j15/j17/trust_surface's acceptance
	// journeys, whose entire narrative is "the sync still succeeds; the
	// retracted content just never reaches the user"). Only Errors (a real
	// fetch/apply failure) makes the pull itself fail.
	if result.Errors == 0 {
		return nil
	}
	var refs []string
	for _, item := range result.Failed {
		refs = append(refs, item.Reference)
	}
	return fmt.Errorf("remote pull: %d failed (%s)", result.Errors, strings.Join(refs, ", "))
}

// renderPullSummary prints a completed pull.
//
// The skipped line deliberately does NOT say "already installed".
// Pull installs exactly the PINNED set; moving an existing pin is `remote
// upgrade`'s job. So an item whose upstream content has changed is skipped and
// was being reported as "already installed" — which reads as "you are current"
// when you are not: upstream moved and you are still served the old content. A
// human (or an agent) reasonably concludes there is nothing to do. The line now
// says what is actually true — the pin was honored — and names the command that
// moves it.
func renderPullSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result.Total == 0 {
		fmt.Fprintln(w, "No remote dependencies to pull.")
		return
	}

	fmt.Fprintf(w, "\nPulled %d items:\n", result.Total)
	if result.Installed > 0 {
		fmt.Fprintf(w, "  Installed: %d\n", result.Installed)
	}
	if result.Updated > 0 {
		fmt.Fprintf(w, "  Updated: %d\n", result.Updated)
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(w, "  Skipped (kept at their locked commit): %d\n", len(result.Skipped))
		fmt.Fprintln(w, "    Pull never moves an existing pin, so these may have upstream changes.")
		fmt.Fprintln(w, "    Run 'ctxloom remote upgrade' to advance them.")
	}
	if len(result.Retracted) > 0 {
		fmt.Fprintf(w, "  Retracted: %d\n", len(result.Retracted))
		for _, item := range result.Retracted {
			fmt.Fprintf(w, "    - %s: retracted (%s)\n", item.Reference, item.Error)
		}
	}
	if result.Errors > 0 {
		fmt.Fprintf(w, "  Failed: %d\n", result.Errors)
		for _, item := range result.Failed {
			fmt.Fprintf(w, "    - %s: %s\n", item.Reference, item.Error)
		}
	}
}

func init() {
	rootCmd.AddCommand(remoteCmd)

	remoteCmd.AddCommand(remoteCreateCmd)
	remoteCmd.AddCommand(remoteDeleteCmd)
	remoteCmd.AddCommand(remoteListCmd)
	remoteCmd.AddCommand(remoteDefaultCmd)
	remoteCmd.AddCommand(remotePullCmd)

	remoteCreateCmd.Flags().StringVar(&remoteAddForge, "forge", "",
		"Forge to bind this remote to: github, git, or a configured forges: label (default: resolve from URL host)")

	remoteDefaultCmd.Flags().BoolVar(&remoteDefaultClear, "clear", false,
		"Clear the default remote")

	remotePullCmd.Flags().BoolVarP(&remotePullForce, "force", "f", false,
		"Re-pull even if already installed")
	remotePullCmd.Flags().BoolVar(&remotePullLock, "lock", true,
		"Update lockfile after pull")
}
