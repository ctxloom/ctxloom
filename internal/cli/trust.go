package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Per-item trust CLI (trust rework, TR2). These commands wire the TR1 mutation
// ops (operations.SetItemTrust / SetBlacklist / SetBundleTrust) to the user.
// Management is CLI-only: there are deliberately NO MCP tools for any of these.
// The coarse, separate `remote trust/untrust` tier lives in remote.go.

var trustCmd = &cobra.Command{
	Use:   "trust <ref>",
	Short: "Trust an item's current version (fragment, prompt, or MCP server)",
	Long: `Bless the currently-resolved version of an item so it is exposed to the agent.

The grant is bound to the item's effective-content hash (distilled or raw, per
config): a later content change drops the grant and forces re-review.

Reference format: <bundle-ref>#fragments/<name>, <bundle-ref>#prompts/<name>, or
<bundle-ref>#mcp/<name>. The bundle ref may be a canonical URL ref, a
ctxloom:local ref, or a plain local bundle name.

Address a specific historical version by appending @<commit> to the bundle ref
(recorded as provenance only):
  ctxloom trust 'https://github.com/acme/repo@bundles/tooling@9f3c1d7#mcp/postgres'

Examples:
  ctxloom trust core#fragments/tdd
  ctxloom trust ctxloom:local@bundles/dev#prompts/review
  ctxloom trust 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'

Revoke exposure with 'ctxloom blacklist <ref>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runItemTrust(cmd, cfg, args[0])
	},
}

// runItemTrust grants trust to the resolved item and reports the recorded grant.
// Split out (cfg-injectable) so it can be unit-tested against a temp project.
func runItemTrust(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.SetItemTrust(cfg, operations.SetItemTrustRequest{Ref: ref})
	if err != nil {
		return err
	}
	// Reflect the new grant on disk: a previously-withheld item is (re)written into
	// the managed artifacts now, not at the next apply.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Trusted %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:    %s\n", res.RepoURL)
		fmt.Fprintf(out, "  content: %s (%s)\n", res.ContentHash, res.Form)
		return nil
	})
}

var blacklistCmd = &cobra.Command{
	Use:   "blacklist <ref>",
	Short: "Blacklist an item so it is withheld from the agent",
	Long: `Withhold an item from every exposure surface, always.

A blacklist writes two companion entries: a sticky ref-level block (denies this
ref regardless of content/version, surviving content changes) and the item's
current content hash on the denylist (so a renamed or moved identical copy stays
blocked too). The denylist entry is only recorded when the item can be resolved;
the sticky ref block is written regardless.

Reference format matches 'ctxloom trust' (see its help).

Examples:
  ctxloom blacklist tooling#fragments/curl-pipe-sh
  ctxloom blacklist 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runBlacklist(cmd, cfg, args[0])
	},
}

// runBlacklist records both blacklist components and reports what was written.
func runBlacklist(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: ref})
	if err != nil {
		return err
	}
	// The content denylist component is best-effort: blacklisting must succeed
	// even when the item is gone, so surface when only the sticky ref block was
	// written (the durable guarantee), per the family warning convention.
	if res.ContentHash == "" {
		clidiag.Warn("ctxloom",
			"could not resolve %q to hash its content; the sticky ref blacklist applies, "+
				"but no content-denylist entry was recorded", ref)
	}
	// Scrub the now-withheld item from the managed artifacts immediately so an
	// already-written bundle MCP server / hook stops being exposed.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Blacklisted %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:        %s\n", res.RepoURL)
		fmt.Fprintln(out, "  ref block:   recorded (sticky — survives content changes)")
		if res.ContentHash != "" {
			fmt.Fprintf(out, "  denylist:    %s (blocks this content even if renamed/moved)\n", res.ContentHash)
		} else {
			fmt.Fprintln(out, "  denylist:    not recorded (content could not be resolved)")
		}
		return nil
	})
}

var bundleTrustCmd = &cobra.Command{
	Use:   "trust <name>",
	Short: "Trust a bundle so its grant-less items are exposed by default",
	Long: `Set a SHA-agnostic trusted posture for a whole bundle.

The posture cascades to every item in the bundle that has no explicit per-item
grant or blacklist. It is a posture toward the bundle as a source, not pinned to
any content hash.

Revoke with 'ctxloom bundle untrust <name>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runBundleTrust(cmd, cfg, args[0], true)
	},
}

var bundleUntrustCmd = &cobra.Command{
	Use:   "untrust <name>",
	Short: "Untrust a bundle so its grant-less items are withheld by default",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runBundleTrust(cmd, cfg, args[0], false)
	},
}

// runBundleTrust sets the bundle posture and reports it.
func runBundleTrust(cmd *cobra.Command, cfg *config.Config, name string, trust bool) error {
	res, err := operations.SetBundleTrust(cfg, operations.SetBundleTrustRequest{
		Bundle: name,
		Trust:  trust,
	})
	if err != nil {
		return err
	}
	// A posture flip cascades to the bundle's grant-less items, so refresh the
	// managed artifacts to expose or withhold them per the new posture.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Bundle '%s' is now %s.\n", res.Bundle, res.Status)
		return nil
	})
}

// refreshManagedArtifacts re-applies the managed harness after a trust mutation so
// the gate's new decision is reflected on disk immediately: a now-withheld bundle
// MCP server / hook is scrubbed from backend settings (and the regenerated
// context), and a newly-trusted one is (re)written, without waiting for the next
// `manage hooks install` / `ctxloom run`. The re-apply routes through
// operations.ApplyHooks, which gates every executable surface (bundle MCP servers,
// bundle hooks, prompt exports) through NewExecutableTrustGate and the regenerated
// context through exposureLoader — the same chokes context assembly uses — so the
// on-disk artifacts stay consistent with what assembly would expose. Gating logic
// is not duplicated here; this only triggers the existing apply path.
//
// Fault tolerant (CLAUDE.md): the trust change has already persisted, so a refresh
// failure only warns — it never fails the mutation, never blocks. It refreshes
// only a project whose harness is already applied (some backend has managed
// artifacts wired); re-applying into a project that never installed the harness
// would create artifacts where none exist, so that case is skipped.
func refreshManagedArtifacts(ctx context.Context, cfg *config.Config) {
	if !harnessApplied(ctx, cfg) {
		return
	}
	if _, err := operations.ApplyHooks(ctx, cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	}); err != nil {
		clidiag.Warn("ctxloom", "failed to refresh managed artifacts after trust change: %v", err)
	}
}

// harnessApplied reports whether ctxloom has managed artifacts wired into any
// settings-supporting backend. It guards refreshManagedArtifacts so only a project
// with an applied harness is refreshed. A status-read failure is treated as
// "not applied" — fail safe toward never creating artifacts on an unreadable
// project (the trust change persisted regardless).
func harnessApplied(ctx context.Context, cfg *config.Config) bool {
	status, err := operations.HarnessStatus(ctx, cfg, operations.HarnessStatusRequest{})
	if err != nil {
		return false
	}
	for _, b := range status.Backends {
		if b.HooksPresent || b.StatusLine || b.MCPPresent {
			return true
		}
	}
	return false
}

func init() {
	// Top-level per-item mutations.
	rootCmd.AddCommand(trustCmd)
	rootCmd.AddCommand(blacklistCmd)

	// Bundle-grain posture, mirroring the existing `bundle` subcommand
	// registration in bundle.go. bundleCmd is a package-level var, initialized
	// before any init() runs, so adding to it here is safe.
	bundleCmd.AddCommand(bundleTrustCmd)
	bundleCmd.AddCommand(bundleUntrustCmd)
}
