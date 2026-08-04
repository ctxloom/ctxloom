package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
)

// The exec-consent CLI: the scriptable half of the trust-on-first-use decision
// `ctxloom` makes interactively the first time it meets a companion binary.
//
// Companions are DISCOVERED, not configured — the first-party names plus every
// `ctxloom-companion-*` on $PATH — and reading a companion's loadout means
// EXECUTING it. So a human has to agree, once, per binary. Interactively that
// happens at the prompt; these three leaves are how the same decision is made,
// inspected and undone from a script, from CI, or after the fact.
//
// Deliberately NO MCP tools for any of this, matching every other trust
// surface: handing the agent the ability to approve the binaries that run
// alongside it defeats the property the consent exists to provide.

const trustCompanionLong = `Inspect and change which companion binaries ctxloom may execute.

ctxloom discovers companions on your PATH (the shipped ltk / taskloom / reprise,
plus anything named ctxloom-companion-*) and EXECUTES each one to read the
context it contributes. Because any program on your PATH can claim one of those
names — including a transitive dependency in ./node_modules/.bin — a companion
ctxloom has not run before is put to you once and the answer recorded, keyed to
the binary's absolute path AND its SHA-256. Replace the file and you are asked
again.

A non-interactive session (an agent, CI) is never prompted: an unconfirmed
companion is skipped with a warning. 'allow' is how you record the decision for
one anyway.

The shipped companions are exempt from the prompt only when they resolve from
the directory ctxloom itself is installed in. An 'ltk' found anywhere else is a
third-party binary that picked a familiar name, and is asked about like any other.

Decisions live in ~/.ctxloom/companion_consent.yaml. There is deliberately no
committable project counterpart — a repo you cloned must not be able to arrive
carrying pre-approved binaries.`

var trustCompanionCmd = groupNode(&cobra.Command{
	Use:   "companion",
	Short: "Inspect and change which companion binaries ctxloom may execute",
	Long:  trustCompanionLong,
})

var trustCompanionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded companion execution decisions",
	Long:  trustCompanionLong,
	Args:  cobra.NoArgs,
	RunE:  runTrustCompanionListCmd,
}

var trustCompanionAllowCmd = &cobra.Command{
	Use:   "allow <path-or-name>",
	Short: "Record that ctxloom may execute a companion binary",
	Long:  trustCompanionLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runTrustCompanionAllowCmd,
}

var trustCompanionForgetCmd = &cobra.Command{
	Use:   "forget <path-or-name>",
	Short: "Drop the recorded decision for a companion binary (it is asked about again)",
	Long:  trustCompanionLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runTrustCompanionForgetCmd,
}

// trustCompanionListing is the emitted shape of `trust companion list`.
type trustCompanionListing struct {
	Path       string `json:"path" yaml:"path" toml:"path"`
	Bin        string `json:"bin" yaml:"bin" toml:"bin"`
	SHA256     string `json:"sha256" yaml:"sha256" toml:"sha256"`
	Allowed    bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	RecordedAt string `json:"recorded_at" yaml:"recorded_at" toml:"recorded_at"`
}

func runTrustCompanionListCmd(cmd *cobra.Command, _ []string) error {
	records, err := config.ListCompanionConsent()
	if err != nil {
		return err
	}
	out := make([]trustCompanionListing, 0, len(records))
	for _, r := range records {
		out = append(out, trustCompanionListing{
			Path:       r.Path,
			Bin:        r.Bin,
			SHA256:     r.SHA256,
			Allowed:    r.Approved,
			RecordedAt: r.RecordedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return emit(cmd, out, func() error { return printTrustCompanionListings(cmd.OutOrStdout(), out) })
}

// printTrustCompanionListings renders the text form. An empty store says so in
// words rather than printing nothing: "no output" and "nothing recorded" are
// the same pixels and very different facts.
func printTrustCompanionListings(w io.Writer, listings []trustCompanionListing) error {
	if len(listings) == 0 {
		_, err := fmt.Fprintln(w, "no companion decisions recorded")
		return err
	}
	for _, l := range listings {
		state := "allowed"
		if !l.Allowed {
			state = "declined"
		}
		if _, err := fmt.Fprintf(w, "%-10s %-8s %s (sha256 %s)\n", state, l.Bin, l.Path, shortSHA(l.SHA256)); err != nil {
			return err
		}
	}
	return nil
}

// trustCompanionDecision is the emitted shape of `allow` / `forget`.
type trustCompanionDecision struct {
	Path    string `json:"path" yaml:"path" toml:"path"`
	Bin     string `json:"bin" yaml:"bin" toml:"bin"`
	SHA256  string `json:"sha256" yaml:"sha256" toml:"sha256"`
	Allowed bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Forgot  int    `json:"forgot" yaml:"forgot" toml:"forgot"`
}

func runTrustCompanionAllowCmd(cmd *cobra.Command, args []string) error {
	rec, err := config.SetCompanionConsent(args[0], true)
	if err != nil {
		return err
	}
	payload := trustCompanionDecision{Path: rec.Path, Bin: rec.Bin, SHA256: rec.SHA256, Allowed: true}
	return emit(cmd, payload, func() error {
		_, werr := fmt.Fprintf(cmd.OutOrStdout(),
			"allowed %s at %s (sha256 %s) — ctxloom will run it\n", rec.Bin, rec.Path, shortSHA(rec.SHA256))
		return werr
	})
}

func runTrustCompanionForgetCmd(cmd *cobra.Command, args []string) error {
	removed, err := config.ForgetCompanionConsent(args[0])
	if err != nil {
		return err
	}
	payload := trustCompanionDecision{Path: args[0], Forgot: removed}
	return emit(cmd, payload, func() error {
		if removed == 0 {
			// Not an error — but never silently "succeeded" either: the user
			// asked to undo something that was not recorded, and needs to know
			// their revocation changed nothing.
			_, werr := fmt.Fprintf(cmd.OutOrStdout(), "forgot 0 decisions — nothing recorded for %s\n", args[0])
			return werr
		}
		_, werr := fmt.Fprintf(cmd.OutOrStdout(), "forgot %d decision(s) for %s\n", removed, args[0])
		return werr
	})
}

// shortSHA abbreviates a hex digest for human display. Full digests are in the
// record and in --format json; a 64-char hex string in a status line is noise a
// human cannot check by eye anyway.
func shortSHA(sum string) string {
	if len(sum) <= 16 {
		return sum
	}
	return sum[:16]
}

func init() {
	trustCmd.AddCommand(trustCompanionCmd)
	trustCompanionCmd.AddCommand(trustCompanionListCmd)
	trustCompanionCmd.AddCommand(trustCompanionAllowCmd)
	trustCompanionCmd.AddCommand(trustCompanionForgetCmd)
}
