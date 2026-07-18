package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// signer management is CLI-only (ADR 0024): none of this is exposed over
// MCP. Handing an agent a "signer add" tool would give it exactly the
// capability the signature-envelope design exists to deny it — an agent
// that could name its own key as trusted could forge publisher/reviewer
// trust for itself.

// signerCmd is kept as a working alias namespace (CLI-primary reorg plan,
// Decision 1/3: top-level `signer *` -> `trust signer *`). Its leaves below
// carry the real cobra Deprecated field (see trustSignerAddCmd etc. in this
// file for the new home); this parent stays undecorated — like memoryCmd — so
// marking it Deprecated doesn't hide the whole subtree from `--help`.
var signerCmd = &cobra.Command{
	Use:   "signer",
	Short: "Manage trusted signers (allowed_signers) — moved under `ctxloom trust signer`",
	Long: `Manage who ctxloom trusts to publish or approve/reject content: entries in
the ssh-keygen "allowed_signers" format (spec §7), layered over ctxloom's
own embedded trust root.

DEPRECATED: this top-level namespace moved to ` + "`ctxloom trust signer`" + `. Each
subcommand below still runs and prints a one-line pointer to its new home.

Trusting a signer is the single most consequential command in the signing
feature: everything that key ever publishes (or approves, for an
approve-namespace key) reaches your agent WITHOUT REVIEW, forever, until
you remove it. 'signer add' names that consequence and shows the
fingerprint you are supposed to verify out of band before continuing.`,
}

var (
	signerAddKey        string
	signerAddNamespaces []string
	signerAddComment    string
	signerAddProject    bool
	signerAddYes        bool
)

// signerAddLong is shared by signerAddCmd (deprecated top-level alias) and
// trustSignerAddCmd (the real home) so the two texts can never drift.
const signerAddLong = `Add a public key to your allowed_signers store, trusted for the given
namespace(s) (default: publish).

<principal> is an arbitrary identity string (spec §7.1) — an email, a team
name, an org name ("context@acme.com"), or a pipeline identity
("releases@ctxloom.dev"). It has no relationship to any account; it is
purely the label your allowed_signers file and 'signer list' display.

By default this writes to your USER store (~/.ctxloom/allowed_signers),
which follows you across every project. --project writes to the
COMMITTABLE project store (.ctxloom/allowed_signers) instead — the way a
team distributes "trust our lead's approval key" to everyone who clones.

Examples:
  ctxloom trust signer add context@acme.com --key ~/.ssh/acme-publish.pub
  ctxloom trust signer add lead@team.example --key lead.pub --namespace approve,reject --project`

var signerAddCmd = &cobra.Command{
	Use:        "add <principal>",
	Short:      "Trust a signer's public key",
	Long:       signerAddLong,
	Deprecated: signerAddDeprecation,
	Args:       cobra.ExactArgs(1),
	RunE:       runSignerAddCmd,
}

// signerAddDeprecation is the one-line pointer cobra prints whenever the
// legacy top-level `ctxloom signer add` still runs.
const signerAddDeprecation = "use `ctxloom trust signer add` instead"

// runSignerAddCmd is signerAddCmd/trustSignerAddCmd's shared RunE: a cobra
// command has exactly one parent, so the reorg's new home
// (trustSignerAddCmd) is a distinct command sharing this body rather than the
// same *cobra.Command (mirrors registerACPServerFlags' rationale in
// acp_cmd.go).
func runSignerAddCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return runSignerAdd(cmd, cfg, args[0], signerAddKey, signerAddNamespaces, signerAddComment, signerAddProject, signerAddYes)
}

// runSignerAdd is the testable body of `ctxloom signer add`: cfg is DI'd
// (a real config.Config over a temp project) and every flag value is an
// explicit parameter, so a test can drive the confirmation → write path
// without touching cobra's global flag vars or a real home directory.
func runSignerAdd(cmd *cobra.Command, cfg *config.Config, principal, keyArg string, namespaceAliases []string, comment string, project, assumeYes bool) error {
	keyInfo, err := operations.ResolveSignerKey(keyArg, nil, cmd.InOrStdin())
	if err != nil {
		return err
	}
	namespaces, err := operations.ResolveSignerNamespaces(namespaceAliases)
	if err != nil {
		return err
	}

	if !confirmSignerAdd(cmd, principal, keyInfo, namespaces, assumeYes) {
		fmt.Fprintln(cmd.OutOrStdout(), "not trusted (aborted)")
		return nil
	}

	res, err := operations.AddSigner(cfg, operations.AddSignerRequest{
		Principal:  principal,
		Key:        keyInfo,
		Namespaces: namespaces,
		Comment:    comment,
		Project:    project,
	})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Trusted %s for %s (%s) — wrote %s\n",
			principal, strings.Join(namespaces, ", "), res.Fingerprint, res.Path)
		return err
	})
}

