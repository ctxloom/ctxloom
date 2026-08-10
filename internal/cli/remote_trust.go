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
// was not provisionable at all. These three leaves mirror the companion exec
// gate's (`ctxloom companion trust|untrust|list`), over the same shared
// consent store.
//
// Deliberately NO MCP tools for any of this, matching every other trust
// surface: handing the agent the ability to approve its own publish
// destinations defeats the property the confirmation exists to provide.

const remoteTrustLong = `Inspect and change which remotes ctxloom may publish your signed content to.

The first publish to a given non-GitHub remote asks you once and records the
answer; every later publish to that same remote is silent. A session with
nobody to ask — an agent, an editor, a CI job, a piped command — REFUSES rather
than assuming yes.

That guards three mistakes: a typo or a stale remote in config (a typo is a
different URL, so it prompts), a committable .ctxloom/ config carrying a remote
you never chose, and an agent publishing on its own initiative.

'remote trust' is how a non-interactive host records the decision without a
prompt. 'remote untrust' undoes one, so the next publish asks again. Two spellings of one
repository (git@host:o/r.git and https://host/o/r) are ONE destination.

Decisions live in ~/.ctxloom/publish_remotes.yaml. There is deliberately no
committable project counterpart — "I meant to push MY signed content HERE" is
an answer only the person at the keyboard can give about their own credentials,
and a shareable one would pre-answer the question for everyone who clones.`

// remoteTrustedCmd lists the confirmations rather than the registry, so it is
// a leaf of its own: bare `ctxloom remote` answers "which remotes do I have",
// and a publish confirmation can name a URL no registry entry mentions.
var remoteTrustedCmd = &cobra.Command{
	Use:   "trusted",
	Short: "List remotes confirmed as publish destinations",
	Long:  remoteTrustLong,
	Args:  cobra.NoArgs,
	RunE:  runRemoteTrustedCmd,
}

var remoteTrustCmd = &cobra.Command{
	Use:   "trust <remote-url>",
	Short: "Record that ctxloom may publish to a remote",
	Long:  remoteTrustLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runRemoteTrustCmd,
}

var remoteUntrustCmd = &cobra.Command{
	Use:   "untrust <remote-url>",
	Short: "Drop the recorded decision for a remote (it is asked about again)",
	Long:  remoteTrustLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runRemoteUntrustCmd,
}

// remoteTrustListing is the emitted shape of `remote trusted`.
type remoteTrustListing struct {
	URL        string `json:"url" yaml:"url" toml:"url"`
	Identity   string `json:"identity" yaml:"identity" toml:"identity"`
	Allowed    bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	RecordedAt string `json:"recorded_at" yaml:"recorded_at" toml:"recorded_at"`
}

func runRemoteTrustedCmd(cmd *cobra.Command, _ []string) error {
	records, err := remote.ListPublishRemoteConsent()
	if err != nil {
		return err
	}
	out := make([]remoteTrustListing, 0, len(records))
	for _, r := range records {
		out = append(out, remoteTrustListing{
			URL:        r.Key.URL,
			Identity:   r.Key.Identity,
			Allowed:    r.Approved,
			RecordedAt: r.RecordedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return emit(cmd, out, func() error { return printRemoteTrustListings(cmd.OutOrStdout(), out) })
}

// printRemoteTrustListings renders the text form: the URL as the human typed
// it, which is what they will recognise.
func printRemoteTrustListings(w io.Writer, listings []remoteTrustListing) error {
	return printAdmissionListings(w, listings, "no publish destinations recorded",
		func(l remoteTrustListing) bool { return l.Allowed },
		func(l remoteTrustListing) string { return l.URL })
}

// remoteTrustDecision is the emitted shape of `allow` / `forget`.
type remoteTrustDecision struct {
	URL      string `json:"url" yaml:"url" toml:"url"`
	Identity string `json:"identity" yaml:"identity" toml:"identity"`
	Allowed  bool   `json:"allowed" yaml:"allowed" toml:"allowed"`
	Forgot   int    `json:"forgot" yaml:"forgot" toml:"forgot"`
}

func runRemoteTrustCmd(cmd *cobra.Command, args []string) error {
	rec, err := remote.SetPublishRemoteConsent(args[0], true)
	if err != nil {
		return err
	}
	payload := remoteTrustDecision{URL: rec.Key.URL, Identity: rec.Key.Identity, Allowed: true}
	return emit(cmd, payload, func() error {
		_, werr := fmt.Fprintf(cmd.OutOrStdout(),
			"allowed %s — ctxloom will publish there without asking\n", rec.Key.URL)
		return werr
	})
}

func runRemoteUntrustCmd(cmd *cobra.Command, args []string) error {
	removed, err := remote.ForgetPublishRemoteConsent(args[0])
	if err != nil {
		return err
	}
	payload := remoteTrustDecision{URL: args[0], Forgot: removed}
	return emit(cmd, payload, func() error {
		return printForgetResult(cmd.OutOrStdout(), removed, args[0])
	})
}

func init() {
	remoteCmd.AddCommand(remoteTrustedCmd)
	remoteCmd.AddCommand(remoteTrustCmd)
	remoteCmd.AddCommand(remoteUntrustCmd)
}
