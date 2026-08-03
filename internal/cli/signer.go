package cli

import (
	"fmt"
	"io"
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

var (
	signerAddKey        string
	signerAddNamespaces []string
	signerAddComment    string
	signerAddProject    bool
	signerAddYes        bool
)

// signerCreateLong documents `ctxloom trust signer create`.
const signerCreateLong = `Add a public key to your allowed_signers store, trusted for the given
namespace(s) (default: publish).

<principal> is an arbitrary identity string (spec §7.1) — an email, a team
name, an org name ("context@acme.com"), or a pipeline identity
("releases@ctxloom.dev"). It has no relationship to any account; it is
purely the label your allowed_signers file and 'trust signer list' display.

By default this writes to your USER store (~/.ctxloom/allowed_signers),
which follows you across every project. --project writes to the
COMMITTABLE project store (.ctxloom/allowed_signers) instead — the way a
team distributes "trust our lead's approval key" to everyone who clones.

Examples:
  ctxloom trust signer create context@acme.com --key ~/.ssh/acme-publish.pub
  ctxloom trust signer create lead@team.example --key lead.pub --namespace approve,reject --project`

// runSignerAddCmd is trustSignerCreateCmd's RunE.
func runSignerAddCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return runSignerAdd(cmd, cfg, args[0], signerAddKey, signerAddNamespaces, signerAddComment, signerAddProject, signerAddYes)
}

// runSignerAdd is the testable body of `ctxloom trust signer create`: cfg is DI'd
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
// proceed — trusting a signer is a deliberate, explicit CLI invocation either way,
// never a first-sight TOFU prompt (spec explicitly rejects TOFU; this
// confirmation is the opposite: an EXPLICIT add the user already chose to
// run, being asked to double check what they typed).
func confirmSignerAdd(cmd *cobra.Command, principal string, key operations.SignerKeyInfo, namespaces []string, assumeYes bool) bool {
	if assumeYes || !isInteractiveTerminal() {
		return true
	}
	return promptSignerAdd(cmd, principal, key, namespaces)
}

// promptSignerAdd renders the consequence block and reads the answer. It is
// split out of confirmSignerAdd — whose only other job is the --yes/no-TTY
// gate — because the gate made the prompt untestable: in any test process
// isInteractiveTerminal() is false, so every existing test took the skip path
// and the most consequential text in the product had nothing asserting it is
// shown at all. It writes to cmd.ErrOrStderr() rather than
// os.Stderr for the same reason; in production those are the same descriptor.
func promptSignerAdd(cmd *cobra.Command, principal string, key operations.SignerKeyInfo, namespaces []string) bool {
	consequence := signerConsequenceText(namespaces)
	fmt.Fprintf(cmd.ErrOrStderr(), "\nTrust %s as a %s?\n\n  %s  (%s)\n\n  %s\n  Verify this fingerprint out of band before you continue.\n\n",
		principal, signerRoleWord(namespaces), key.Fingerprint, key.PublicKey.Type(), consequence)
	yes, err := promptYesNo("  [y/N] ")
	return err == nil && yes
}

// hasPublishNamespace reports whether a grant includes the publish namespace —
// the one conditional every publish-vs-review distinction in the confirmation
// turns on. signerRoleWord and signerConsequenceText each used to
// open-code this scan, so no test between them could catch the two disagreeing;
// they now share this single answer.
func hasPublishNamespace(namespaces []string) bool {
	for _, ns := range namespaces {
		if ns == signing.NamespacePublish {
			return true
		}
	}
	return false
}

// signerRoleWord picks the noun for the confirmation header: PUBLISHER when
// publish is among the trusted namespaces (the broadest, most dangerous
// grant — content AND executables, unreviewed), REVIEWER otherwise.
func signerRoleWord(namespaces []string) string {
	if hasPublishNamespace(namespaces) {
		return "PUBLISHER"
	}
	return "REVIEWER"
}

