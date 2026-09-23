package companions

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// CompanionAdmissionReason names WHY a companion was or was not admitted to
// execution. It exists so no caller has to report a refusal as an absence:
// "found on PATH but never confirmed" and "not installed" are different facts
// about the user's machine, and collapsing them is the silent no-op this
// codebase's characteristic bug is made of.
type CompanionAdmissionReason string

const (
	// CompanionAdmissionNotInstalled: the name resolves to nothing on $PATH.
	// Ordinary and silent — most machines have no reprise.
	CompanionAdmissionNotInstalled CompanionAdmissionReason = "not-installed"

	// CompanionAdmissionSigned: the bytes carry a signature from a key the
	// trust root authorizes for the companion namespace.
	CompanionAdmissionSigned CompanionAdmissionReason = "signed"

	// CompanionAdmissionUnsigned: no detached signature beside the binary.
	CompanionAdmissionUnsigned CompanionAdmissionReason = "unsigned"

	// CompanionAdmissionUntrusted: signed, by a key not authorized to say
	// "these bytes may execute here".
	CompanionAdmissionUntrusted CompanionAdmissionReason = "untrusted-signer"

	// CompanionAdmissionTampered: a signature that does not cover these bytes,
	// or will not parse. Never degraded to "unsigned" — a broken signature is a
	// signal, not an absence.
	CompanionAdmissionTampered CompanionAdmissionReason = "signature-tampered"
	// CompanionAdmissionDeclined: a recorded DENIAL covers this path. Beats
	// the first-party exemption.
	CompanionAdmissionDeclined CompanionAdmissionReason = "declined"
	// CompanionAdmissionUnreadable: the binary is present but could not be
	// resolved or hashed, so it cannot be identified. Fail-closed.
	CompanionAdmissionUnreadable CompanionAdmissionReason = "unreadable"
	// CompanionAdmissionStoreFault: the consent record exists but cannot be
	// read. Denies EVERY companion, first-party included.
	CompanionAdmissionStoreFault CompanionAdmissionReason = "consent-store-fault"
	// CompanionAdmissionSelf: the running ctxloom binary, probed as its own
	// companion. No signature is consulted — the process is already
	// executing, so exec consent is not a question it can be asked.
	CompanionAdmissionSelf CompanionAdmissionReason = "self"
)

// CompanionAdmission is the decision about whether ctxloom may EXECUTE one
// discovered companion binary — the gate that sits between DiscoverCompanions
// (which only lists candidates) and the two probes that shell out to them.
//
// Its two halves are the shared admission shape's: WHAT was decided about
// (CompanionKey — the discovered name, the resolved absolute path, and the
// binary's hash when one was computed) and WHAT was decided
// (admission.Decision — Allow, Reason, Detail). Both are embedded, so a
// caller still reads a.Bin, a.Path, a.Allow and a.Reason directly.
//
// Path is empty when the name is not installed. SHA256 is empty for a
// first-party admission (decided by location, which never hashes — hashing
// three ~60MB binaries on every startup would be a cost bought for nothing)
// and for every pre-hash refusal.
type CompanionAdmission struct {
	CompanionKey
	admission.Decision[CompanionAdmissionReason]
}

// newCompanionAdmission is the constructor the cascade's arms return through.
// Embedded fields cannot be named in a composite literal by their promoted
// spelling, and spelling both wrappers out at every arm would bury the one
// thing each arm is actually saying.
func newCompanionAdmission(k CompanionKey, allow bool, reason CompanionAdmissionReason) CompanionAdmission {
	return CompanionAdmission{
		CompanionKey: k,
		Decision:     admission.Decision[CompanionAdmissionReason]{Allow: allow, Reason: reason},
	}
}

// AdmitCompanions decides, for each discovered companion name, whether ctxloom
// may execute it: a signature over the binary's bytes, from a key root
// authorizes for the companion namespace, and nothing else.
//
// Decisions are made BEFORE any exec, which is what keeps a refused companion
// from running: the probes' concurrency starts after admission, over the
// admitted set only.
func AdmitCompanions(bins []string, root trust.TrustRoot) []CompanionAdmission {
	out := make([]CompanionAdmission, 0, len(bins))
	for _, bin := range bins {
		out = append(out, admitCompanion(bin, root))
	}
	return out
}

