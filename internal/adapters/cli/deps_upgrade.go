package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// The two terminal lines for a round that advanced nothing. They are constants
// because the WHOLE POINT of the pair is that they are different sentences for
// different facts, and a test that pins the difference has to name them.
//
// msgEverythingUpToDate is a claim about DECLARED dependencies: they were
// resolved, and none needed to move. It is a lie about a directory that
// declares none, which is why msgNothingDeclared exists and why it states the
// remedy — a user who sees it has almost always run the command somewhere
// other than the project they meant.
const (
	msgEverythingUpToDate = "Everything is up to date."
	msgNothingDeclared    = "Nothing to upgrade: no dependencies are declared here, so there was nothing to check and no lockfile was written.\n" +
		"  If you expected some, you are probably not in the project you meant: run 'ctxloom deps upgrade' from your project root.\n" +
		"  'ctxloom profile list' shows what this directory actually resolves, and 'ctxloom init' sets a new project up."
)

// depsUpgradeCmd is the apt-style "upgrade" verb and the ONE command that
// moves an existing pin: it shows every pin that would move to the newest
// commit its version constraint allows, and with --yes writes the result
// straight to the active lock.
var depsUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Show, then apply, the newest pins your constraints allow",
	Long: `Re-resolve each local profile's dependency closure to the newest commit each
version constraint allows, and show what every moved pin brings in: each hook,
MCP server, skill, command, fragment and profile added, removed or changed —
with what hooks and MCP servers run, before and after — and a unified diff of
every changed script.

Nothing is written without --yes. With it, the closure is resolved again,
applied to the active lock, each moved bundle's installed tree is moved with
it, and what was actually applied is shown — so a remote that moved since the
preview is what lands, and what you see. Your profile YAML is never rewritten.
A held entry ('ctxloom deps hold') stays frozen.

This is the only command that moves an existing pin. 'deps pull', 'init' and
startup create first pins and keep every existing one, even when you change a
constraint; that change takes effect here.

The lockfile is pure dependency pinning: upgrading a pin does not expose new
content to the agent. Any changed content from an untrusted source is withheld
until you accept it with 'ctxloom review'.

A pin is NOT advanced onto content whose publisher signature does not verify
over its bytes: that content is withheld as tampered and cannot be reviewed, so
advancing past the last commit that did verify would leave you with neither
copy. The old pin is kept and the refusal is reported.

A refusal EXITS 2, not 0 and not 1: the command ran fine and deliberately did
not do part of what it was asked, so an unattended sync can tell "I refused
something" apart from both "nothing to do" (0) and a failure (1). An applied
refusal also survives the run — 'ctxloom doctor' reports it until an upgrade
advances that pin.

A pin is also NOT moved below the version its publisher signed at the last pin
— a rollback to an older signed release — nor from signed to unsigned content.
Name a ref with --allow-downgrade to accept that for it; the lower version then
becomes its floor.`,
	Example: `  ctxloom deps upgrade                   # Show what would move, and what it brings in
  ctxloom deps upgrade --yes             # Apply it
  ctxloom deps upgrade --yes --allow-downgrade <ref>   # Accept a lower signed version for <ref>`,
	RunE: runDepsUpgradeCmd,
}

func runDepsUpgradeCmd(cmd *cobra.Command, args []string) error {
	return runDepsUpgrade(cmd, GetConfig)
}

