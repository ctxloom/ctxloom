package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Per-item trust CLI: the scriptable plumbing of the three-state review model
// (trust-simplify). `trust accept` records accepted, `trust reject` records
// rejected — the same states the interactive `ctxloom review` porcelain
// (slice 2) writes.
// Management is CLI-only: there are deliberately NO MCP tools for any of these.
// (Source-level `remote trust/untrust` is deleted — trust is now keyed to a
// publisher signing key, not a remote; see docs/trust-model.md.)

// trustCmdLong documents `ctxloom trust accept`.
const trustCmdLong = `Accept the currently-resolved content of an item so it is exposed to the agent.

The acceptance is bound to the item's current content-hash pair (raw and, when
one exists, distilled): a later change to either form returns the item to
pending and forces re-review.

Reference format: <bundle-ref>#fragments/<name>, <bundle-ref>#commands/<name>,
<bundle-ref>#mcp/<name>, or <bundle-ref>#hooks/<event>/<index>. The bundle ref
may be a canonical URL ref, a ctxloom:local ref, or a plain local bundle name.

Examples:
  ctxloom trust accept core#fragments/tdd
  ctxloom trust accept ctxloom:local@bundles/dev#commands/review
  ctxloom trust accept 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'

Reject an item with 'ctxloom trust reject <ref>'.`

// trustCmd is a PURE GROUP NODE: the parent of accept|reject|signer. It used
// to also be runnable as a bare `ctxloom trust <ref>` alias for `trust
// accept`, which is deleted — an imperative bare noun that silently means
// "accept" is exactly the surface a user cannot guess safely (verb-spine
// reorg §6).
var trustCmd = groupNode(&cobra.Command{
	Use:   "trust",
	Short: "Accept, reject, or manage the signers of item content",
	Long:  trustCmdLong,
})

// runTrustAcceptCmd is trustAcceptCmd's RunE.
func runTrustAcceptCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runItemTrust(cmd, cfg, args[0])
}

// trustAcceptCmd is the real home of `ctxloom trust <ref>`.
var trustAcceptCmd = &cobra.Command{
	Use:   "accept <ref>",
	Short: "Accept an item's current content (fragment, command, MCP server, or hook)",
	Long:  trustCmdLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runTrustAcceptCmd,
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
		fmt.Fprintf(out, "Approved %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:  %s\n", res.RepoURL)
		fmt.Fprintf(out, "  store: %s\n", res.Store)
		if res.Unsigned {
			fmt.Fprintln(out, "  UNSIGNED — recorded locally, not shareable (no signing key was available)")
		} else {
			fmt.Fprintf(out, "  signed by: %s\n", res.KeyFingerprint)
		}
		return nil
	})
}

// blacklistLong documents `ctxloom trust reject`.
const blacklistLong = `Reject an item, withholding it from every exposure surface, always.

A rejection writes two companion entries: the ref-level rejected state (denies
this ref regardless of content/version, surviving content changes) and the
item's current content hashes on the denylist (so a renamed or moved identical
copy stays rejected too). The denylist entries are only recorded when the item
can be resolved; the ref-level rejection is written regardless.

Rejection beats every exemption: it withholds the item even from a trusted
source and even for project-local content.

Reference format matches 'ctxloom trust accept' (see its help).

Examples:
  ctxloom trust reject tooling#fragments/curl-pipe-sh
  ctxloom trust reject 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'`

// runBlacklistCmd is trustRejectCmd's RunE.
func runBlacklistCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runBlacklist(cmd, cfg, args[0])
}

// trustRejectCmd is the trust noun's `reject` decision verb.
var trustRejectCmd = &cobra.Command{
	Use:   "reject <ref>",
	Short: "Reject an item so it is withheld from the agent",
	Long:  blacklistLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runBlacklistCmd,
}

// runBlacklist records both rejection components and reports what was written.
func runBlacklist(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.SetBlacklist(cfg, operations.SetBlacklistRequest{Ref: ref})
	if err != nil {
		return err
	}
	// The content-reject component is best-effort: rejecting must succeed
	// even when the item is gone, so surface when only the ref-level state was
	// written (the durable guarantee), per the family warning convention.
	if len(res.ContentForms) == 0 {
		clidiag.Warn("ctxloom",
			"could not resolve %q to countersign its content; the ref-level rejection applies, "+
				"but no content-reject countersignature was recorded", ref)
	}
	// Scrub the now-withheld item from the managed artifacts immediately so an
	// already-written bundle MCP server / hook stops being exposed.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Rejected %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:  %s\n", res.RepoURL)
		fmt.Fprintf(out, "  store: %s\n", res.Store)
		if res.Unsigned {
			fmt.Fprintln(out, "  UNSIGNED — recorded locally, not shareable (no signing key was available)")
		} else {
			fmt.Fprintf(out, "  signed by: %s\n", res.KeyFingerprint)
		}
		fmt.Fprintln(out, "  ref block: recorded (sticky — survives content changes)")
		if len(res.ContentForms) > 0 {
			fmt.Fprintf(out, "  content:   rejected in form(s) %s (blocks this content even if renamed/moved)\n", strings.Join(res.ContentForms, ", "))
		} else {
			fmt.Fprintln(out, "  content:   not recorded (content could not be resolved)")
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
	if _, err := operations.ApplyHooks(ctx, operations.ApplyHooksRequest{
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
	// Top-level per-item mutations. trustCmd is a pure group node.
	rootCmd.AddCommand(trustCmd)

	trustCmd.AddCommand(trustAcceptCmd)
	trustCmd.AddCommand(trustRejectCmd)
	trustCmd.AddCommand(trustSignerCmd)
}