// companionAdmission is the seam the two probes consult, so a test can pin the
// exec-consent answer without building a real binary and a real home record for
// every loadout-parsing case. Production is AdmitCompanions itself.
var companionAdmission = AdmitCompanions

// SetCompanionAdmissionForTesting overrides the exec-consent gate the probes
// consult and returns a restore function. Companion of
// SetCompanionLoadoutOutputForTesting: those seams fake the probe's OUTPUT,
// this one fakes the decision to run it at all.
func SetCompanionAdmissionForTesting(fn func(bins []string, root trust.TrustRoot) []CompanionAdmission) func() {
	prev := companionAdmission
	companionAdmission = fn
	return func() { companionAdmission = prev }
}

// AdmitEveryDiscoveredCompanionForTesting pins the exec-consent gate OPEN for
// tests whose subject is what a companion contributes once it runs, and returns
// a restore function. It admits whatever the (usually faked) lookPath resolves
// and reports everything else as not-installed.
//
// It exists because the real gate hashes a real file at a real path, which a
// test that faked PATH resolution to "/fake/ltk" cannot satisfy. Callers must
// ask for it EXPLICITLY — a SetLookPathForTesting that silently disabled the
// consent gate as a side effect would let a future regression in that gate go
// unnoticed by every test in the repo.
func AdmitEveryDiscoveredCompanionForTesting() func() {
	return SetCompanionAdmissionForTesting(func(bins []string, _ trust.TrustRoot) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			path, err := lookPath(bin)
			if err != nil {
				out = append(out, newCompanionAdmission(
					CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled))
				continue
			}
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin, Path: path}, true, CompanionAdmissionSigned))
		}
		return out
	})
}

// AdmitNoCompanionForTesting pins the exec-consent gate SHUT and returns a
// restore function: every discovered name reports as not-installed, whatever
// is on the developer's PATH and whatever the trust root says about it.
//
// It is the counterpart of AdmitEveryDiscoveredCompanionForTesting, for tests
// whose subject is NOT companions but whose assertions a companion perturbs —
// a listing that must come back empty, an install/uninstall pair that must
// round-trip exactly. Those assertions are about ctxloom's own contribution,
// and a companion silently adds fragments, MCP servers and hooks to it.
//
// Ask for it EXPLICITLY, exactly as with the admit-everything seam. A test
// that leaves the real gate in place is measuring the machine it runs on: it
// reports a different verdict for a developer with taskloom installed than for
// one without, and neither verdict is about the code under test.
func AdmitNoCompanionForTesting() func() {
	return SetCompanionAdmissionForTesting(func(bins []string, _ trust.TrustRoot) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled))
		}
		return out
	})
}

