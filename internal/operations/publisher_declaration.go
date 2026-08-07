// Who a REPOSITORY authorises to publish its bundles, and the refusal that
// stops `ctxloom bundle sign` signing as anybody else.
//
// The failure this exists to prevent, measured on ctxloom's own default-content
// repo: `ctxloom bundle sign --all` with no --key falls through to `git config
// user.signingkey`, which in an ordinary checkout resolves to whatever key the
// developer signs commits with. In a repo whose allowed_signers names a
// RELEASE identity instead, every signature that run writes is unverifiable —
// and the run prints "signed by ..." per bundle and exits 0. 43 of 45 bundles
// were re-signed that way. Nothing surfaces at the publisher: the failure lands
// in a CONSUMER, where ctxloom withholds the bundle and any profile inheriting
// it silently degrades.
package operations

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// DeclaredPublishersPath is where a repository declares who may publish it,
// relative to the project root: the ssh-keygen allowed_signers file at the
// location git-signing setups already use, and the one ctxloom's own publishing
// repos check in CI (.github/verify-signatures.sh reads this exact file).
//
// ONE location, deliberately. The ctxloom project trust store
// (.ctxloom/allowed_signers) is NOT consulted: it answers the opposite question
// — whose published content this project trusts to CONSUME — so a project that
// trusts a vendor's publish key would start refusing to sign its own bundles.
const DeclaredPublishersPath = ".github/allowed_signers"

// PublisherDeclaration is a repository's answer to "who may publish this?": the
// entries of its allowed_signers that EXPLICITLY name the publish namespace.
type PublisherDeclaration struct {
	// Path is the file the declaration was read from, for error messages.
	Path string
	// Principals are the identities the file names, in file order.
	Principals []string

	store *allowedsigners.Store
}

// ReadPublisherDeclaration reads root's publisher declaration, or nil when the
// repository makes none.
//
// "Makes none" covers both the absent file and — the case that keeps this from
// misfiring — a file that exists for COMMIT verification. Putting contributors'
// commit-signing keys in .github/allowed_signers is ordinary practice, and
// those entries either carry no namespaces= option (OpenSSH: valid for all
// namespaces) or name the git namespace. Enforcing against them would refuse to
// sign for every contributor not on the commit-signing list, which is a
// different set entirely.
//
// So the trigger is an EXPLICIT publish grant: at least one entry whose
// namespaces= option names publish.v1.ctxloom.dev. That is a repository saying
// "these keys publish ctxloom content from here" and nothing else says it — it
// is exactly what `ctxloom trust signer create` writes, and what ctxloom's own
// content repos already carry.
//
// A file that exists but cannot be READ or PARSED is an error, never "no
// declaration". Degrading to unenforced there would restore the silent-wrong-key
// failure at the exact moment the trust root is broken.
func ReadPublisherDeclaration(fs afero.Fs, root string) (*PublisherDeclaration, error) {
	if root == "" {
		return nil, nil
	}
	path := filepath.Join(root, filepath.FromSlash(DeclaredPublishersPath))
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the publisher declaration %s: %w", path, err)
	}
	store, parseErrs, err := allowedsigners.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse the publisher declaration %s: %w", path, err)
	}
	if len(parseErrs) > 0 {
		return nil, fmt.Errorf("the publisher declaration %s has an unparseable line (%d:%v) — "+
			"a trust root that cannot be read in full cannot say who may sign", path, parseErrs[0].Line, parseErrs[0].Err)
	}

	var declared []allowedsigners.Entry
	var principals []string
	for _, e := range store.Entries() {
		// Namespaces == nil is "all namespaces" (the commit-signing shape); only
		// an option that actually names publish counts as a declaration.
		if e.Namespaces == nil || !e.MatchesNamespace(signing.NamespacePublish) {
			continue
		}
		declared = append(declared, e)
		principals = append(principals, e.Principals...)
	}
	if len(declared) == 0 {
		return nil, nil
	}
	return &PublisherDeclaration{
		Path:       path,
		Principals: principals,
		store:      allowedsigners.NewStore(declared...),
	}, nil
}

// Authorizes reports whether key is one the declaration names for publishing,
// through the SAME decision function every verifier uses
// (allowedsigners.Store.TrustedForNamespace), so the publisher's own check and
// the consumer's cannot disagree about validity windows or cert-authority
// entries.
func (d *PublisherDeclaration) Authorizes(key ssh.PublicKey, now time.Time) bool {
	if d == nil {
		return true
	}
	return d.store.TrustedForNamespace(key, signing.NamespacePublish, now).Trusted
}

// AuthorizePublisher refuses a signing run whose key the repository does not
// authorise to publish, BEFORE a byte is written.
//
// source describes where the key came from ("git config user.signingkey", "--key",
// ...) and may be empty; the fingerprint is always named. Both halves are in the
// message on purpose: a publisher who is told only "wrong key" cannot tell which
// of the identities in their agent answered, and one told only the fingerprint
// cannot tell who they were supposed to be.
func AuthorizePublisher(cfg *config.Config, fs afero.Fs, signer ssh.Signer, source string) error {
	if signer == nil || cfg == nil {
		return nil
	}
	roots := cfg.GetAppPaths()
	if len(roots) == 0 {
		return nil
	}
	declaration, err := ReadPublisherDeclaration(getFS(fs), filepath.Dir(roots[0]))
	if err != nil {
		return err
	}
	if declaration.Authorizes(signer.PublicKey(), time.Now()) {
		return nil
	}

	from := ""
	if source != "" {
		from = ", from " + source
	}
	return fmt.Errorf("ctxloom bundle sign: refusing to sign as a key this repository does not authorise to publish\n\n"+
		"  would have signed with:  %s%s\n"+
		"  repository authorises:   %s\n"+
		"  declared in:             %s\n\n"+
		"Signing anyway produces signatures that verify for nobody: this command would report success, "+
		"and every consumer would withhold the bundle while any profile inheriting it silently degraded. "+
		"Sign with the declared key instead:\n\n"+
		"  ctxloom bundle sign --key <fingerprint | path to the .pub | ssh-agent comment>\n"+
		"  ctxloom config set sign.key <same>",
		ssh.FingerprintSHA256(signer.PublicKey()), from,
		strings.Join(declaration.Principals, ", "),
		declaration.Path)
}