// confirmSignerAdd names the real consequence of trusting a signer (spec
// §7.2) and shows the fingerprint the user is supposed to check out of
// band, mirroring the deleted `remote trust`'s consequence-naming
// confirmation. --yes and a non-interactive terminal (scripted/CI use, or
// a piped stdin already consumed by --key -) both skip the prompt and
// proceed — signer add is a deliberate, explicit CLI invocation either way,
// never a first-sight TOFU prompt (spec explicitly rejects TOFU; this
// confirmation is the opposite: an EXPLICIT add the user already chose to
// run, being asked to double check what they typed).
func confirmSignerAdd(cmd *cobra.Command, principal string, key operations.SignerKeyInfo, namespaces []string, assumeYes bool) bool {
	if assumeYes || !isInteractiveTerminal() {
		return true
	}
	consequence := signerConsequenceText(namespaces)
	fmt.Fprintf(os.Stderr, "\nTrust %s as a %s?\n\n  %s  (%s)\n\n  %s\n  Verify this fingerprint out of band before you continue.\n\n",
		principal, signerRoleWord(namespaces), key.Fingerprint, key.PublicKey.Type(), consequence)
	yes, err := promptYesNo("  [y/N] ")
	return err == nil && yes
}

// signerRoleWord picks the noun for the confirmation header: PUBLISHER when
// publish is among the trusted namespaces (the broadest, most dangerous
// grant — content AND executables, unreviewed), REVIEWER otherwise.
func signerRoleWord(namespaces []string) string {
	for _, ns := range namespaces {
		if ns == signing.NamespacePublish {
			return "PUBLISHER"
		}
	}
	return "REVIEWER"
}

// signerConsequenceText names what trusting this signer actually grants,
// worded differently for publish vs. approve/reject exactly as the spec
// requires (§7.2) — a publish grant and a delegated-review grant are
// different dangers and must not share one sentence.
func signerConsequenceText(namespaces []string) string {
	for _, ns := range namespaces {
		if ns == signing.NamespacePublish {
			return "Everything this signer ever publishes — text AND executables (MCP servers,\n" +
				"  hooks), now and in every future update — will reach your agent WITHOUT REVIEW."
		}
	}
	return "Everything this signer ever approves reaches your agent unreviewed —\n" +
		"  you are delegating your review decisions to them, forever."
}

var signerListCmd = &cobra.Command{
	Use:        "list",
	Short:      "List trusted signers",
	Deprecated: signerListDeprecation,
	RunE:       runSignerListCmd,
}

// signerListDeprecation is the one-line pointer cobra prints whenever the
// legacy top-level `ctxloom signer list` still runs.
const signerListDeprecation = "use `ctxloom trust signer list` instead"

func runSignerListCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	listings, err := operations.ListSigners(cfg, nil)
	if err != nil {
		return err
	}
	sort.Slice(listings, func(i, j int) bool {
		return signerSortKey(listings[i]) < signerSortKey(listings[j])
	})
	return emit(cmd, listings, func() error { return printSignerListings(cmd.OutOrStdout(), listings) })
}

func signerSortKey(l operations.SignerListing) string {
	principal := ""
	if len(l.Entry.Principals) > 0 {
		principal = l.Entry.Principals[0]
	}
	return principal + "\x00" + l.Source
}

func printSignerListings(w io.Writer, listings []operations.SignerListing) error {
	if len(listings) == 0 {
		_, err := fmt.Fprintln(w, "no trusted signers")
		return err
	}
	for _, l := range listings {
		principal := "?"
		if len(l.Entry.Principals) > 0 {
			principal = strings.Join(l.Entry.Principals, ",")
		}
		ns := "all namespaces"
		if l.Entry.Namespaces != nil {
			ns = strings.Join(l.Entry.Namespaces, ",")
			if ns == "" {
				ns = "no namespaces (untrusted for everything)"
			}
		}
		if _, err := fmt.Fprintf(w, "%-40s %-10s %-45s %s%s\n",
			principal, l.Source, ns, sshFingerprintOf(l), embeddedAnnotation(l)); err != nil {
			return err
		}
	}
	return nil
}

