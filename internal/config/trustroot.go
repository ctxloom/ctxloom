package config

import (
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

// TrustRoot returns the union of every allowed_signers location: ctxloom's
// embedded defaults, the user store (~/.ctxloom/allowed_signers), and the
// project store (.ctxloom/allowed_signers). All locations are unioned — a key
// counts for the namespaces it lists wherever it is listed — because precedence
// lives in the DECISION FUNCTION, never in the filesystem (spec §7, §9.2).
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
	stores := []*allowedsigners.Store{embeddedSigners()}
	for _, path := range c.allowedSignersPaths() {
		stores = append(stores, c.parseAllowedSigners(fs, path))
	}
	return allowedsigners.Union(stores...)
}

// allowedSignersPaths lists the on-disk trust-root files in union order (user,
// then project), skipping the project path when it resolves to the same file as
// the user one (a home-rooted .ctxloom, where both names denote one file).
func (c *Config) allowedSignersPaths() []string {
	var out []string
	if home, err := paths.HomeAllowedSignersPath(); err == nil {
		out = append(out, home)
	}
	if len(c.AppPaths) > 0 {
		project := paths.AllowedSignersPath(c.AppPaths[0])
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
