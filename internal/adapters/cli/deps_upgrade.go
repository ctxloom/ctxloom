package cli

import (
	"fmt"

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

// depsUpgradeCmd is the apt-style "upgrade" verb: it advances every unheld
// pinned dependency to the newest commit its version constraint allows and
// writes the result straight to the active lock. Where 'deps check' reads and
// reports, 'deps upgrade' advances your pins.
var depsUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade pinned dependencies to the latest available",
	Long: `Re-resolve each local profile's dependency closure to the newest commit each
version constraint allows and write the advances straight to the active lock —
your profile YAML is never rewritten. A held entry ('ctxloom deps hold') stays
frozen.

The lockfile is pure dependency pinning: upgrading a pin does not expose new
content to the agent. Any changed content from an untrusted source is withheld
until you accept it with 'ctxloom review'.

A pin is NOT advanced onto content whose publisher signature does not verify
over its bytes: that content is withheld as tampered and cannot be reviewed, so
advancing past the last commit that did verify would leave you with neither
copy. The old pin is kept and the refusal is reported.

A refusal EXITS 2, not 0 and not 1: the command ran fine and deliberately did
not do part of what it was asked, so an unattended sync can tell "I refused
something" apart from both "nothing to do" (0) and a failure (1). The refusal
also survives the run — 'ctxloom doctor' reports it until an upgrade advances
that pin.

Mirrors apt: 'deps check' reports what is out of date, 'deps upgrade' advances
your pins to the newest commit. 'deps pull' installs exactly what is already
pinned and never advances one.

A pin is also NOT moved below the version its publisher signed at the last pin
— a rollback to an older signed release — nor from signed to unsigned content.
Name a ref with --allow-downgrade to accept that for it; the lower version then
becomes its floor.

Examples:
  ctxloom deps upgrade                   # Advance pins to the latest available
  ctxloom deps upgrade --allow-downgrade <ref>   # Accept a lower signed version for <ref>`,
	RunE: runDepsUpgradeCmd,
}

func runDepsUpgradeCmd(cmd *cobra.Command, args []string) error {
	return runDepsUpgrade(cmd, GetConfig)
}

// runDepsUpgrade re-resolves and rewrites the active lock.
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

	fmt.Println("Resolving latest commits for pinned dependencies...")

	res, err := operations.UpgradeDependencies(cmd.Context(), cfg, depsUpgradeAllowDowngrade)
	if err != nil {
		return err
	}

	// The refusals print FIRST and unconditionally, before any tally. A pin
	// that did not move because its new content failed publisher verification
	// reads exactly like a pin that had nothing to move to, and the difference
	// is the whole point: one means "you are current", the other means
	// "somebody published bytes their signature does not cover".
	reportRefusedAdvances(res.Refused)

	if res.Advanced == 0 {
		if len(res.Refused) > 0 {
			// Deliberately NOT "Everything is up to date." — nothing advanced
			// precisely because something was wrong upstream.
			fmt.Printf("No pins advanced: %d refused above. Your existing pins are unchanged.\n", len(res.Refused))
			return refusedExit()
		}
		// "Everything is up to date." used to print unconditionally
		// here, even on a round where part of the dependency closure could not
		// be reached (a warning about it prints separately, but the terminal
		// message still claimed a clean, complete check). advanced==0 only
		// means nothing that WAS resolved needed to move — it says nothing
		// about the part that was never resolved at all.
		if res.Incomplete {
			fmt.Println("No pins advanced among what could be resolved — part of the dependency closure was unreachable this round (see warning above); re-run once it's reachable to get a complete picture.")
		} else if res.NothingDeclared {
			// An empty closure reaches this branch with advanced==0 and nothing
			// refused, exactly like a healthy current project — and it used to
			// print the same line. The difference is the one the user needs:
			// one means "your pins are current", the other means "there is
			// nothing here", and only the second has a remedy.
			fmt.Println(msgNothingDeclared)
		} else {
			fmt.Println(msgEverythingUpToDate)
		}
		return nil
	}

	fmt.Printf("Advanced %d dependency pin(s).\n", res.Advanced)
	// This used to say "Changed content from untrusted sources is withheld
	// until reviewed: ctxloom review", which was a dead end: the content most
	// likely to be withheld after an upgrade was withheld as TAMPERED, which is
	// deliberately not reviewable, so `ctxloom review` answered "Nothing is
	// pending review." and the user went in a circle. Upgrade now refuses that
	// advance outright (above), and what remains is pointed at an inspector
	// that answers whatever the state actually is.
	fmt.Println("Newly pinned content is not exposed to your assistant until it passes the trust gate: run 'ctxloom doctor' to see whether any of it is withheld, and why.")
	// A round can BOTH advance some pins and refuse others; the refusal is what
	// decides the exit code, because it is the part of the request that was not
	// carried out. Reporting a partial round as a clean 0 is the silence this
	// whole feature exists to remove.
	if len(res.Refused) > 0 {
		return refusedExit()
	}
	return nil
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

// reportRefusedAdvances says, for each pin upgrade declined to move, the three
// things a human needs and cannot infer: WHICH bundle, that its new content's
// signature does not verify, and WHICH pin is being kept instead.
//
// It names no command that cannot help. `ctxloom review` in particular is
// wrong here by construction — bytes a signature does not cover are never
// offered for review (bundles.Reason.NeedsReview), so sending the user there
// answers "Nothing is pending review." and teaches them the message is noise.
func reportRefusedAdvances(refused []operations.RefusedAdvance) {
	for _, r := range refused {
		if r.BelowFloor {
			fmt.Printf("REFUSED to advance %s: the content at %s is not signed at or above the version this project last pinned (%s).\n",
				r.Identity, shortSHA(r.ProposedSHA), r.Detail)
			fmt.Printf("  Keeping the pin %s. Whoever controls the repository can re-serve an older signed release; if going back is what you intend, re-run with --allow-downgrade %s.\n",
				shortSHA(r.KeptSHA), r.Identity)
			continue
		}
		fmt.Printf("REFUSED to advance %s: the publisher signature on the content at %s does not verify over those bytes (%s).\n",
			r.Identity, shortSHA(r.ProposedSHA), r.Detail)
		fmt.Printf("  Keeping the last verified pin %s — your assistant goes on receiving the content at that pin.\n", shortSHA(r.KeptSHA))
		fmt.Println("  There is nothing to accept: a signature that does not cover its bytes is a tamper signal, not unsigned content, so it is never offered for review. Ask the publisher to re-sign and publish again, then re-run 'ctxloom deps upgrade'.")
	}
}

var depsUpgradeAllowDowngrade []string

func init() {
	depsCmd.AddCommand(depsUpgradeCmd)
	depsUpgradeCmd.Flags().StringArrayVar(&depsUpgradeAllowDowngrade, "allow-downgrade", nil,
		"Accept a lower signed version (or unsigned content) for this ref, and record it as the new floor; repeat per ref")
}
