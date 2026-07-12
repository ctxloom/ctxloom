package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Per-item trust CLI: the scriptable plumbing of the three-state review model
// (trust-simplify). `trust` records accepted, `blacklist` records rejected —
// the same states the interactive `ctxloom review` porcelain (slice 2) writes.
// Management is CLI-only: there are deliberately NO MCP tools for any of these.
// (Source-level `remote trust/untrust` is deleted — trust is now keyed to a
// publisher signing key, not a remote; see docs/trust-model.md.)

var trustCmd = &cobra.Command{
	Use:   "trust <ref>",
	Short: "Accept an item's current content (fragment, skill, MCP server, or hook)",
	Long: `Accept the currently-resolved content of an item so it is exposed to the agent.

The acceptance is bound to the item's current content-hash pair (raw and, when
one exists, distilled): a later change to either form returns the item to
pending and forces re-review.

Reference format: <bundle-ref>#fragments/<name>, <bundle-ref>#skills/<name>,
<bundle-ref>#mcp/<name>, or <bundle-ref>#hooks/<event>/<index>. The bundle ref
may be a canonical URL ref, a ctxloom:local ref, or a plain local bundle name.

Examples:
  ctxloom trust core#fragments/tdd
  ctxloom trust ctxloom:local@bundles/dev#skills/review
  ctxloom trust 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'

Reject an item with 'ctxloom blacklist <ref>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		return runItemTrust(cmd, cfg, args[0])
	},
}

// runItemTrust records the resolved item as accepted and reports the recorded
// hash pair. Split out (cfg-injectable) so it can be unit-tested against a temp
// project.
func runItemTrust(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.SetItemTrust(cfg, operations.SetItemTrustRequest{Ref: ref})
	if err != nil {
		return err
	}
	// Reflect the new acceptance on disk: a previously-withheld item is
	// (re)written into the managed artifacts now, not at the next apply.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Accepted %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:      %s\n", res.RepoURL)
		fmt.Fprintf(out, "  raw:       %s\n", res.RawHash)
		if res.DistilledHash != "" {
			fmt.Fprintf(out, "  distilled: %s\n", res.DistilledHash)
		}
		return nil
	})
}

var blacklistCmd = &cobra.Command{
	Use:   "blacklist <ref>",
	Short: "Reject an item so it is withheld from the agent",
	Long: `Reject an item, withholding it from every exposure surface, always.

A rejection writes two companion entries: the ref-level rejected state (denies
this ref regardless of content/version, surviving content changes) and the
item's current content hashes on the denylist (so a renamed or moved identical
copy stays rejected too). The denylist entries are only recorded when the item
can be resolved; the ref-level rejection is written regardless.

Rejection beats every exemption: it withholds the item even from a trusted
source and even for project-local content.

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

// runBlacklist records both rejection components and reports what was written.
func runBlacklist(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: ref})
	if err != nil {
		return err
	}
	// The content denylist component is best-effort: rejecting must succeed
	// even when the item is gone, so surface when only the ref-level state was
	// written (the durable guarantee), per the family warning convention.
	if len(res.ContentHashes) == 0 {
		clidiag.Warn("ctxloom",
			"could not resolve %q to hash its content; the ref-level rejection applies, "+
				"but no content-denylist entry was recorded", ref)
	}
	// Scrub the now-withheld item from the managed artifacts immediately so an
	// already-written bundle MCP server / hook stops being exposed.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Rejected %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:      %s\n", res.RepoURL)
		fmt.Fprintln(out, "  ref block: recorded (sticky — survives content changes)")
		if len(res.ContentHashes) > 0 {
			for _, h := range res.ContentHashes {
				fmt.Fprintf(out, "  denylist:  %s (blocks this content even if renamed/moved)\n", h)
			}
		} else {
			fmt.Fprintln(out, "  denylist:  not recorded (content could not be resolved)")
		}
		return nil
	})
}

// refreshManagedArtifacts re-applies the managed harness after a trust mutation so
// the gate's new decision is reflected on disk immediately: a now-withheld bundle
// MCP server / hook is scrubbed from backend settings (and the regenerated
// context), and a newly-accepted one is (re)written, without waiting for the next
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
// reprise:ignore — its only twin was bundle_review_cli.go's applyHooksAfterReview, deleted with the review flow in the trust-simplify demolition; this survivor is correct and untouched, so the duplicate group no longer exists.
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
}
