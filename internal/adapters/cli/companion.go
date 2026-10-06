package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
)

// pinAdmittedCompanions is the isolation layer's companion pin
// (isolation.SetCompanionPin): the admitted companions, decided against the
// allow store, copied where a host launch puts them first on the engine's
// PATH. Resolved per launch, so a companion updated mid-session is re-admitted
// rather than served from a stale decision.
func pinAdmittedCompanions() (string, error) {
	store, err := paths.HomeCompanionPinDir()
	if err != nil {
		return "", err
	}
	return companions.PinAdmittedCompanions(store, companions.LoadAllowed())
}

// The exec-allow CLI. Companions are DISCOVERED, not configured — the
// first-party names plus every `ctxloom-companion-*` on $PATH — and reading a
// companion's loadout means EXECUTING it, so a human allows each binary once,
// by path and by hash, here.
//
// Deliberately NO MCP tools for any of this: handing the agent the ability to
// allow the binaries that run alongside it defeats the property the allow
// exists to provide.

const companionLong = `Inspect and decide which companion binaries ctxloom may execute.

ctxloom discovers companions on your PATH (the shipped ltk / taskloom / reprise,
plus anything named ctxloom-companion-*) and EXECUTES each one to read the
context it contributes. Because any program on your PATH can claim one of those
names — including a transitive dependency in ./node_modules/.bin — a companion
runs only when you have ALLOWED it.

An allow is a record of the binary's resolved path and the SHA-256 of its
bytes, kept in your own ~/.ctxloom/companion_allow.yaml. A companion whose path
has no record is skipped as not-allowed; one whose bytes changed since it was
allowed (a rebuild, an upgrade, a swap) is skipped as hash-changed, and the
warning names the old and new hash so you can tell which.

  ctxloom companion allow <path|name>          show what would be allowed
  ctxloom companion allow <path|name> --yes    allow it
  ctxloom companion forget <path|name> --yes   withdraw it`

// Bare `ctxloom companion` lists the gate's answer for every companion on PATH.
var companionCmd = groupNodeDefault(&cobra.Command{
	Use:   "companion",
	Short: "Inspect and decide which companion binaries ctxloom may execute",
	Long:  companionLong,
}, "list")

// companionListCmd reports the gate's answer for every companion on PATH: what
// would happen on the next run, derived from the binary on disk and the allow
// store together.
var companionListCmd = &cobra.Command{
	Use:     "list",
	Short:   "Show which discovered companions ctxloom would execute, and why",
	Long:    companionLong,
	Example: `  ctxloom companion list`,
	Args:    cobra.NoArgs,
	RunE:    runCompanionListCmd,
}

// companionListing is the emitted shape of `companion list`.
type companionListing struct {
	Bin     string `json:"bin" yaml:"bin" toml:"bin"`
	Path    string `json:"path" yaml:"path" toml:"path"`
	Allowed bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Reason  string `json:"reason" yaml:"reason" toml:"reason"`
}

func runCompanionListCmd(cmd *cobra.Command, _ []string) error {
	// Merely LOOKING at companion state must never itself run a foreign
	// binary. AdmitCompanions decides without executing anything.
	admissions := companions.AdmitCompanions(companions.DiscoverCompanions(), companions.LoadAllowed())
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
			if _, err := fmt.Fprintf(w, "%-13s %-10s %s\n", l.Reason, l.Bin, l.Path); err != nil {
				return err
			}
		}
		return nil
	})
}

var companionShowCmd = &cobra.Command{
	Use:     "show <path-or-name>",
	Short:   "Show whether ctxloom would execute one companion binary, and why",
	Long:    companionLong,
	Example: `  ctxloom companion show ltk`,
	Args:    cobra.ExactArgs(1),
	RunE:    runCompanionShowCmd,
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
	Detail  string `json:"detail,omitempty" yaml:"detail,omitempty" toml:"detail,omitempty"`
}

