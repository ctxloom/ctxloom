package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// remoteUpgradeCmd is the apt-style "upgrade" verb: it advances every unheld
// pinned dependency to the newest commit its version constraint allows and
// writes the result straight to the active lock. Where 'remote update' refreshes
// the local clones (the index), 'remote upgrade' advances your pins.
var remoteUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade pinned dependencies to the latest available",
	Long: `Re-resolve each local profile's dependency closure to the newest commit each
version constraint allows and write the advances straight to the active lock —
your profile YAML is never rewritten. A held entry ('ctxloom bundle hold') stays
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

Mirrors apt: where 'remote update' refreshes the local clones (the index),
'remote upgrade' advances your pins to the newest commit. Passive 'remote pull'
installs exactly what is already pinned and never advances.

Examples:
  ctxloom remote upgrade                 # Advance pins to the latest available`,
	RunE: runRemoteUpgradeCmd,
}

func runRemoteUpgradeCmd(cmd *cobra.Command, args []string) error {
	return runRemoteUpgrade(cmd, GetConfig)
}

// runRemoteUpgrade re-resolves and rewrites the active lock.
//
// It deliberately does NOT use loadConfigOrFallback. That helper exists for the
// fault-tolerant READ-ONLY startup paths (`remote update`, `search`) and hands
// back a minimal EMPTY config on any load error so they can keep working.
// Upgrade is destructive: it rewrites the lockfile wholesale from whatever
// closure the config yields, and an empty config yields an empty closure — no
// profile definitions to enumerate, nothing proposed, so the write erased every
// pin, hold and retraction and printed "Everything is up to date." A command
// that rewrites state must fail on a config it could not read, not guess.
func runRemoteUpgrade(cmd *cobra.Command, loadConfig func() (*config.Config, error)) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("cannot upgrade dependencies: the config could not be loaded, and upgrade rewrites the lockfile from it: %w", err)
	}

	fmt.Println("Resolving latest commits for pinned dependencies...")

	res, err := operations.UpgradeDependencies(cmd.Context(), cfg)
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
		} else {
			fmt.Println("Everything is up to date.")
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
		fmt.Printf("REFUSED to advance %s: the publisher signature on the content at %s does not verify over those bytes (%s).\n",
			r.Identity, shortSHA(r.ProposedSHA), r.Detail)
		fmt.Printf("  Keeping the last verified pin %s — your assistant goes on receiving the content at that pin.\n", shortSHA(r.KeptSHA))
		fmt.Println("  There is nothing to accept: a signature that does not cover its bytes is a tamper signal, not unsigned content, so it is never offered for review. Ask the publisher to re-sign and publish again, then re-run 'ctxloom remote upgrade'.")
	}
}

func init() {
	remoteCmd.AddCommand(remoteUpgradeCmd)
}
