package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
)

// ===== Companion EXEC consent (trust-on-first-use) ===========================
//
// Discovery (DiscoverCompanions) answers "what binaries CLAIM to be
// companions". This file answers the separate, sharper question: "may ctxloom
// EXECUTE this one". The two are deliberately not the same decision.
//
// THE HOLE THIS CLOSES. companionsOnPathByConvention walks every $PATH entry
// and filters nothing — not relative entries, not the exec bit — because it is
// a candidate LIST. ProbeCompanions / ProbeCompanionLoadouts then exec each
// match at session start with no user action. `./node_modules/.bin` is on $PATH
// in a large share of JavaScript projects, and an npm package — including a
// transitive dependency nobody chose — can ship a binary under any name.
// Shipping `ctxloom-companion-anything` therefore earned an exec at the next
// session start. That attacker does not control PATH; they name-squatted an
// auto-exec convention in a directory that is already on it. Every OTHER
// consumer of node_modules/.bin requires a human to TYPE the command.
//
// THE SHAPE is internal/shared/admission's, which is where the whole
// trust-on-first-use flow, the personal-file store and its fail-closed
// properties now live. What stays here is what is genuinely about companions:
// how a name resolves to a file, what identifies that file, and the cascade's
// order.
//
// THE KEY. A record is keyed on the RESOLVED ABSOLUTE PATH **and the binary's
// SHA-256**. Path alone would let a replace-in-place swap inherit an existing
// approval; name alone would let a binary earlier in $PATH inherit an approval
// granted to a completely different file (the motivating case: node_modules/.bin
// usually sits EARLY in PATH, and companionsOnPathByConvention mirrors shell
// order, first directory wins).
//
// THE ASYMMETRY is the shared store's scope/key split, and this is the domain
// that needed it: an APPROVAL requires an exact (path, sha256) match, so any
// byte change at an approved path re-prompts. A DENIAL matches the SCOPE, which
// here is the PATH ALONE, so "I never want this run" survives the attacker
// rebuilding — rejection is supreme here for the same reason it is step 1 of
// EffectiveTrust.

// CompanionKey identifies one companion binary to the consent store.
//
// It is also the whole record body on disk, so the file can be read, audited
// and pruned with `cat`. Only Path and SHA256 are the KEY (see
// companionConsentKey); Bin rides along as display metadata and is never
// decided on, because a name is exactly what an attacker gets to choose.
type CompanionKey struct {
	// Bin is the companion name as discovered (filepath.Base of Path). Display
	// and grouping only.
	Bin string `yaml:"bin"`
	// Path is the resolved, symlink-followed absolute path of the binary. The
	// SCOPE: a denial recorded here covers this file whatever its bytes become.
	Path string `yaml:"path"`
	// SHA256 is the lowercase hex SHA-256 of the binary's bytes at the moment
	// the decision was recorded. The other half of the key, and the half that
	// makes a replace-in-place swap re-prompt.
	SHA256 string `yaml:"sha256"`
}

// CompanionConsentRecord is one recorded human decision about executing a
// companion binary. It is data, not a signature: its authority is the
// filesystem permissions on the user's home directory, exactly like the
// unsigned approval markers of signature-envelope spec §9.5. That is why the
// record is PERSONAL-only and has no committable project twin (see
// paths.CompanionConsentFileName): a repo you cloned must never be able to
// arrive carrying pre-approved binaries.
type CompanionConsentRecord = admission.Record[CompanionKey]

// companionConsentKey is the identity an APPROVAL binds to: the exact file at
// the exact path, with the exact bytes. Bin is deliberately absent.
func companionConsentKey(k CompanionKey) string { return k.Path + "\x00" + k.SHA256 }

// companionConsentScope is what a DENIAL covers and what Forget removes by:
// the path, hash-blind. A refusal that only held until one byte changed would
// never have been a refusal.
func companionConsentScope(k CompanionKey) string { return k.Path }

// companionConsentPath is the seam for the record's location; production
// resolves ~/.ctxloom/companion_consent.yaml.
var companionConsentPath = paths.HomeCompanionConsentPath

// companionConsentReasons is this domain's half of the shared flow's
// vocabulary: the four outcomes the STORE itself can produce, spelled in the
// enum every companion surface already reports in. The other three reasons
// (not-installed, first-party, unreadable) belong to cascade arms the store
// never sees.
func companionConsentReasons() admission.Reasons[CompanionAdmissionReason] {
	return admission.Reasons[CompanionAdmissionReason]{
		Approved: CompanionAdmissionConsented,
		Declined: CompanionAdmissionDeclined,
		Unasked:  CompanionAdmissionUnconfirmed,
		Fault:    CompanionAdmissionStoreFault,
	}
}

// companionConsentStore opens the personal consent record.
//
// No admission.WithLockPathFor override: this record lives at
// ~/.ctxloom/companion_consent.yaml — home-rooted, not inside a project
// .ctxloom tree — which is exactly the shape admission.Store's default write
// lock (filelock.PathFor, beside the file) is for. Contrast dirtyTreeAckStore,
// whose record lives inside a project tree and passes filelock.ProjectPathFor
// explicitly.
//
// An unresolvable home yields an UNCONFIGURED store rather than an error, so
// construction stays total; the fault surfaces at the gate, where it fails
// closed. That is not a convenience: filepath.Join("", x) == x, so a store
// built from an empty path would key off the process working directory, and a
// stray file at a repo root would authorise an exec. The shared store refuses
// every read and write in that state.
func companionConsentStore() *admission.Store[CompanionKey, CompanionAdmissionReason] {
	path, err := companionConsentPath()
	if err != nil {
		path = ""
	}
	return admission.NewStore(afero.NewOsFs(), path, companionConsentKey, companionConsentReasons(),
		admission.WithScope(companionConsentScope))
}