// runCompanionShowCmd answers "would ctxloom execute this companion right
// now, and why" by running the EXACT SAME decision the two real probes consult
// (companions.AdmitCompanions) — never a second, hand-rolled re-derivation
// that could disagree with what actually happens at session start.
func runCompanionShowCmd(cmd *cobra.Command, args []string) error {
	admissions := companions.AdmitCompanions([]string{args[0]}, companions.LoadAllowed())
	a := admissions[0]
	payload := companionShow{Bin: a.Bin, Path: a.Path, SHA256: a.SHA256, Allowed: a.Allow, Reason: string(a.Reason), Detail: a.Detail}
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
	if _, err := fmt.Fprintf(w, "%-8s %s\n", s.Bin, s.Path); err != nil {
		return err
	}
	if s.SHA256 != "" {
		if _, err := fmt.Fprintf(w, "  sha256: %s\n", s.SHA256); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  %s\n", s.Reason); err != nil {
		return err
	}
	if s.Detail != "" {
		if _, err := fmt.Fprintf(w, "  %s\n", s.Detail); err != nil {
			return err
		}
	}
	if !s.Allowed {
		_, err := fmt.Fprintf(w, "  to allow it: ctxloom companion allow %s --yes\n", s.Path)
		return err
	}
	return nil
}

var companionAllowYes bool

var companionAllowCmd = &cobra.Command{
	Use:   "allow <path|name>",
	Short: "Allow ctxloom to execute one companion binary, as its bytes are now",
	Long:  companionLong,
	Example: `  ctxloom companion allow ltk
  ctxloom companion allow ~/go/bin/ltk --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runCompanionAllowCmd,
}

// runCompanionAllowCmd previews the record an allow would write — the resolved
// path and its hash, or the hash change when the path is already allowed for
// other bytes — and writes it only with --yes.
func runCompanionAllowCmd(cmd *cobra.Command, args []string) error {
	res, err := operations.AllowCompanion(cmd.Context(), afero.NewOsFs(),
		operations.CompanionAllowRequest{PathOrName: args[0], Apply: companionAllowYes})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "%-8s %s\n", res.Key.Bin, res.Key.Path)
		if res.Previous != nil {
			fmt.Fprintf(w, "  hash changed: %s -> %s\n", res.Previous.SHA256, res.Key.SHA256)
		} else {
			fmt.Fprintf(w, "  sha256: %s\n", res.Key.SHA256)
		}
		if res.Applied {
			fmt.Fprintln(w, "Allowed.")
			return nil
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Nothing was recorded. Re-run with --yes to allow it:")
		fmt.Fprintf(w, "  ctxloom companion allow %s --yes\n", res.Key.Path)
		return nil
	})
}

var companionForgetYes bool

var companionForgetCmd = &cobra.Command{
	Use:     "forget <path|name>",
	Short:   "Withdraw the allow for one companion binary",
	Long:    companionLong,
	Example: `  ctxloom companion forget ~/go/bin/ltk --yes`,
	Args:    cobra.ExactArgs(1),
	RunE:    runCompanionForgetCmd,
}

// runCompanionForgetCmd reports the allow records it would drop and drops them
// only with --yes, in the shared remove-preview shape.
func runCompanionForgetCmd(cmd *cobra.Command, args []string) error {
	res, err := operations.ForgetCompanion(cmd.Context(), afero.NewOsFs(), args[0], companionForgetYes)
	if err != nil {
		return err
	}
	detail := make([]string, 0, len(res.Records))
	for _, k := range res.Records {
		detail = append(detail, fmt.Sprintf("%s %s (sha256 %s)", k.Bin, k.Path, k.SHA256))
	}
	target := "the companion allow for " + args[0]
	if !res.Applied {
		applyCmd := fmt.Sprintf("ctxloom companion forget %s --yes", args[0])
		return emit(cmd, newRemovePreviewResult(target, detail, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, detail, applyCmd)
			return nil
		})
	}
	return emit(cmd, res, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Forgot %s.\n", target)
		return nil
	})
}

// shortSHA abbreviates a hex digest for human display. Full digests are in
// --format json; a 64-char hex string in a status line is noise a human cannot
// check by eye anyway.
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
	companionCmd.AddCommand(companionAllowCmd)
	companionCmd.AddCommand(companionForgetCmd)
	companionAllowCmd.Flags().BoolVarP(&companionAllowYes, yesFlagName, "y", false, "Record the allow this invocation would report (default: report only)")
	companionForgetCmd.Flags().BoolVarP(&companionForgetYes, yesFlagName, "y", false, "Apply the removal this invocation would report (default: report only)")
}
