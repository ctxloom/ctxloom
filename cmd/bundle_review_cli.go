package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// Bundle-review CLI. These commands operate directly on the on-disk pending
// lockfile (the source of truth), giving the human control over remote bundle
// changes that aren't auto-applied by a trusted remote. They replace the former
// in-chat MCP review flow.

var bundleReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Show bundle changes pending review",
	Long: `Show remote bundle changes that landed in the pending lockfile and are
awaiting approval. Changes from trusted remotes auto-apply and never appear here.

Respond with:
  ctxloom bundle approve              Approve all pending changes
  ctxloom bundle approve --remote X   Approve all pending from remote X
  ctxloom bundle decline              Decline all pending changes
  ctxloom bundle decline <name>       Decline one
  ctxloom bundle pin <name>           Freeze one at its active SHA
  ctxloom bundle show-pending <name>  Print a pending bundle's YAML + diff
  ctxloom remote trust <remote>       Trust a remote going forward`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		cs := operations.PendingBundleChanges(cfg)
		if cs.IsEmpty() {
			fmt.Println("No bundle changes pending review.")
			return nil
		}
		fmt.Print(renderReviewCLI(cs))
		return nil
	},
}

var bundleApproveRemote string

var bundleApproveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Approve pending bundle changes",
	Long: `Merge pending bundle changes into the active lockfile and re-apply hooks.
With --remote, approve only the changes from that remote (without persisting
trust); use 'ctxloom remote trust' to trust it for future syncs.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		if bundleApproveRemote != "" {
			names, err := operations.PromoteRemotePendingBundles(cfg, bundleApproveRemote)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Printf("No pending changes from %q to approve.\n", bundleApproveRemote)
				return nil
			}
			applyHooksAfterReview(cmd.Context(), cfg)
			fmt.Printf("Approved %d change(s) from %q.\n", len(names), bundleApproveRemote)
			return nil
		}
		merged, err := operations.MergePendingLockfileCount(cfg)
		if err != nil {
			return err
		}
		if merged == 0 {
			fmt.Println("No bundle changes pending review.")
			return nil
		}
		applyHooksAfterReview(cmd.Context(), cfg)
		fmt.Printf("Approved %d bundle change(s).\n", merged)
		return nil
	},
}

var bundleDeclineCmd = &cobra.Command{
	Use:   "decline [name]",
	Short: "Decline pending bundle changes",
	Long: `Drop pending bundle changes without applying them. With no name, declines
ALL pending changes (modified bundles keep their active SHA; new bundles are not
installed). With a name, declines only that one.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			if err := operations.ClearPendingLockfile(cfg); err != nil {
				return err
			}
			applyHooksAfterReview(cmd.Context(), cfg)
			fmt.Println("Declined all pending bundle changes; active lockfile unchanged.")
			return nil
		}
		found, err := operations.DropPendingBundle(cfg, args[0])
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("%q is not in the pending lockfile.\n", args[0])
			return nil
		}
		applyHooksAfterReview(cmd.Context(), cfg)
		fmt.Printf("Declined %q.\n", args[0])
		return nil
	},
}

