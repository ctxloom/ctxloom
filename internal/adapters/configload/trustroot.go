package configload

import (
	"bufio"
	_ "embed"
	"os"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// embeddedAllowedSigners is ctxloom's compiled-in trust root in the real
// allowed_signers format, parsed by the same parser every other location uses
// so the format has a single source of truth. See the file for what it grants.
//
//go:embed embedded_signers.allowed_signers
var embeddedAllowedSigners string

// EmbeddedSigners returns ctxloom's own compiled-in trust root: the public
// key(s) of the ctxloom release / bundle-publishing pipeline (spec §7, location
// 1). Trusting the ctxloom binary trusts what it ships.
//
// The embedded content is a fixed constant, so a parse failure here is a build
// bug caught by TestEmbeddedSigners, not a runtime condition; any unparsable
// line is dropped (toward LESS trust — an unrecognized key trusts nothing).
//
// It is a READ view — every entry, unfiltered, including any this machine has
// locally suppressed (see SuppressedEmbeddedPrincipals). Callers outside this
// package (operations.ListSigners/ShowSigner) enumerate the compiled-in
// entries through it rather than duplicating the //go:embed.
// allowedsigners.Store exposes no mutator, so the embedded root is visible
// without becoming editable.
func EmbeddedSigners() *allowedsigners.Store {
	store, _, err := allowedsigners.Parse(strings.NewReader(embeddedAllowedSigners))
	if err != nil || store == nil {
		return allowedsigners.NewStore()
	}
	return store
}

// signerFiles is where one generation's trust root is read from: the
// filesystem, the app directories whose project stores join the user ones, and
// the reporter a degraded read is announced through. Sources.TrustPorts builds
// it from the Config it is building a generation for (signerFilesOf).
type signerFiles struct {
	fs       afero.Fs
	appPaths []string
	rep      report.Reporter
}

// signerFilesOf is the signer store set cfg's generation reads.
func signerFilesOf(cfg *config.Config) *signerFiles {
	return &signerFiles{fs: cfg.FS(), appPaths: cfg.GetAppPaths(), rep: report.To(cfg.Reporter())}
}

// getFS is the filesystem the stores are read through; nil means the OS.
func (c *signerFiles) getFS() afero.Fs {
	if c.fs != nil {
		return c.fs
	}
	return afero.NewOsFs()
}

// trustStore is the union of every allowed_signers location: ctxloom's
// embedded defaults (MINUS any locally suppressed entry — see
// filterSuppressedPrincipals), the user store (~/.ctxloom/allowed_signers),
// and the project store (.ctxloom/allowed_signers). All locations are unioned
// — a key counts for the namespaces it lists wherever it is listed — because
// precedence lives in the DECISION FUNCTION, never in the filesystem (spec
// §7, §9.2).
//
// It never fails. A missing store is simply no keys; an unreadable or malformed
// one warns and contributes whatever lines did parse. Every one of those
// degradations moves toward LESS exposure, never more: fewer trusted keys means
// more content is unsigned, and unsigned content is withheld until a human
// reviews it (spec §10.5).
//
// "Never fails" is not "never lost anything": a location that existed but
// could not be read is reported (report.KindTrust) as it is skipped and rides
// on the union as a failed source, so a silently-shortened root is never
// presented as the whole one.
//
// Sources.TrustPorts hands it out as the port (trust.TrustRoot), once per
// generation; every consumer reads it back as Config.Trust().Root().
func (c *signerFiles) trustStore() *allowedsigners.Store {
	fs := c.getFS()
	stores := []*allowedsigners.Store{c.embeddedSignersTrusted()}
	for _, path := range c.allowedSignersPaths() {
		stores = append(stores, c.parseAllowedSigners(fs, path))
	}
	return allowedsigners.Union(stores...)
}

// embeddedSignersTrusted returns the embedded trust root minus any entry
// whose principal this machine has locally distrusted. The compiled-in
// bytes never change — nothing here edits
// embedded_signers.allowed_signers or the binary — this filters the STORE
// value each time a generation's root is built, so a suppression
// (`signer remove <embedded-principal>`) takes effect from the next generation
// (config.Owner.Reload) with nothing to invalidate.
func (c *signerFiles) embeddedSignersTrusted() *allowedsigners.Store {
	store := EmbeddedSigners()
	suppressed, unreadable := c.suppressedEmbeddedPrincipals()
	if unreadable {
		// A revocation list that cannot be read cannot be shown to be empty.
		// Trust nothing first-party rather than re-grant what may have been
		// revoked — see readPrincipalLines.
		return allowedsigners.NewStore()
	}
	if len(suppressed) == 0 {
		return store
	}
	return filterSuppressedPrincipals(store, suppressed)
}

// filterSuppressedPrincipals returns a copy of store with every entry whose
// Principals list contains a suppressed principal removed. This is the actual
// SUBTRACTION primitive needed here: allowedsigners.Store's decision is
// purely additive with no negative-entry concept, and allowedsigners.Union
// only ever concatenates — so this is new machinery, not
// a reuse of the existing content-item REJECTION mechanism (that beats a
// trusted publisher at the per-item decision, EffectiveTrust step 1; this
// instead removes a KEY from the trust root itself, upstream of every
// decision that would otherwise consult it).
func filterSuppressedPrincipals(store *allowedsigners.Store, suppressed map[string]bool) *allowedsigners.Store {
	if store == nil || len(suppressed) == 0 {
		return store
	}
	var kept []allowedsigners.Entry
	for _, e := range store.Entries() {
		if e.MatchesAnyPrincipal(suppressed) {
			continue
		}
		kept = append(kept, e)
	}
	return allowedsigners.NewStore(kept...)
}

// SuppressedEmbeddedPrincipals returns the set of embedded-signer principals
// this machine has locally DISTRUSTED — the union of the user and project
// distrusted_signers files (paths.HomeDistrustedSignersPath /
// paths.DistrustedSignersPath), one principal per line, blank/`#`-comment
// lines skipped. This is the SAME two-location shape as allowed_signers, so a
// team can commit a project-wide distrust decision exactly like they commit a
// project-wide trust decision (`signer remove <embedded-principal>
// --project`).
//
// Never fails: a missing file simply contributes nothing. This reporting form
// answers only "which principals are named", which is what ListSigners and
// ShowSigner display; the trust root itself uses
// signerFiles.suppressedEmbeddedPrincipals, which also reports whether any of
// those files was unreadable.
//
// Read/write of this store is written by operations.RemoveSigner (the only
// production mutator — see docs/trust-model.md's CLI-only signer-management
// boundary, ADR 0024); this is the READ side the trust root and
// ListSigners/ShowSigner both consult.
func SuppressedEmbeddedPrincipals(cfg *config.Config) map[string]bool {
	out, _ := signerFilesOf(cfg).suppressedEmbeddedPrincipals()
	return out
}

// suppressedEmbeddedPrincipals is SuppressedEmbeddedPrincipals plus the fact a
// display surface can ignore and a trust decision cannot: whether any
// suppression file EXISTS but could not be read in full. A caller that sees
// true has no evidence the set it was handed is complete, and must not treat
// it as a complete list of what was revoked.
func (c *signerFiles) suppressedEmbeddedPrincipals() (map[string]bool, bool) {
	fs := c.getFS()
	out := map[string]bool{}
	unreadable := false
	for _, path := range c.distrustedSignersPaths() {
		principals, bad := readPrincipalLines(c.rep, fs, path)
		if bad {
			unreadable = true
		}
		for principal := range principals {
			out[principal] = true
		}
	}
	return out, unreadable
}

// signerStorePaths lists one signer store's on-disk locations in union order
// (user, then project), skipping the project path when it resolves to the same
// file as the user one (a home-rooted .ctxloom, where both names denote one
// file). allowed_signers and distrusted_signers are the SAME two-location
// shape and differ only in which pair of path builders names the file, so the
// shape lives here once: a change to the union order or the dedup rule cannot
// reach one store and miss its counterpart.
func (c *signerFiles) signerStorePaths(homePath func() (string, error), projectPath func(string) string) []string {
	var out []string
	if home, err := homePath(); err == nil {
		out = append(out, home)
	}
	if len(c.appPaths) > 0 {
		project := projectPath(c.appPaths[0])
		if len(out) == 0 || out[0] != project {
			out = append(out, project)
		}
	}
	return out
}

// distrustedSignersPaths lists the on-disk suppression files in union order —
// the exact mirror of allowedSignersPaths.
func (c *signerFiles) distrustedSignersPaths() []string {
	return c.signerStorePaths(paths.HomeDistrustedSignersPath, paths.DistrustedSignersPath)
}

// readPrincipalLines parses one distrusted_signers file: one principal per
// non-empty, non-`#`-comment line. An ABSENT file contributes nothing, which
// is the overwhelmingly common shape (nobody has suppressed anything).
//
// A file that EXISTS but cannot be read is the mirror of
// parseAllowedSigners' case with the DIRECTION REVERSED: fewer suppressions
// means MORE embedded keys trusted, so a partially-read file re-trusts a key
// the operator explicitly removed — a human's "no" reversed, the same shape as
// a silently-reversed rejection on the reject path. A revocation holds until a
// human withdraws it; an I/O error is not a human withdrawing it. So an
// unreadable file suppresses EVERY embedded principal, and readPrincipalLines
// reports that as its second return rather than as an empty set.
//
// The cost is real and is the intended one: while the file cannot be read, no
// first-party content is trusted. That withholds content over a permissions
// problem, which is recoverable and loud, rather than admitting content over
// one, which is neither.
//
// A file that opens and then stops PART WAY THROUGH is the same degradation
// wearing a success's clothes: a mid-read I/O error, or a line past
// bufio.Scanner's 64 KiB token limit, ends the scan with whatever was parsed
// so far and no error anywhere. Every principal below the truncation point
// would stop being suppressed, so a truncated read is treated exactly like an
// unreadable one.
func readPrincipalLines(rep report.Reporter, fs afero.Fs, path string) (map[string]bool, bool) {
	f, err := fs.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false // absent: nobody suppressed anything here
		}
		rep.Warnf("distrusted_signers %s exists but cannot be read; no first-party signer is trusted this session, so that a revocation recorded there cannot be reversed by an I/O error: %v", path, err)
		return nil, true
	}
	defer func() { _ = f.Close() }()

	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if err := sc.Err(); err != nil {
		rep.Warnf("distrusted_signers %s could only be read as far as %d entr(ies); no first-party signer is trusted this session, so that a revocation below that point cannot be reversed by a truncated read: %v", path, len(out), err)
		return out, true
	}
	return out, false
}

