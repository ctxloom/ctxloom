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

const companionLong = `Inspect and change which companion binaries ctxloom may execute.

ctxloom discovers companions on your PATH (the shipped ltk / taskloom / reprise,
plus anything named ctxloom-companion-*) and EXECUTES each one to read the
context it contributes. Because any program on your PATH can claim one of those
names — including a transitive dependency in ./node_modules/.bin — a companion
ctxloom has not run before is put to you once and the answer recorded, keyed to
the binary's absolute path AND its SHA-256. Replace the file and you are asked
again.

A non-interactive session (an agent, CI) is never prompted: an unconfirmed
companion is skipped with a warning. 'companion trust' is how you record the
decision for one anyway, and 'companion untrust' drops it so the next run asks
again.

The shipped companions are exempt from the prompt only when they resolve from
the directory ctxloom itself is installed in. An 'ltk' found anywhere else is a
third-party binary that picked a familiar name, and is asked about like any other.

Decisions live in ~/.ctxloom/companion_consent.yaml. There is deliberately no
committable project counterpart — a repo you cloned must not be able to arrive
carrying pre-approved binaries.`

// Bare `ctxloom companion` lists the recorded execution decisions: the record
// is the one thing the noun is about, and reading it executes nothing.
var companionCmd = groupNodeDefault(&cobra.Command{
	Use:   "companion",
	Short: "Manage which companion binaries ctxloom may execute",
	Long:  companionLong,
}, "list")

var companionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded companion execution decisions",
	Long:  companionLong,
	Args:  cobra.NoArgs,
	RunE:  runCompanionListCmd,
}

var companionTrustCmd = &cobra.Command{
	Use:   "trust <path-or-name>",
	Short: "Record that ctxloom may execute a companion binary",
	Long:  companionLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runCompanionTrustCmd,
}

var companionUntrustCmd = &cobra.Command{
	Use:   "untrust <path-or-name>",
	Short: "Drop the recorded decision for a companion binary (it is asked about again)",
	Long:  companionLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runCompanionUntrustCmd,
}

// companionListing is the emitted shape of `companion list`.
type companionListing struct {
	Path       string `json:"path" yaml:"path" toml:"path"`
	Bin        string `json:"bin" yaml:"bin" toml:"bin"`
	SHA256     string `json:"sha256" yaml:"sha256" toml:"sha256"`
	Allowed    bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	RecordedAt string `json:"recorded_at" yaml:"recorded_at" toml:"recorded_at"`
}

func runCompanionListCmd(cmd *cobra.Command, _ []string) error {
	records, err := config.ListCompanionConsent()
	if err != nil {
		return err
	}
	out := make([]companionListing, 0, len(records))
	for _, r := range records {
		out = append(out, companionListing{
			Path:       r.Key.Path,
			Bin:        r.Key.Bin,
			SHA256:     r.Key.SHA256,
			Allowed:    r.Approved,
			RecordedAt: r.RecordedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return emit(cmd, out, func() error { return printCompanionListings(cmd.OutOrStdout(), out) })
}

// printCompanionListings renders the text form: the binary, where it resolved
// from, and the digest the decision is bound to.
func printCompanionListings(w io.Writer, listings []companionListing) error {
	return printAdmissionListings(w, listings, "no companion decisions recorded",
		func(l companionListing) bool { return l.Allowed },
		func(l companionListing) string {
			return fmt.Sprintf("%-8s %s (sha256 %s)", l.Bin, l.Path, shortSHA(l.SHA256))
		})
}

// companionDecision is the emitted shape of `allow` / `forget`.
type companionDecision struct {
	Path    string `json:"path" yaml:"path" toml:"path"`
	Bin     string `json:"bin" yaml:"bin" toml:"bin"`
	SHA256  string `json:"sha256" yaml:"sha256" toml:"sha256"`
	Allowed bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Forgot  int    `json:"forgot" yaml:"forgot" toml:"forgot"`
}

func runCompanionTrustCmd(cmd *cobra.Command, args []string) error {
	rec, err := config.SetCompanionConsent(args[0], true)
	if err != nil {
		return err
	}
	payload := companionDecision{Path: rec.Key.Path, Bin: rec.Key.Bin, SHA256: rec.Key.SHA256, Allowed: true}
	return emit(cmd, payload, func() error {
		_, werr := fmt.Fprintf(cmd.OutOrStdout(),
			"allowed %s at %s (sha256 %s) — ctxloom will run it\n", rec.Key.Bin, rec.Key.Path, shortSHA(rec.Key.SHA256))
		return werr
	})
}

func runCompanionUntrustCmd(cmd *cobra.Command, args []string) error {
	removed, err := config.ForgetCompanionConsent(args[0])
	if err != nil {
		return err
	}
	payload := companionDecision{Path: args[0], Forgot: removed}
	return emit(cmd, payload, func() error {
		return printForgetResult(cmd.OutOrStdout(), removed, args[0])
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
	rootCmd.AddCommand(companionCmd)
	companionCmd.AddCommand(companionListCmd)
	companionCmd.AddCommand(companionTrustCmd)
	companionCmd.AddCommand(companionUntrustCmd)
}
