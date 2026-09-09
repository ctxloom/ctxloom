package cli

import (
	"fmt"
	"io"
	"os"

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

const companionLong = `Inspect which companion binaries ctxloom may execute.

ctxloom discovers companions on your PATH (the shipped ltk / taskloom / reprise,
plus anything named ctxloom-companion-*) and EXECUTES each one to read the
context it contributes. Because any program on your PATH can claim one of those
names — including a transitive dependency in ./node_modules/.bin — a companion
runs only when its bytes carry a SIGNATURE from a publisher you trust.

That is the whole gate. A companion is executed when a detached '<binary>.sig'
beside it verifies, in the companion namespace, against a key in your
allowed_signers. Anything else is skipped with a warning: no signature, a
signature that does not cover those bytes, or a signer you have not authorized
to say "this may run here".

There is no command to approve or refuse one, and none is needed. To stop
ctxloom running a companion, take away what admits it: delete its '.sig', or
rename the binary so discovery no longer finds it. Both are ordinary file
operations, they need no record to be kept in step with them, and they are
visible in the place the decision actually lives.

Sign a companion where it is BUILT — 'just sign-binary <path>' in its own
repository — so the signature covers the bytes that were produced there.`

// Bare `ctxloom companion` shows the gate's answer for one binary. There is no
// record to list any more — admission is decided from the signature beside each
// binary, so the answer lives with the file rather than in a store this could
// print.
var companionCmd = groupNodeDefault(&cobra.Command{
	Use:   "companion",
	Short: "Inspect which companion binaries ctxloom may execute",
	Long:  companionLong,
}, "list")

// companionListCmd reports the gate's answer for every companion on PATH.
//
// It replaced a listing of RECORDED decisions, which no longer exist. The
// answer is now derived live from each binary's signature, which makes this
// strictly more useful: it reports what would happen on the next run rather
// than what someone once agreed to.
var companionListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show which discovered companions ctxloom would execute, and why",
	Long:  companionLong,
	Args:  cobra.NoArgs,
	RunE:  runCompanionListCmd,
}

// companionListing is the emitted shape of `companion list`.
type companionListing struct {
	Bin     string `json:"bin" yaml:"bin" toml:"bin"`
	Path    string `json:"path" yaml:"path" toml:"path"`
	Allowed bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Reason  string `json:"reason" yaml:"reason" toml:"reason"`
}

func runCompanionListCmd(cmd *cobra.Command, _ []string) error {
	root := loadConfigOrFallback(GetConfig, os.Stderr).TrustRoot()
	// prompt=false: merely LOOKING at companion state must never itself run a
	// foreign binary. AdmitCompanions decides without executing anything.
	admissions := config.AdmitCompanions(config.DiscoverCompanions(), root)
	out := make([]companionListing, 0, len(admissions))
	for _, a := range admissions {
		out = append(out, companionListing{Bin: a.Bin, Path: a.Path, Allowed: a.Allow, Reason: string(a.Reason)})
	}
	return emit(cmd, out, func() error {
		w := cmd.OutOrStdout()
		if len(out) == 0 {
			_, err := fmt.Fprintln(w, "no companions found on PATH")
			return err
		}
		for _, l := range out {
			verdict := "DENIED "
			if l.Allowed {
				verdict = "allowed"
			}
			if _, err := fmt.Fprintf(w, "%s %-10s %s (%s)\n", verdict, l.Bin, l.Path, l.Reason); err != nil {
				return err
			}
		}
		return nil
	})
}

var companionShowCmd = &cobra.Command{
	Use:   "show <path-or-name>",
	Short: "Show whether ctxloom would execute one companion binary, and why",
	Long:  companionLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runCompanionShowCmd,
}

// companionShow is the emitted shape of `companion show` — the read-one
// gap-fill (`companion` had `list` and no way to inspect a single binary's
// decision without scanning the whole listing by eye).
type companionShow struct {
	Bin     string `json:"bin" yaml:"bin" toml:"bin"`
	Path    string `json:"path,omitempty" yaml:"path,omitempty" toml:"path,omitempty"`
	SHA256  string `json:"sha256,omitempty" yaml:"sha256,omitempty" toml:"sha256,omitempty"`
	Allowed bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Reason  string `json:"reason" yaml:"reason" toml:"reason"`
}

// runCompanionShowCmd answers "would ctxloom execute this companion right
// now, and why" by running the EXACT SAME decision cascade the two real
// probes consult (config.AdmitCompanions) — never a second, hand-rolled
// re-derivation that could disagree with what actually happens at session
// start. prompt=false: merely LOOKING at companion state must never itself
// conjure a security question (the same posture `status`/`doctor` take).
func runCompanionShowCmd(cmd *cobra.Command, args []string) error {
	// The trust root is CONFIG-provided, so this shows the decision the real
	// probes would make on this machine rather than a second answer.
	root := loadConfigOrFallback(GetConfig, os.Stderr).TrustRoot()
	admissions := config.AdmitCompanions([]string{args[0]}, root)
	a := admissions[0]
	payload := companionShow{Bin: a.Bin, Path: a.Path, SHA256: a.SHA256, Allowed: a.Allow, Reason: string(a.Reason)}
	return emit(cmd, payload, func() error {
		return printCompanionShow(cmd.OutOrStdout(), payload)
	})
}

// printCompanionShow renders the text form: the resolved path (if any), the
// digest the decision would bind to, and the allow/reason verdict.
func printCompanionShow(w io.Writer, s companionShow) error {
	if s.Path == "" {
		_, err := fmt.Fprintf(w, "%s: not found (%s)\n", s.Bin, s.Reason)
		return err
	}
	status := "DENIED"
	if s.Allowed {
		status = "allowed"
	}
	if _, err := fmt.Fprintf(w, "%-8s %s\n", s.Bin, s.Path); err != nil {
		return err
	}
	if s.SHA256 != "" {
		if _, err := fmt.Fprintf(w, "  sha256: %s\n", s.SHA256); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "  %s (%s)\n", status, s.Reason)
	return err
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
	companionCmd.AddCommand(companionShowCmd)
}