// signerConsequenceText names what trusting this signer actually grants,
// worded differently for publish vs. approve/reject exactly as the spec
// requires (§7.2) — a publish grant and a delegated-review grant are
// different dangers and must not share one sentence.
func signerConsequenceText(namespaces []string) string {
	if hasPublishNamespace(namespaces) {
		return "Everything this signer ever publishes — text AND executables (MCP servers,\n" +
			"  hooks), now and in every future update — will reach your agent WITHOUT REVIEW."
	}
	return "Everything this signer ever approves reaches your agent unreviewed —\n" +
		"  you are delegating your review decisions to them, forever."
}

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
		if l.Unreadable != "" {
			// Not an entry — a line in this store that could not be read.
			// Shown rather than omitted so the listing cannot pass a
			// silently-shortened trust root off as the whole one.
			if _, err := fmt.Fprintf(w, "%-40s %-10s %s\n", "(unreadable)", l.Source, l.Unreadable); err != nil {
				return err
			}
			continue
		}
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
// entry: always not-removable via this CLI, and — when a local suppression
// record exists (`trust signer delete <embedded-principal>`) — that it is
// DISTRUSTED and no longer actually trusted despite still being listed.
// Visibility never regresses just
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

// signerDeleteLong documents `ctxloom trust signer delete`.
const signerDeleteLong = `Removes every entry for <principal> from your allowed_signers store (user
store by default; --project for the committable project store). This does
NOT reject any content that signer already published or approved — it
means "I will review this myself from now on", not "deny". Use
'ctxloom trust reject'/'ctxloom review --reject' to actually reject content.

<principal> naming ctxloom's OWN embedded release key is a special case: that
key is compiled into the binary and cannot be deleted by this command. Instead
this records a LOCAL distrust decision (only a new binary changes the
compiled-in bytes themselves) — content signed only by that key is withheld
from here on, on this machine or project.`

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

// --- trust signer: the canonical spine over the allowed_signers store -------

var trustSignerCmd = groupNode(&cobra.Command{
	Use:   "signer",
	Short: "Manage trusted signers (allowed_signers)",
	Long: `Manage who ctxloom trusts to publish or approve/reject content: entries in
the ssh-keygen "allowed_signers" format (spec §7), layered over ctxloom's
own embedded trust root.

Trusting a signer is the single most consequential command in the signing
feature: everything that key ever publishes (or approves, for an
approve-namespace key) reaches your agent WITHOUT REVIEW, forever, until
you remove it. 'trust signer create' names that consequence and shows the
fingerprint you are supposed to verify out of band before continuing.`,
})

var trustSignerCreateCmd = &cobra.Command{
	Use:   "create <principal>",
	Short: "Trust a signer's public key",
	Long:  signerCreateLong,
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

var trustSignerDeleteCmd = &cobra.Command{
	Use:   "delete <principal>",
	Short: "Delete a trusted signer",
	Long:  signerDeleteLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runSignerRemoveCmd,
}

func init() {
	// Wired here since the flag vars and RunE bodies are local to this file;
	// trust.go adds trustSignerCmd itself under trustCmd.
	trustSignerCmd.AddCommand(trustSignerListCmd)
	trustSignerCmd.AddCommand(trustSignerShowCmd)
	trustSignerCmd.AddCommand(trustSignerCreateCmd)
	trustSignerCmd.AddCommand(trustSignerDeleteCmd)

	trustSignerCreateCmd.Flags().StringVar(&signerAddKey, "key", "", "public key: a file path, '-' for stdin, or a literal authorized_keys line (required)")
	trustSignerCreateCmd.Flags().StringSliceVar(&signerAddNamespaces, "namespace", nil, "namespace(s) to trust this key for: publish|approve|reject (default: publish)")
	trustSignerCreateCmd.Flags().StringVar(&signerAddComment, "comment", "", "override the key's own comment")
	trustSignerCreateCmd.Flags().BoolVar(&signerAddProject, "project", false, "write to the committable project store (.ctxloom/allowed_signers) instead of the user store")
	trustSignerCreateCmd.Flags().BoolVarP(&signerAddYes, "yes", "y", false, "skip the confirmation prompt")
	_ = trustSignerCreateCmd.MarkFlagRequired("key")

	trustSignerDeleteCmd.Flags().BoolVar(&signerRemoveProject, "project", false, "delete from the committable project store instead of the user store")
}
