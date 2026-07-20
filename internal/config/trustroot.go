package config

import (
	"bufio"
	_ "embed"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// embeddedAllowedSigners is ctxloom's compiled-in trust root in the real
// allowed_signers format, parsed by the same parser every other location uses
// so the format has a single source of truth. See the file for what it grants.
//
//go:embed embedded_signers.allowed_signers
var embeddedAllowedSigners string

// embeddedSigners returns ctxloom's own compiled-in trust root: the public
// key(s) of the ctxloom release / bundle-publishing pipeline (spec §7, location
// 1). Trusting the ctxloom binary trusts what it ships.
//
// The embedded content is a fixed constant, so a parse failure here is a build
// bug caught by TestEmbeddedSigners, not a runtime condition; any unparsable
// line is dropped (toward LESS trust — an unrecognized key trusts nothing).
func embeddedSigners() *allowedsigners.Store {
	store, _, err := allowedsigners.Parse(strings.NewReader(embeddedAllowedSigners))
	if err != nil || store == nil {
		return allowedsigners.NewStore()
	}
	return store
}

// EmbeddedSigners returns ctxloom's own compiled-in trust root as a READ view
// — every entry, unfiltered, including any this machine has locally
// suppressed (see SuppressedEmbeddedPrincipals). It exists so callers outside
// this package (operations.ListSigners/ShowSigner — the oozy-plod (a)
// visibility fix) can enumerate the compiled-in entries without duplicating
// the //go:embed. embeddedSigners() itself stays unexported by design (a
// trust root nobody controls would be worse than none): this returns a
// Store VALUE, never a handle anything could write through, so the embedded
// root is now VISIBLE without ever becoming editable via this accessor.
func EmbeddedSigners() *allowedsigners.Store {
	return embeddedSigners()
}

// TrustRoot returns the union of every allowed_signers location: ctxloom's
// embedded defaults (MINUS any locally suppressed entry — oozy-plod (b), see
// SuppressedEmbeddedPrincipals/filterSuppressedPrincipals below), the user
// store (~/.ctxloom/allowed_signers), and the project store
// (.ctxloom/allowed_signers). All locations are unioned — a key counts for
// the namespaces it lists wherever it is listed — because precedence lives in
// the DECISION FUNCTION, never in the filesystem (spec §7, §9.2).
//
// It never fails. A missing store is simply no keys; an unreadable or malformed
// one warns and contributes whatever lines did parse. Every one of those
// degradations moves toward LESS exposure, never more: fewer trusted keys means
// more content is unsigned, and unsigned content is withheld until a human
// reviews it (spec §10.5).
func (c *Config) TrustRoot() *allowedsigners.Store {
	fs := c.fs
	if fs == nil {
		fs = afero.NewOsFs()
	}
	stores := []*allowedsigners.Store{c.embeddedSignersTrusted()}
	for _, path := range c.allowedSignersPaths() {
		stores = append(stores, c.parseAllowedSigners(fs, path))
	}
	return allowedsigners.Union(stores...)
}

// embeddedSignersTrusted returns the embedded trust root minus any entry
// whose principal this machine has locally distrusted (oozy-plod (b)). The
// compiled-in bytes never change — nothing here edits
// embedded_signers.allowed_signers or the binary — this filters the STORE
// value fresh on every call, so a suppression written mid-session (`signer
// remove <embedded-principal>`) takes effect on the very next trust decision
// with nothing to invalidate.
func (c *Config) embeddedSignersTrusted() *allowedsigners.Store {
	store := embeddedSigners()
	suppressed := c.SuppressedEmbeddedPrincipals()
	if len(suppressed) == 0 {
		return store
	}
	return filterSuppressedPrincipals(store, suppressed)
}

// filterSuppressedPrincipals returns a copy of store with every entry whose
// Principals list contains a suppressed principal removed. This is the actual
// SUBTRACTION primitive oozy-plod (b) needed: allowedsigners.Store.decide()
// (store.go:100) is purely additive with no negative-entry concept, and
// Union (store.go:35) only ever concatenates — so this is new machinery, not
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
// Never fails: a missing or unreadable file simply contributes nothing.
// Read/write of this store is written by operations.RemoveSigner (the only
// production mutator — see docs/trust-model.md's CLI-only signer-management
// boundary, ADR 0024); this method is the READ side TrustRoot() and
// ListSigners/ShowSigner both consult.
func (c *Config) SuppressedEmbeddedPrincipals() map[string]bool {
	fs := c.fs
	if fs == nil {
		fs = afero.NewOsFs()
	}
	out := map[string]bool{}
	for _, path := range c.distrustedSignersPaths() {
		for principal := range readPrincipalLines(fs, path) {
			out[principal] = true
		}
	}
	return out
}

// distrustedSignersPaths lists the on-disk suppression files in union order
// (user, then project), skipping the project path when it resolves to the
// same file as the user one — the exact mirror of allowedSignersPaths.
func (c *Config) distrustedSignersPaths() []string {
	var out []string
	if home, err := paths.HomeDistrustedSignersPath(); err == nil {
		out = append(out, home)
	}
	if len(c.appPaths) > 0 {
		project := paths.DistrustedSignersPath(c.appPaths[0])
		if len(out) == 0 || out[0] != project {
			out = append(out, project)
		}
	}
	return out
}

// readPrincipalLines parses one distrusted_signers file: one principal per
// non-empty, non-`#`-comment line. An absent or unreadable file simply
// contributes nothing (mirrors parseAllowedSigners' degrade-toward-fewer-keys
// default — here, degrading toward FEWER suppressions, i.e. MORE embedded
// keys trusted, which is the pre-A2 status quo, never a new exposure this
// mechanism itself introduces).
func readPrincipalLines(fs afero.Fs, path string) map[string]bool {
	f, err := fs.Open(path)
	if err != nil {
		return nil
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
	return out
}

// allowedSignersPaths lists the on-disk trust-root files in union order (user,
// then project), skipping the project path when it resolves to the same file as
// the user one (a home-rooted .ctxloom, where both names denote one file).
func (c *Config) allowedSignersPaths() []string {
	var out []string
	if home, err := paths.HomeAllowedSignersPath(); err == nil {
		out = append(out, home)
	}
	if len(c.appPaths) > 0 {
		project := paths.AllowedSignersPath(c.appPaths[0])
		if len(out) == 0 || out[0] != project {
			out = append(out, project)
		}
	}
	return out
}

// parseAllowedSigners reads and parses one allowed_signers file. An absent file
// contributes nothing and is not an error — the overwhelmingly common case is
// that neither store exists. A malformed LINE is skipped with a warning while
// the file's valid entries still load, matching ssh-keygen's own behavior: one
// bad line must not silently disarm every other key in the file.
func (c *Config) parseAllowedSigners(fs afero.Fs, path string) *allowedsigners.Store {
	f, err := fs.Open(path)
	if err != nil {
		return nil // absent (or unreadable): no keys from here
	}
	defer func() { _ = f.Close() }()

	store, parseErrs, err := allowedsigners.Parse(f)
	if err != nil {
		clidiag.Warn("ctxloom", "allowed_signers %s unreadable, ignoring it: %v", path, err)
		return nil
	}
	for _, pe := range parseErrs {
		clidiag.Warn("ctxloom", "allowed_signers %s:%d ignored: %v", path, pe.Line, pe.Err)
	}
	return store
}