// embeddedAnnotation renders the trailing note for an "embedded" trust-root
// entry (oozy-plod (a)): always not-removable via this CLI, and — when a
// local suppression record exists (oozy-plod (b), `signer remove
// <embedded-principal>`) — that it is DISTRUSTED and no longer actually
// trusted despite still being listed. Visibility never regresses just
// because an entry was suppressed: an operator must be able to see both that
// the key exists and that they already acted on it. Empty for any
// non-embedded entry.
func embeddedAnnotation(l operations.SignerListing) string {
	if l.Source != "embedded" {
		return ""
	}
	if l.Suppressed {
		return "  (embedded, not removable — LOCALLY DISTRUSTED, no longer trusted)"
	}
	return "  (embedded, not removable)"
}

func sshFingerprintOf(l operations.SignerListing) string {
	if l.Entry.PublicKey == nil {
		return ""
	}
	return ssh.FingerprintSHA256(l.Entry.PublicKey)
}

var signerShowCmd = &cobra.Command{
	Use:        "show <principal>",
	Short:      "Show every trust-root entry for a principal",
	Args:       cobra.ExactArgs(1),
	Deprecated: signerShowDeprecation,
	RunE:       runSignerShowCmd,
}

// signerShowDeprecation is the one-line pointer cobra prints whenever the
// legacy top-level `ctxloom signer show` still runs.
const signerShowDeprecation = "use `ctxloom trust signer show` instead"

func runSignerShowCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	listings, err := operations.ShowSigner(cfg, args[0], nil)
	if err != nil {
		return err
	}
	return emit(cmd, listings, func() error { return printSignerListings(cmd.OutOrStdout(), listings) })
}

var signerRemoveProject bool

// signerRemoveLong is shared by signerRemoveCmd (deprecated top-level alias)
// and trustSignerRemoveCmd (the real home).
const signerRemoveLong = `Removes every entry for <principal> from your allowed_signers store (user
store by default; --project for the committable project store). This does
NOT reject any content that signer already published or approved — it
means "I will review this myself from now on", not "deny". Use
'ctxloom trust'/'ctxloom review --reject' to actually reject content.

<principal> naming ctxloom's OWN embedded release key is a special case: that
key is compiled into the binary and cannot be deleted by this command. Instead
this records a LOCAL distrust decision (only a new binary changes the
compiled-in bytes themselves) — content signed only by that key is withheld
from here on, on this machine or project.`

var signerRemoveCmd = &cobra.Command{
	Use:        "remove <principal>",
	Short:      "Remove a trusted signer",
	Long:       signerRemoveLong,
	Deprecated: signerRemoveDeprecation,
	Args:       cobra.ExactArgs(1),
	RunE:       runSignerRemoveCmd,
}

// signerRemoveDeprecation is the one-line pointer cobra prints whenever the
// legacy top-level `ctxloom signer remove` still runs.
const signerRemoveDeprecation = "use `ctxloom trust signer remove` instead"

func runSignerRemoveCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	res, err := operations.RemoveSigner(cfg, operations.RemoveSignerRequest{
		Principal: args[0],
		Project:   signerRemoveProject,
	})
	if err != nil {
		return err
	}
	return emit(cmd, res, func() error {
		switch {
		case res.EmbeddedSuppressed:
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"%s is ctxloom's embedded release key; it cannot be deleted (only a new binary changes it), "+
					"but it is now DISTRUSTED on this machine — content signed only by it will be withheld until reviewed (recorded in %s)\n",
				args[0], res.SuppressionPath)
			return err
		case res.Removed == 0:
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "no entry for %s in %s\n", args[0], res.Path)
			return err
		default:
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "removed %d entr%s for %s from %s\n",
				res.Removed, plural(res.Removed, "y", "ies"), args[0], res.Path)
			return err
		}
	})
}