// allowedSignersPaths lists the on-disk trust-root files in union order.
func (c *signerFiles) allowedSignersPaths() []string {
	return c.signerStorePaths(paths.HomeAllowedSignersPath, paths.AllowedSignersPath)
}

// parseAllowedSigners reads and parses one allowed_signers file. An absent file
// contributes nothing and is not an error — the overwhelmingly common case is
// that neither store exists. A malformed LINE is skipped with a warning while
// the file's valid entries still load, matching ssh-keygen's own behavior: one
// bad line must not silently disarm every other key in the file.
//
// A file that EXISTS but cannot be read is neither of those, and it used to
// be erased: this returned nil, Union skipped nil, and the resulting trust
// root was byte-identical to one where the file did not exist. Every key that
// file listed silently stopped counting, with no warning on the way out and
// nothing on the Store to ask afterwards. It now warns and returns a
// FailedSource, so the failure rides on the union as provenance for any
// caller that needs to refuse rather than guess.
func (c *signerFiles) parseAllowedSigners(fs afero.Fs, path string) *allowedsigners.Store {
	f, err := fs.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // absent: no keys from here, and that is a real answer
		}
		// A real error here (EACCES, a directory in its place) is NOT the
		// same fact as "absent" — it silently disarmed the on-disk trust
		// root with no finding beyond the stderr line. Escalate it, matching
		// EffectiveTrust's fail-closed posture for a corrupt trust store, in
		// addition to the warning.
		c.rep.Warnf("allowed_signers %s exists but cannot be read, its keys are NOT trusted this session: %v", path, err)
		c.rep.Failf(report.KindTrust, "make the allowed_signers file readable, or remove it",
			"allowed_signers %s exists but cannot be read: %v", path, err)
		return allowedsigners.FailedSource(path, err)
	}
	defer func() { _ = f.Close() }()

	store, parseErrs, err := allowedsigners.Parse(f)
	if err != nil {
		c.rep.Warnf("allowed_signers %s unreadable, ignoring it: %v", path, err)
		c.rep.Failf(report.KindTrust, "fix the allowed_signers file, or remove it",
			"allowed_signers %s unreadable: %v", path, err)
		return allowedsigners.FailedSource(path, err)
	}
	for _, pe := range parseErrs {
		// Once per file and line: the root is rebuilt by every reader of it, and
		// a stray line is one fact however many times the file is parsed.
		c.rep.WarnOncef("allowed_signers %s:%d ignored: %v", path, pe.Line, pe.Err)
	}
	return store.WithSource(path)
}
