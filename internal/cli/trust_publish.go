package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// The publish-destination CLI: the scriptable half of the trust-on-first-use
// decision `ctxloom` makes interactively the first time it publishes to a
// given generic-git remote.
//
// WHY IT EXISTS. The confirmation shipped with a prompt and nothing else, so
// the only documented way to record one was "run the same publish once from an
// interactive terminal and answer yes". A CI runner and an agent host have no
// interactive terminal — that is the state the gate exists to refuse — so for
// them that instruction named an action they could not perform, and the gate
// was not provisionable at all. These three leaves are the same three the
// companion exec gate already had (`ctxloom trust companion ...`), over the
// same shared consent store.
//
// Deliberately NO MCP tools for any of this, matching every other trust
// surface: handing the agent the ability to approve its own publish
// destinations defeats the property the confirmation exists to provide.

const trustPublishLong = `Inspect and change which remotes ctxloom may publish your signed content to.

The first publish to a given non-GitHub remote asks you once and records the
answer; every later publish to that same remote is silent. A session with
nobody to ask — an agent, an editor, a CI job, a piped command — REFUSES rather
than assuming yes.

That guards three mistakes: a typo or a stale remote in config (a typo is a
different URL, so it prompts), a committable .ctxloom/ config carrying a remote
you never chose, and an agent publishing on its own initiative.

'allow' is how a non-interactive host records the decision without a prompt.
'forget' undoes one, so the next publish asks again. Two spellings of one
repository (git@host:o/r.git and https://host/o/r) are ONE destination.

Decisions live in ~/.ctxloom/publish_remotes.yaml. There is deliberately no
committable project counterpart — "I meant to push MY signed content HERE" is
an answer only the person at the keyboard can give about their own credentials,
and a shareable one would pre-answer the question for everyone who clones.`

var trustPublishCmd = groupNode(&cobra.Command{
	Use:   "publish",
	Short: "Inspect and change which remotes ctxloom may publish to",
	Long:  trustPublishLong,
})

var trustPublishListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded publish-destination decisions",
	Long:  trustPublishLong,
	Args:  cobra.NoArgs,
	RunE:  runTrustPublishListCmd,
}

var trustPublishAllowCmd = &cobra.Command{
	Use:   "allow <remote-url>",
	Short: "Record that ctxloom may publish to a remote",
	Long:  trustPublishLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runTrustPublishAllowCmd,
}

var trustPublishForgetCmd = &cobra.Command{
	Use:   "forget <remote-url>",
	Short: "Drop the recorded decision for a remote (it is asked about again)",
	Long:  trustPublishLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runTrustPublishForgetCmd,
}

// trustPublishListing is the emitted shape of `trust publish list`.
type trustPublishListing struct {
	URL        string `json:"url" yaml:"url" toml:"url"`
	Identity   string `json:"identity" yaml:"identity" toml:"identity"`
	Allowed    bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	RecordedAt string `json:"recorded_at" yaml:"recorded_at" toml:"recorded_at"`
}

func runTrustPublishListCmd(cmd *cobra.Command, _ []string) error {
	records, err := remote.ListPublishRemoteConsent()
	if err != nil {
		return err
	}
	out := make([]trustPublishListing, 0, len(records))
	for _, r := range records {
		out = append(out, trustPublishListing{
			URL:        r.Key.URL,
			Identity:   r.Key.Identity,
			Allowed:    r.Approved,
			RecordedAt: r.RecordedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return emit(cmd, out, func() error { return printTrustPublishListings(cmd.OutOrStdout(), out) })
}

// printTrustPublishListings renders the text form. An empty store says so in
// words rather than printing nothing: "no output" and "nothing recorded" are
// the same pixels and very different facts.
func printTrustPublishListings(w io.Writer, listings []trustPublishListing) error {
	if len(listings) == 0 {
		_, err := fmt.Fprintln(w, "no publish destinations recorded")
		return err
	}
	for _, l := range listings {
		state := "allowed"
		if !l.Allowed {
			state = "declined"
		}
		if _, err := fmt.Fprintf(w, "%-10s %s\n", state, l.URL); err != nil {
			return err
		}
	}
	return nil
}

// trustPublishDecision is the emitted shape of `allow` / `forget`.
type trustPublishDecision struct {
	URL      string `json:"url" yaml:"url" toml:"url"`
	Identity string `json:"identity" yaml:"identity" toml:"identity"`
	Allowed  bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Forgot   int    `json:"forgot" yaml:"forgot" toml:"forgot"`
}

func runTrustPublishAllowCmd(cmd *cobra.Command, args []string) error {
	rec, err := remote.SetPublishRemoteConsent(args[0], true)
	if err != nil {
		return err
	}
	payload := trustPublishDecision{URL: rec.Key.URL, Identity: rec.Key.Identity, Allowed: true}
	return emit(cmd, payload, func() error {
		_, werr := fmt.Fprintf(cmd.OutOrStdout(),
			"allowed %s — ctxloom will publish there without asking\n", rec.Key.URL)
		return werr
	})
}

func runTrustPublishForgetCmd(cmd *cobra.Command, args []string) error {
	removed, err := remote.ForgetPublishRemoteConsent(args[0])
	if err != nil {
		return err
	}
	payload := trustPublishDecision{URL: args[0], Forgot: removed}
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

func init() {
	trustCmd.AddCommand(trustPublishCmd)
	trustPublishCmd.AddCommand(trustPublishListCmd)
	trustPublishCmd.AddCommand(trustPublishAllowCmd)
	trustPublishCmd.AddCommand(trustPublishForgetCmd)
}