// runDepsUpgrade re-resolves the closure, and with --yes rewrites the active
// lock.
//
// It deliberately does NOT use loadConfigOrFallback. That helper exists for the
// fault-tolerant READ-ONLY startup paths (`deps check`, `search`) and hands
// back a minimal EMPTY config on any load error so they can keep working.
// Upgrade is destructive: it rewrites the lockfile wholesale from whatever
// closure the config yields, and an empty config yields an empty closure — no
// profile definitions to enumerate, nothing proposed, so the write erased every
// pin, hold and retraction and printed "Everything is up to date." A command
// that rewrites state must fail on a config it could not read, not guess.
func runDepsUpgrade(cmd *cobra.Command, loadConfig func() (*config.Config, error)) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("cannot upgrade dependencies: the config could not be loaded, and upgrade rewrites the lockfile from it: %w", err)
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "Resolving latest commits for pinned dependencies...")

	res, err := upgradeDependencies(cmd.Context(), cfg, operations.UpgradeRequest{Apply: depsUpgradeYes, AllowDowngrade: depsUpgradeAllowDowngrade})
	if err != nil {
		return err
	}

	if err := emit(cmd, res, func() error {
		renderUpgrade(cmd.OutOrStdout(), res)
		return nil
	}); err != nil {
		return err
	}
	// A round can BOTH advance some pins and refuse others; the refusal is what
	// decides the exit code, because it is the part of the request that was not
	// carried out. Reporting a partial round as a clean 0 is the silence this
	// whole feature exists to remove. The payload above went out first, so a
	// machine caller has the refusals the exit code announces.
	if len(res.Refused) > 0 {
		return refusedExit()
	}
	return nil
}

// renderUpgrade is the human report of an upgrade round: refusals, removals,
// each pin that moves with what it brings in, and what to do next.
func renderUpgrade(out io.Writer, res operations.UpgradeResult) {
	// The refusals print FIRST and unconditionally. A pin that did not move
	// because its new content failed publisher verification reads exactly
	// like a pin that had nothing to move to, and the difference is the whole
	// point: one means "you are current", the other means "somebody published
	// bytes their signature does not cover".
	reportRefusedAdvances(out, res.Refused)
	// Removals print on every branch: the lock is rewritten wholesale, so an
	// entry dropped without a line here is indistinguishable from one that was
	// never pinned — including under "Everything is up to date."
	reportRemovedPins(out, res.Removed, res.Applied)
	operations.WritePinChanges(out, res.Changes)
	if len(res.Changes) == 0 {
		renderNothingMoves(out, res)
	}
	switch {
	case res.Applied && len(res.Changes) > 0:
		fmt.Fprintf(out, "Applied %d pin(s).\n", len(res.Changes))
		// Content withheld as TAMPERED is deliberately not reviewable, so
		// `ctxloom review` would answer "Nothing is pending review." here;
		// doctor answers whatever the state actually is.
		fmt.Fprintln(out, "Newly pinned content is not exposed to your assistant until it passes the trust gate: run 'ctxloom doctor' to see whether any of it is withheld, and why.")
	case !res.Applied && len(res.Changes)+len(res.Removed) > 0:
		fmt.Fprintf(out, "%d pin(s) would move. Re-run with --yes to apply.\n", len(res.Changes))
	}
}

// renderNothingMoves is the line for a round in which no pin moves; which line
// depends on WHY nothing moves.
func renderNothingMoves(out io.Writer, res operations.UpgradeResult) {
	switch {
	case len(res.Refused) > 0:
		// Deliberately NOT "Everything is up to date." — nothing moves
		// precisely because something was wrong upstream.
		fmt.Fprintf(out, "No pins advanced: %d refused above. Your existing pins are unchanged.\n", len(res.Refused))
	case res.Incomplete:
		// No changes only means nothing that WAS resolved needs to move — it
		// says nothing about the part that was never resolved at all.
		fmt.Fprintln(out, "No pins advanced among what could be resolved — part of the dependency closure was unreachable this round (see warning above); re-run once it's reachable to get a complete picture.")
	case res.NothingDeclared:
		// An empty closure reaches here with nothing refused, exactly like a
		// healthy current project. The difference is the one the user needs:
		// one means "your pins are current", the other means "there is
		// nothing here", and only the second has a remedy.
		fmt.Fprintln(out, msgNothingDeclared)
	default:
		fmt.Fprintln(out, msgEverythingUpToDate)
	}
}