// admitCompanion is the per-binary decision cascade. The ORDER is the security
// content, and it mirrors EffectiveTrust's: the fail-closed store gate first,
// then the human's "no", then the exemptions, then the recorded "yes", then the
// question. Nothing short-circuits ahead of a refusal.
//
// Arms 0–2 are companion-specific and stay here: they are about how a name
// resolves to a file, and about an exemption that must be decided BEFORE
// anything pays to hash. Arms 3 and 4 are the shared trust-on-first-use flow
// and are delegated to the store's Decide, which is where "recorded yes",
// "recorded no", "nobody could be asked" and "the store is unreadable" are one
// implementation for every consumer.
func admitCompanion(bin string, root trust.TrustRoot) CompanionAdmission {
	raw, err := lookPath(bin)
	if err != nil {
		// not installed — ordinary, not a warning
		return newCompanionAdmission(CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled)
	}
	resolved, rerr := resolveCompanionPath(raw)
	if rerr != nil {
		clidiag.Warn("ctxloom", "companion %q: cannot resolve %s, withholding: %v", bin, raw, rerr)
		return newCompanionAdmission(CompanionKey{Bin: bin, Path: raw}, false, CompanionAdmissionUnreadable)
	}
	key := CompanionKey{Bin: bin, Path: resolved}

	// A SIGNATURE FROM A TRUSTED PUBLISHER IS THE WHOLE GATE. There is no
	// second input: no recorded consent, no location exemption, no prompt.
	//
	// Each of those was a proxy for the question a signature answers directly —
	// who vouches for these bytes. The location pin meant "it sits where our
	// installer puts things", which stops being true the moment ctxloom runs
	// from a working tree while its companions live in $GOBIN. Trust-on-first-
	// use meant "you said yes to this hash once", which every rebuild
	// invalidates, so it asked again on each `just install` and trained the
	// reflex approval it existed to prevent.
	//
	// There is deliberately no way to refuse a companion that IS validly
	// signed. Nothing needs one: a user who does not want ctxloom running a
	// binary renames it, and discovery stops finding it. A recorded veto would
	// be a second policy to keep in step with the first, for a case the
	// filesystem already settles.
	//
	// UNSIGNED IS REFUSED, and it is not degradable: executing code nothing can
	// attest to IS the harm.
	//
	// WHAT IS SIGNED is the binary's release statement (companion_release.go):
	// its name, version and hash. Signing the bytes alone vouched for "some
	// program by this publisher", so a trusted publisher's taskloom, installed
	// under ltk's name, was admitted and run as ltk. The statement's name is
	// checked against the file actually resolved, and its hash against the
	// bytes actually there.
	sig, sigErr := os.ReadFile(resolved + companionSigSuffix)
	statement, relErr := os.ReadFile(resolved + companionReleaseSuffix)
	if sigErr != nil || relErr != nil {
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: no signed release statement beside it (%s and %s), skipping — a companion must be signed by a publisher you "+
				"trust (sign it where it is built: `just sign-binary %s`)", bin, resolved, companionReleaseSuffix, companionSigSuffix, resolved)
		return newCompanionAdmission(key, false, CompanionAdmissionUnsigned)
	}
	principal, verifyErr := signing.VerifyInNamespace(statement, sig, root, signing.NamespaceCompanion, time.Now())
	switch {
	case verifyErr != nil:
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: its signature does not cover its release statement, refusing to execute it: %v",
			bin, resolved, verifyErr)
		return newCompanionAdmission(key, false, CompanionAdmissionTampered)
	case principal == "":
		// VerifyInNamespace's "unsigned to you": a well-formed signature by a
		// key the trust root does not authorize for THIS namespace. A bundle
		// treats that as reviewable; execution cannot.
		clidiag.WarnOnce("ctxloom",
			"companion %q at %s: signed by a key you do not trust to authorize execution, skipping "+
				"(add its publisher to allowed_signers with namespaces=%q)",
			bin, resolved, signing.NamespaceCompanion)
		return newCompanionAdmission(key, false, CompanionAdmissionUntrusted)
	}
	rel, perr := parseCompanionRelease(statement)
	if perr != nil {
		clidiag.WarnOnce("ctxloom", "companion %q at %s: %s's signed release statement cannot be read, refusing to execute it: %v",
			bin, resolved, principal, perr)
		return newCompanionAdmission(key, false, CompanionAdmissionTampered)
	}
	if installed := filepath.Base(resolved); rel.name != installed {
		clidiag.WarnOnce("ctxloom", "companion %q at %s: %s signed it as %q but it is installed as %q, refusing to execute it under a name its publisher did not give it",
			bin, resolved, principal, rel.name, installed)
		return newCompanionAdmission(key, false, CompanionAdmissionTampered)
	}
	payload, readErr := os.ReadFile(resolved)
	if readErr != nil {
		clidiag.Warn("ctxloom", "companion %q: cannot read %s to verify it, withholding: %v", bin, resolved, readErr)
		return newCompanionAdmission(key, false, CompanionAdmissionUnreadable)
	}
	if sum := sha256.Sum256(payload); hex.EncodeToString(sum[:]) != rel.sha256 {
		clidiag.WarnOnce("ctxloom", "companion %q at %s: its bytes are not the ones %s signed as %s %s, refusing to execute it",
			bin, resolved, principal, rel.name, rel.version)
		return newCompanionAdmission(key, false, CompanionAdmissionTampered)
	}
	return newCompanionAdmission(key, true, CompanionAdmissionSigned)
}

// companionSigSuffix is the detached signature's extension — the one
// `ssh-keygen -Y sign` writes and the justfiles produce. It signs the release
// statement (companionReleaseSuffix), not the binary.
const companionSigSuffix = ".sig"