// --- trust signer (real home, Decision 1/3: top-level `signer *` -> `trust
// signer *`) ---------------------------------------------------------------
//
// A cobra command has exactly one parent, so these are distinct *cobra.Command
// values from signerAddCmd/signerListCmd/signerShowCmd/signerRemoveCmd above,
// sharing the same RunE bodies and flag vars — the same shape as
// acp_agents_cmd.go's acpEntriesCmd/acpAgentsCmd pair.

var trustSignerCmd = &cobra.Command{
	Use:   "signer",
	Short: "Manage trusted signers (allowed_signers)",
	Long: `Manage who ctxloom trusts to publish or approve/reject content: entries in
the ssh-keygen "allowed_signers" format (spec §7), layered over ctxloom's
own embedded trust root.

Trusting a signer is the single most consequential command in the signing
feature: everything that key ever publishes (or approves, for an
approve-namespace key) reaches your agent WITHOUT REVIEW, forever, until
you remove it. 'trust signer add' names that consequence and shows the
fingerprint you are supposed to verify out of band before continuing.`,
}

var trustSignerAddCmd = &cobra.Command{
	Use:   "add <principal>",
	Short: "Trust a signer's public key",
	Long:  signerAddLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runSignerAddCmd,
}

var trustSignerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List trusted signers",
	RunE:  runSignerListCmd,
}

var trustSignerShowCmd = &cobra.Command{
	Use:   "show <principal>",
	Short: "Show every trust-root entry for a principal",
	Args:  cobra.ExactArgs(1),
	RunE:  runSignerShowCmd,
}

var trustSignerRemoveCmd = &cobra.Command{
	Use:   "remove <principal>",
	Short: "Remove a trusted signer",
	Long:  signerRemoveLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runSignerRemoveCmd,
}

func init() {
	// Deprecated top-level alias namespace (Decision 1/3): kept working
	// exactly as before.
	rootCmd.AddCommand(signerCmd)
	signerCmd.AddCommand(signerAddCmd)
	signerCmd.AddCommand(signerListCmd)
	signerCmd.AddCommand(signerShowCmd)
	signerCmd.AddCommand(signerRemoveCmd)

	signerAddCmd.Flags().StringVar(&signerAddKey, "key", "", "public key: a file path, '-' for stdin, or a literal authorized_keys line (required)")
	signerAddCmd.Flags().StringSliceVar(&signerAddNamespaces, "namespace", nil, "namespace(s) to trust this key for: publish|approve|reject (default: publish)")
	signerAddCmd.Flags().StringVar(&signerAddComment, "comment", "", "override the key's own comment")
	signerAddCmd.Flags().BoolVar(&signerAddProject, "project", false, "write to the committable project store (.ctxloom/allowed_signers) instead of the user store")
	signerAddCmd.Flags().BoolVarP(&signerAddYes, "yes", "y", false, "skip the confirmation prompt")
	_ = signerAddCmd.MarkFlagRequired("key")

	signerRemoveCmd.Flags().BoolVar(&signerRemoveProject, "project", false, "remove from the committable project store instead of the user store")

	// Real home: nested under `trust` (wired here since the flag vars and RunE
	// bodies are local to this file; trust.go adds trustSignerCmd itself under
	// trustCmd).
	trustSignerCmd.AddCommand(trustSignerAddCmd)
	trustSignerCmd.AddCommand(trustSignerListCmd)
	trustSignerCmd.AddCommand(trustSignerShowCmd)
	trustSignerCmd.AddCommand(trustSignerRemoveCmd)

	trustSignerAddCmd.Flags().StringVar(&signerAddKey, "key", "", "public key: a file path, '-' for stdin, or a literal authorized_keys line (required)")
	trustSignerAddCmd.Flags().StringSliceVar(&signerAddNamespaces, "namespace", nil, "namespace(s) to trust this key for: publish|approve|reject (default: publish)")
	trustSignerAddCmd.Flags().StringVar(&signerAddComment, "comment", "", "override the key's own comment")
	trustSignerAddCmd.Flags().BoolVar(&signerAddProject, "project", false, "write to the committable project store (.ctxloom/allowed_signers) instead of the user store")
	trustSignerAddCmd.Flags().BoolVarP(&signerAddYes, "yes", "y", false, "skip the confirmation prompt")
	_ = trustSignerAddCmd.MarkFlagRequired("key")

	trustSignerRemoveCmd.Flags().BoolVar(&signerRemoveProject, "project", false, "remove from the committable project store instead of the user store")
}