// refusedExit is the exit-2 outcome: the command completed, said in full why,
// and deliberately did not do part of what it was asked (exitCodeRefused).
//
// It rides ExitError because that is this CLI's one mechanism for an error that
// carries its own code, and — load-bearing here — Run() returns an ExitError's
// code WITHOUT printing it. The refusal has already been reported above, in
// language a human can act on; an "Error: exit code 2" line under it would add
// nothing and would read as a failure of the tool rather than a decision by it.
func refusedExit() error {
	return &ExitError{Code: exitCodeRefused}
}

// The line reportRefusedAdvances closes each refusal with, by its cause.
const (
	msgRefusedTamper = "  There is nothing to accept: a signature that does not cover its bytes is a tamper signal, not unsigned content, so it is never offered for review. " +
		"Ask the publisher to re-sign and publish again, then re-run 'ctxloom deps upgrade'."
	msgRefusedUnreadable = "  This is not a signature failure: the content could not be read as a bundle, so nothing about its signature was established. " +
		"If the reason above is a fault in the bundle, the publisher must fix it and publish again; then re-run 'ctxloom deps upgrade'."
)

// reportRefusedAdvances says, for each pin upgrade declined to move, the three
// things a human needs and cannot infer: WHICH bundle, WHY its new content was
// refused (operations.RefusalCause — only a signature failure is worded as a
// tamper signal), and WHICH pin is being kept instead.
//
// It names no command that cannot help. `ctxloom review` in particular is
// wrong here by construction — bytes a signature does not cover are never
// offered for review (bundles.Reason.NeedsReview), so sending the user there
// answers "Nothing is pending review." and teaches them the message is noise.
func reportRefusedAdvances(out io.Writer, refused []operations.RefusedAdvance) {
	for _, r := range refused {
		if r.Cause == operations.RefusalBelowFloor {
			fmt.Fprintf(out, "REFUSED to advance %s: the content at %s is not signed at or above the version this project last pinned (%s).\n",
				r.Identity, shortSHA(r.ProposedSHA), r.Detail)
			fmt.Fprintf(out, "  Keeping the pin %s. Whoever controls the repository can re-serve an older signed release; if going back is what you intend, re-run with --allow-downgrade %s.\n",
				shortSHA(r.KeptSHA), r.Identity)
			continue
		}
		if r.Cause == operations.RefusalSignature {
			fmt.Fprintf(out, "REFUSED to advance %s: the publisher signature on the content at %s does not verify over those bytes (%s).\n",
				r.Identity, shortSHA(r.ProposedSHA), r.Detail)
		} else {
			fmt.Fprintf(out, "REFUSED to advance %s: the content at %s could not be read as a bundle (%s).\n",
				r.Identity, shortSHA(r.ProposedSHA), r.Detail)
		}
		fmt.Fprintf(out, "  Keeping the last verified pin %s — your assistant goes on receiving the content at that pin.\n", shortSHA(r.KeptSHA))
		if r.Cause == operations.RefusalSignature {
			fmt.Fprintln(out, msgRefusedTamper)
		} else {
			fmt.Fprintln(out, msgRefusedUnreadable)
		}
	}
}

// reportRemovedPins names each lockfile entry upgrade dropped (applied) or
// would drop (a preview) because nothing the project composes reaches it any
// more.
func reportRemovedPins(out io.Writer, removed []string, applied bool) {
	verb := "Would remove"
	if applied {
		verb = "Removed"
	}
	for _, identity := range removed {
		fmt.Fprintf(out, "%s %s from the lockfile: nothing this project composes depends on it any more.\n", verb, identity)
	}
}

var (
	depsUpgradeAllowDowngrade []string
	depsUpgradeYes            bool
)

func init() {
	depsCmd.AddCommand(depsUpgradeCmd)
	depsUpgradeCmd.Flags().BoolVarP(&depsUpgradeYes, yesFlagName, "y", false,
		"Apply the upgrade this invocation would report (default: report only)")
	depsUpgradeCmd.Flags().StringArrayVar(&depsUpgradeAllowDowngrade, "allow-downgrade", nil,
		"Accept a lower signed version (or unsigned content) for this ref, and record it as the new floor; repeat per ref")
}