// ListCompanionConsent returns every recorded decision, sorted by path. It is
// the read half of the user-facing `ctxloom companion list`.
func ListCompanionConsent() ([]CompanionConsentRecord, error) {
	return companionConsentStore().List()
}

// SetCompanionConsent records an explicit decision for the binary at target
// (an absolute path, or a bare name resolved through $PATH), hashing the file
// as it stands right now. It is the scriptable form of the interactive prompt —
// the escape hatch a non-interactive CI run needs, deliberately requiring a
// human to type it rather than inferring consent from the environment.
func SetCompanionConsent(target string, approved bool) (CompanionConsentRecord, error) {
	var zero CompanionConsentRecord
	resolved, err := resolveCompanionTarget(target)
	if err != nil {
		return zero, err
	}
	sum, err := companionBinarySHA256(resolved)
	if err != nil {
		return zero, err
	}
	return companionConsentStore().Set(CompanionKey{
		Bin: filepath.Base(resolved), Path: resolved, SHA256: sum,
	}, approved)
}

// ForgetCompanionConsent drops every recorded decision for target's resolved
// path and reports how many entries were removed. Zero removed is reported as
// zero, never as success-with-no-effect: forgetting a path nobody recorded is
// the caller's mistake to see.
func ForgetCompanionConsent(target string) (int, error) {
	resolved, err := resolveCompanionTarget(target)
	if err != nil {
		// A path whose file is gone can still hold a record — forgetting must
		// work for a binary the user already deleted, so fall back to the
		// literal argument rather than refusing.
		resolved = target
	}
	return companionConsentStore().Forget(CompanionKey{Path: resolved})
}

// resolveCompanionTarget turns a user-supplied name or path into the resolved,
// symlink-followed absolute path a record keys on. Resolution is the same for
// the CLI and for the probe, so a path a user types and a path the probe
// discovers can never disagree about identity.
func resolveCompanionTarget(target string) (string, error) {
	if target == "" {
		return "", errors.New("companion consent: no binary named")
	}
	candidate := target
	if !strings.ContainsRune(target, filepath.Separator) {
		found, err := lookPath(target)
		if err != nil {
			return "", fmt.Errorf("companion consent: %q is not on PATH: %w", target, err)
		}
		candidate = found
	}
	return resolveCompanionPath(candidate)
}

// resolveCompanionPath canonicalizes a companion binary's location: absolute,
// with every symlink followed. Both halves matter. A relative $PATH entry (the
// scan deliberately does not filter them) would otherwise record a key that
// means a different file from a different working directory, and an unresolved
// symlink would let the file the record vouches for be swapped without the
// recorded path changing.
func resolveCompanionPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve companion path %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve companion path %q: %w", abs, err)
	}
	return resolved, nil
}

// companionBinarySHA256 hashes the binary's bytes. A hash failure is a HARD
// failure for the caller: a file ctxloom cannot read is a file it cannot
// identify, and it must not be exec'd on the strength of a path alone.
func companionBinarySHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a resolved companion binary about to be exec'd
	if err != nil {
		return "", fmt.Errorf("hash companion %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, cerr := io.Copy(h, f); cerr != nil {
		return "", fmt.Errorf("hash companion %q: %w", path, cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ===== First-party pinning ===================================================

// companionInstallDir reports the directory ctxloom itself was executed from,
// with symlinks followed — the "expected install location" a first-party
// companion must resolve from to keep its TOFU exemption.
//
// WHY THE RUNNING BINARY'S OWN DIRECTORY, and not a recorded install dir or a
// hardcoded path: it is the one answer that is TRUE BY CONSTRUCTION on every
// install shape ctxloom actually ships, with nothing to configure and nothing
// to fall out of date. `just install` copies ctxloom, ltk and taskloom into
// ~/go/bin together; Homebrew puts all three in the same prefix bin; the
// devcontainer image installs them side by side; a `go install` of the trio
// lands them in $GOBIN. In every one of those, "next to ctxloom" is exactly
// where the first-party companions are, so the exemption stays silent through
// routine rebuilds — which is its entire purpose, since a prompt that fires on
// every `just install` trains reflex approval.
//
// It is also the strongest of the candidates: an attacker who can write into
// the directory holding the running ctxloom binary can replace ctxloom itself,
// so the exemption grants nothing they did not already have. A recorded
// install dir, by contrast, is a value in a file that an attacker could point
// at their own directory, and a name list with no location pin at all is the
// hole this pinning exists to close — firstPartyCompanions is discovered
// unconditionally, so a binary named `ltk` earlier in $PATH would otherwise
// inherit automatic execution against one of only three guessable names.
//
// A seam, so tests can pin the answer without moving the test binary.
var companionInstallDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// A running binary whose path will not resolve is not a reason to
		// widen the exemption — fall back to the unresolved directory, which
		// simply fails to match a resolved companion path and sends the
		// first-party name through TOFU like anything else.
		resolved = exe
	}
	return filepath.Dir(resolved), nil
}

// firstPartyPinned reports whether bin is a first-party companion resolving
// from the expected install location. BOTH halves are required: the name
// exempts nothing on its own.
func firstPartyPinned(bin, resolvedPath string) bool {
	if !isFirstPartyCompanion(bin) {
		return false
	}
	dir, err := companionInstallDir()
	if err != nil || dir == "" {
		return false // cannot establish the location → no exemption
	}
	return filepath.Dir(resolvedPath) == dir
}

// isFirstPartyCompanion reports whether bin is one of the shipped companion
// NAMES. Never a trust answer on its own — see firstPartyPinned.
func isFirstPartyCompanion(bin string) bool {
	for _, name := range firstPartyCompanions {
		if name == bin {
			return true
		}
	}
	return false
}
