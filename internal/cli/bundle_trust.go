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

// Per-item content-decision CLI: the scriptable plumbing of the three-state
// review model. The store holds three states, so there are three verbs, each
// named for what it writes — `bundle trust` records an approval, `bundle
// reject` records a rejection, and `bundle forget` records NOTHING: it clears
// whichever decision is on file and returns the item to pending. They are the
// same states, through the same mutation path, the interactive `ctxloom
// review` porcelain writes.
//
// Management is CLI-only: there are deliberately NO MCP tools for any of these.
// Trust is keyed to a publisher's signing KEY, never to a remote: an address
// says nothing about the bytes that arrive from it. See docs/trust-model.md.

// bundleTrustLong documents `ctxloom bundle trust`.
const bundleTrustLong = `Accept the currently-resolved content of an item so it is exposed to the agent.

The acceptance is bound to the item's current content-hash pair (raw and, when
one exists, distilled): a later change to either form returns the item to
pending and forces re-review.

When a signing key is available the acceptance is COUNTERSIGNED with it, and
that key must be trusted for the 'approve' namespace — a countersignature from
a key you have not granted it is honoured by nothing, so recording one is
refused rather than reported as success. Grant it with
'ctxloom signer trust <principal> --key <key.pub> --namespace approve,reject'.
With no signing key at all the decision is recorded unsigned in your personal
store.

Reference format: <bundle-ref>#fragments/<name>, <bundle-ref>#commands/<name>,
<bundle-ref>#mcp/<name>, or <bundle-ref>#hooks/<event>/<index>. The bundle ref
may be a canonical URL ref, a ctxloom:local ref, or a plain local bundle name.

Examples:
  ctxloom bundle trust core#fragments/tdd
  ctxloom bundle trust ctxloom:local@bundles/dev#commands/review
  ctxloom bundle trust 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'

Reject an item with 'ctxloom bundle reject <ref>'. Withdraw this approval —
returning the item to pending, without rejecting it — with
'ctxloom bundle forget <ref>'.`

// runBundleTrustCmd is bundleTrustCmd's RunE.
func runBundleTrustCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runItemTrust(cmd, cfg, args[0])
}

// bundleTrustCmd records that an item's current content may reach the agent.
var bundleTrustCmd = &cobra.Command{
	Use:   "trust <ref>",
	Short: "Trust an item's current content (fragment, command, MCP server, or hook)",
	Long:  bundleTrustLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runBundleTrustCmd,
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

// bundleRejectLong documents `ctxloom bundle reject`.
const bundleRejectLong = `Reject an item, withholding it from every exposure surface, always.

A rejection writes two companion entries: the ref-level rejected state (denies
this ref regardless of content/version, surviving content changes) and the
item's current content hashes on the denylist (so a renamed or moved identical
copy stays rejected too). The denylist entries are only recorded when the item
can be resolved; the ref-level rejection is written regardless.

Rejection beats every exemption: it withholds the item even from a trusted
source and even for project-local content. That is why this is not the way to
undo an approval — it is a stronger, stickier statement than withdrawing one.
To return an item to pending, decided neither way, use
'ctxloom bundle forget <ref>'.

Reference format matches 'ctxloom bundle trust' (see its help).

Examples:
  ctxloom bundle reject tooling#fragments/curl-pipe-sh
  ctxloom bundle reject 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'`

// runBundleRejectCmd is bundleRejectCmd's RunE.
func runBundleRejectCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runItemReject(cmd, cfg, args[0])
}

// bundleRejectCmd withholds an item from every exposure surface.
var bundleRejectCmd = &cobra.Command{
	Use:   "reject <ref>",
	Short: "Reject an item so it is withheld from the agent, always",
	Long:  bundleRejectLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runBundleRejectCmd,
}

