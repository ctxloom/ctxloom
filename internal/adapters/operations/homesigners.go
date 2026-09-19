package operations

import "github.com/ctxloom/ctxloom/internal/core/paths"

// homeAllowedSignersPath is the sibling chokepoint for the OTHER home-rooted
// store this package WRITES: ~/.ctxloom/allowed_signers, the personal trust
// root `ctxloom signer trust` adds to. It is the same hazard one notch worse —
// a stray write there does not withhold content, it TRUSTS a signing key on
// the developer's machine, permanently — so it gets the same refusal.
//
// No override seam, unlike the approvals store: nothing needs one yet, and an
// injection point no caller uses is a branch no test can hold honest. The
// guard is the half that has to exist, because it protects callers that have
// not been written.
//
// The READ sites (ListSigners' user listing, config.Config.TrustRoot) are
// deliberately not routed through here. Their contract on an unresolvable home
// is to omit the user store silently, so a refusal would degrade rather than
// fail loud — the wrong shape for a guard. A read of the real trust root from
// a test is a lesser hazard than a write to it, and worth its own change.
func homeAllowedSignersPath() (string, error) {
	path, err := paths.HomeAllowedSignersPath()
	if err != nil {
		return "", err
	}
	if err := paths.UnsandboxedHomeError("user trust root", path,
		"testsupport.SandboxedMain / testsupport.Isolate, or --project against a temp checkout"); err != nil {
		return "", err
	}
	return path, nil
}

// homeDistrustedSignersPath is the sibling chokepoint for the OTHER
// home-rooted store this package WRITES: ~/.ctxloom/distrusted_signers, the
// local-suppression record `ctxloom signer untrust` writes when the named
// principal matches ctxloom's own embedded key. It is the exact file an
// unguarded test run once wrote a permanent, machine-wide distrust of
// ctxloom's own publishing principal into — withholding every remote bundle
// from a freshly-initialised project with nothing telling the user why — so
// it gets the same refusal homeAllowedSignersPath gives its sibling store.
func homeDistrustedSignersPath() (string, error) {
	path, err := paths.HomeDistrustedSignersPath()
	if err != nil {
		return "", err
	}
	if err := paths.UnsandboxedHomeError("user distrusted-signers store", path,
		"testsupport.SandboxedMain / testsupport.Isolate, or --project against a temp checkout"); err != nil {
		return "", err
	}
	return path, nil
}