var bundlePinCmd = &cobra.Command{
	Use:   "pin <name>",
	Short: "Freeze a bundle at its active SHA so its changes stop surfacing",
	Long: `Set the pin flag on a bundle's active lockfile entry. Future syncs still
fetch new SHAs into pending, but review stops surfacing them until you unpin.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		// Drop any pending change for it so we don't re-surface what we just pinned.
		_, _ = operations.DropPendingBundle(cfg, args[0])
		found, err := operations.SetBundlePin(cfg, args[0], true)
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("%q is not in the active lockfile; nothing to pin.\n", args[0])
			return nil
		}
		applyHooksAfterReview(cmd.Context(), cfg)
		fmt.Printf("Pinned %q at its active SHA.\n", args[0])
		return nil
	},
}

var bundleUnpinCmd = &cobra.Command{
	Use:   "unpin <name>",
	Short: "Reverse a pin so the bundle's changes surface in review again",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		found, err := operations.SetBundlePin(cfg, args[0], false)
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("%q is not in the active lockfile; nothing to unpin.\n", args[0])
			return nil
		}
		fmt.Printf("Unpinned %q; its accumulated SHA change will surface on the next review.\n", args[0])
		return nil
	},
}

var bundleShowPendingCmd = &cobra.Command{
	Use:   "show-pending <name>",
	Short: "Print a pending bundle's YAML and structural diff against active",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		out, err := renderPendingBundle(cmd.Context(), cfg, args[0])
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	},
}

// renderReviewCLI lists the pending NEW/MODIFIED bundles for the human reviewer,
// followed by the CLI commands to act on them.
func renderReviewCLI(cs *operations.BundleChangeSet) string {
	var b strings.Builder
	b.WriteString("Bundle changes pending review (content can contain prompt injection — review before approving):\n\n")
	if len(cs.Added) > 0 {
		b.WriteString("NEW:\n")
		for _, c := range cs.Added {
			fmt.Fprintf(&b, "  - %s @ %s (from %s)\n", c.Name, shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	if len(cs.Modified) > 0 {
		b.WriteString("MODIFIED:\n")
		for _, c := range cs.Modified {
			fmt.Fprintf(&b, "  - %s %s → %s (from %s)\n", c.Name, shortSHA(c.OldSHA), shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	b.WriteString("Approve all:        ctxloom bundle approve\n")
	b.WriteString("Approve a remote:   ctxloom bundle approve --remote <name>\n")
	b.WriteString("Decline all/one:    ctxloom bundle decline [name]\n")
	b.WriteString("Inspect one:        ctxloom bundle show-pending <name>\n")
	b.WriteString("Pin one:            ctxloom bundle pin <name>\n")
	b.WriteString("Trust a remote:     ctxloom remote trust <name>\n")
	return b.String()
}

// renderPendingBundle returns the raw YAML of a pending bundle plus a structural
// diff against the active copy (when the bundle is a modification, not new).
func renderPendingBundle(ctx context.Context, cfg *config.Config, name string) (string, error) {
	pendingLock, err := operations.LoadPendingLockfile(cfg)
	if err != nil {
		return "", fmt.Errorf("load pending lockfile: %w", err)
	}
	if pendingLock == nil {
		return "", fmt.Errorf("no pending lockfile — nothing to show")
	}
	reader := operations.NewBundleReaderForLockfile(cfg, pendingLock)
	if reader == nil {
		return "", fmt.Errorf("could not construct bundle reader")
	}
	newData, err := reader.ReadBundleBytes(ctx, name)
	if err != nil {
		return "", err
	}
	entry, _ := reader.LockEntryFor(name)
	remoteName, _, _ := strings.Cut(name, "/")

	var b strings.Builder
	fmt.Fprintf(&b, "# %s @ %s (from %s)\n", name, shortSHA(entry.SHA), remoteName)

	if diff := activeBundleDiff(ctx, cfg, name, newData); diff != "" {
		b.WriteString("\n# structural diff vs active:\n")
		b.WriteString(diff)
	}
	b.WriteString("\n")
	b.Write(newData)
	if !strings.HasSuffix(string(newData), "\n") {
		b.WriteString("\n")
	}
	return b.String(), nil
}

// activeBundleDiff returns a structural diff of name's pending bytes against its
// active copy, or "" when there is no active copy (a new bundle) or it can't be
// loaded (non-fatal — the raw YAML is shown regardless).
func activeBundleDiff(ctx context.Context, cfg *config.Config, name string, newData []byte) string {
	active, err := operations.LoadActiveLockfile(cfg)
	if err != nil || active == nil {
		return ""
	}
	oldEntry, ok := active.GetEntry(remote.ItemTypeBundle, name)
	if !ok || oldEntry.SHA == "" {
		return ""
	}
	oldReader := operations.NewBundleReaderForLockfile(cfg, active)
	if oldReader == nil {
		return ""
	}
	oldData, err := oldReader.ReadBundleBytes(ctx, name)
	if err != nil {
		return ""
	}
	return diffBundleYAMLs(oldData, newData)
}

// applyHooksAfterReview re-applies hooks once a review action changed the active
// lockfile, so newly-approved bundles' hooks/context take effect. Best-effort:
// a failure warns but never fails the command.
func applyHooksAfterReview(ctx context.Context, cfg *config.Config) {
	if _, err := operations.ApplyHooks(ctx, cfg, operations.ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to apply hooks: %v\n", err)
	}
}