// runItemReject records both rejection components and reports what was written.
func runItemReject(cmd *cobra.Command, cfg *config.Config, ref string) error {
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

// bundleForgetProject targets the committable project store.
var bundleForgetProject bool

// bundleForgetLong documents `ctxloom bundle forget`.
const bundleForgetLong = `Clear whichever decision is recorded for an item, returning it to pending.

This is the inverse of BOTH other verbs: it removes an approval, and it removes
a rejection — the ref-level block and the content block together — leaving the
item exactly as it was before anyone reviewed it. It is what you want when a
decision was made in error, because rejecting an item you had approved does not
withdraw the approval, it records something stronger.

It writes nothing, so it needs no signing key and no namespace grant. Removing
your own record asserts nothing anyone else has to honour.

It clears ONE store: your personal one by default, the committable project
store with --project. A decision the other store holds still stands, and the
command says so rather than reporting an item back to pending while it stays
withheld.

Reference format matches 'ctxloom bundle trust' (see its help).

Examples:
  ctxloom bundle forget tooling#fragments/curl-pipe-sh
  ctxloom bundle forget --project 'https://github.com/acme/repo@bundles/tooling#mcp/postgres'`

// runBundleForgetCmd is bundleForgetCmd's RunE.
func runBundleForgetCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	return runItemForget(cmd, cfg, args[0])
}

// bundleForgetCmd returns an item to pending by clearing its decision.
var bundleForgetCmd = &cobra.Command{
	Use:   "forget <ref>",
	Short: "Clear an item's recorded decision (approval or rejection), back to pending",
	Long:  bundleForgetLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runBundleForgetCmd,
}

// runItemForget clears the recorded decision and reports what actually went.
//
// The two outcomes are rendered DIFFERENTLY on purpose. "Forgot <ref>" over an
// item that had no decision recorded would confirm a mistyped ref as a
// success; and an item still decided by the other store is still withheld, so
// saying only "cleared" would be the silent no-op wearing a success message.
func runItemForget(cmd *cobra.Command, cfg *config.Config, ref string) error {
	res, err := operations.ForgetItemDecision(cfg, operations.ForgetItemDecisionRequest{Ref: ref, Project: bundleForgetProject})
	if err != nil {
		return err
	}
	if res.StillDecided != "" {
		clidiag.Warn("ctxloom",
			"%q is still %s by a record in the other approvals store, so it stays withheld; "+
				"clear that one too (add or drop --project)", ref, res.StillDecided)
	}
	// A cleared approval un-exposes an executable surface and a cleared
	// rejection re-exposes one, so the managed artifacts are refreshed here for
	// the same reason the other two verbs refresh them.
	refreshManagedArtifacts(cmd.Context(), cfg)
	return emit(cmd, res, func() error {
		out := cmd.OutOrStdout()
		if !res.ClearedAnything() {
			fmt.Fprintf(out, "No decision was recorded for %s — nothing to clear.\n", res.Ref)
			return nil
		}
		fmt.Fprintf(out, "Forgot %s\n", res.Ref)
		fmt.Fprintf(out, "  repo:    %s\n", res.RepoURL)
		fmt.Fprintf(out, "  store:   %s\n", res.Store)
		fmt.Fprintf(out, "  cleared: %s (%d record(s)) — the item is pending review again\n",
			strings.Join(res.Cleared, " and "), res.Records)
		if len(res.ContentForms) > 0 {
			fmt.Fprintf(out, "  content: cleared in form(s) %s\n", strings.Join(res.ContentForms, ", "))
		} else if !res.ContentResolved {
			fmt.Fprintln(out, "  content: could not be resolved, so only the ref-level record was cleared")
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
	// Per-item decisions belong to the noun that owns the items.
	bundleCmd.AddCommand(bundleTrustCmd)
	bundleCmd.AddCommand(bundleRejectCmd)
	bundleForgetCmd.Flags().BoolVar(&bundleForgetProject, "project", false,
		"Clear the decision from the committable project store (.ctxloom/approvals) instead of your personal one")
	bundleCmd.AddCommand(bundleForgetCmd)
}
