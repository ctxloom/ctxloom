package companions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

// CompanionAdmissionReason names WHY a companion was or was not admitted to
// execution. It exists so no caller has to report a refusal as an absence:
// "found on PATH but never allowed" and "not installed" are different facts
// about the user's machine, and collapsing them is the silent no-op this
// codebase's characteristic bug is made of.
type CompanionAdmissionReason string

const (
	// CompanionNotInstalled: the name resolves to nothing on $PATH. Ordinary
	// and silent — most machines have no reprise.
	CompanionNotInstalled CompanionAdmissionReason = "not-installed"

	// CompanionAllowed: the allow store holds a record for exactly this path
	// and these bytes (or, for a pinned copy, these bytes under this name).
	CompanionAllowed CompanionAdmissionReason = "allowed"

	// CompanionNotAllowed: no record covers this path.
	CompanionNotAllowed CompanionAdmissionReason = "not-allowed"

	// CompanionHashChanged: the path is allowed, but for bytes other than the
	// ones there now — a rebuild, an upgrade, or a swap. The human decides
	// which; Detail carries the recorded and the present hash.
	CompanionHashChanged CompanionAdmissionReason = "hash-changed"

	// CompanionUnreadable: the binary is present but could not be resolved or
	// hashed, so it cannot be identified. Fail-closed.
	CompanionUnreadable CompanionAdmissionReason = "unreadable"

	// CompanionSelf: the running ctxloom binary, probed as its own companion.
	// No record is consulted — the process is already executing, so exec
	// consent is not a question it can be asked.
	CompanionSelf CompanionAdmissionReason = "self"
)

// AllowStore is the per-user record of companion binaries ctxloom may execute:
// one record per resolved path, holding the SHA-256 of the bytes a human
// allowed there.
type AllowStore = admission.Store[CompanionKey, CompanionAdmissionReason]

// allowReasons maps the store's own outcomes onto this domain's vocabulary.
// Admission decides from a Snapshot (admitCompanionAllowed) and nothing ever
// records a denial or calls the store's Decide, so Declined and Unasked are
// never produced; they are assigned only because the store requires four
// distinct reasons.
var allowReasons = admission.Reasons[CompanionAdmissionReason]{
	Approved: CompanionAllowed,
	Declined: CompanionHashChanged,
	Unasked:  CompanionNotAllowed,
	Fault:    CompanionUnreadable,
}

// allowKey is the exact identity a record approves: path AND bytes.
func allowKey(k CompanionKey) string { return k.Path + "\x00" + k.SHA256 }

// allowScope is what a record is ABOUT: the path. A scope holds one record, so
// re-allowing a rebuilt binary replaces the old hash rather than adding one.
func allowScope(k CompanionKey) string { return k.Path }

// NewAllowStore opens the per-user allow store (paths.HomeCompanionAllowPath).
func NewAllowStore(fs afero.Fs) (*AllowStore, error) {
	p, err := paths.HomeCompanionAllowPath()
	if err != nil {
		return nil, err
	}
	return NewAllowStoreAt(fs, p), nil
}

// NewAllowStoreAt opens an allow store at an explicit path — for a store that
// is not this user's, such as the one an agent image is built with.
func NewAllowStoreAt(fs afero.Fs, path string) *AllowStore {
	return admission.NewStore(fs, path, allowKey, allowReasons, admission.WithScope(allowScope))
}

// LoadAllowed reads the per-user allow store for a batch of admissions. A
// store that cannot be read admits nothing — it may be the only record of
// what was allowed, and guessing is how the wrong binary runs — and says so.
func LoadAllowed() *admission.Snapshot[CompanionKey] {
	store, err := NewAllowStore(afero.NewOsFs())
	if err != nil {
		clidiag.WarnOnce("ctxloom", "companion allow store unavailable, no companion will run: %v", err)
		return nil
	}
	snap, err := store.Load()
	if err != nil {
		clidiag.WarnOnce("ctxloom", "companion allow store unreadable, no companion will run: %v", err)
		return nil
	}
	return snap
}

// ResolveCompanion identifies the binary a companion name or path refers to,
// as admission would see it: resolved through PATH when it is a bare name,
// made absolute with every symlink followed, and hashed.
func ResolveCompanion(pathOrName string) (CompanionKey, error) {
	raw := pathOrName
	if !strings.ContainsRune(pathOrName, filepath.Separator) && !strings.ContainsRune(pathOrName, '/') {
		found, err := lookPath(pathOrName)
		if err != nil {
			return CompanionKey{}, fmt.Errorf("companion %q: not found on PATH: %w", pathOrName, err)
		}
		raw = found
	}
	resolved, err := resolveCompanionPath(raw)
	if err != nil {
		return CompanionKey{}, err
	}
	payload, err := os.ReadFile(resolved) //nolint:gosec // the binary being identified
	if err != nil {
		return CompanionKey{}, fmt.Errorf("read companion %s: %w", resolved, err)
	}
	return CompanionKey{Bin: filepath.Base(raw), Path: resolved, SHA256: sha256Hex(payload)}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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
// Path is empty when the name is not installed. SHA256 is the hash of the
// bytes admission read — set on an admission and on a refusal for bytes the
// release statement does not hash, and empty for every refusal decided before
// the bytes were read. It costs nothing extra: verification reads and hashes
// the whole binary either way.
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
// may execute it: a record in allowed for this exact path and these bytes, and
// nothing else. A nil snapshot (a store that could not be read) admits nothing.
//
// Decisions are made BEFORE any exec, which is what keeps a refused companion
// from running: the probes' concurrency starts after admission, over the
// admitted set only.
func AdmitCompanions(bins []string, allowed *admission.Snapshot[CompanionKey]) []CompanionAdmission {
	pinRoot, _ := paths.HomeCompanionPinDir() // "" disables only the pin rule
	out := make([]CompanionAdmission, 0, len(bins))
	for _, bin := range bins {
		a, _ := admitCompanionAllowed(bin, allowed, pinRoot)
		out = append(out, a)
	}
	return out
}

// companionAdmission is the seam the two probes consult, so a test can pin the
// admission answer without building and allowing a real binary for every
// loadout-parsing case. Production is AdmitCompanions itself.
var companionAdmission = AdmitCompanions

// SetCompanionAdmissionForTesting overrides the admission gate the probes
// consult and returns a restore function. Companion of
// SetCompanionLoadoutOutputForTesting: those seams fake the probe's OUTPUT,
// this one fakes the decision to run it at all.
func SetCompanionAdmissionForTesting(fn func(bins []string, allowed *admission.Snapshot[CompanionKey]) []CompanionAdmission) func() {
	prev := companionAdmission
	companionAdmission = fn
	return func() { companionAdmission = prev }
}

// AdmitEveryDiscoveredCompanionForTesting pins the admission gate OPEN for
// tests whose subject is what a companion contributes once it runs, and returns
// a restore function. It admits whatever the (usually faked) lookPath resolves
// and reports everything else as not-installed.
//
// It exists because the real gate hashes a real file against a recorded allow,
// which a test that faked PATH resolution to "/fake/ltk" cannot satisfy. Callers must
// ask for it EXPLICITLY — a SetLookPathForTesting that silently disabled the
// admission gate as a side effect would let a future regression in that gate go
// unnoticed by every test in the repo.
func AdmitEveryDiscoveredCompanionForTesting() func() {
	return SetCompanionAdmissionForTesting(func(bins []string, _ *admission.Snapshot[CompanionKey]) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			path, err := lookPath(bin)
			if err != nil {
				out = append(out, newCompanionAdmission(
					CompanionKey{Bin: bin}, false, CompanionNotInstalled))
				continue
			}
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin, Path: path}, true, CompanionAllowed))
		}
		return out
	})
}

// AdmitNoCompanionForTesting pins the admission gate SHUT and returns a
// restore function: every discovered name reports as not-installed, whatever
// is on the developer's PATH and whatever the allow store says about it.
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
	return SetCompanionAdmissionForTesting(func(bins []string, _ *admission.Snapshot[CompanionKey]) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin}, false, CompanionNotInstalled))
		}
		return out
	})
}

// verifiedCompanion is the bytes an admission decision was made OVER: the
// binary exactly as it was read and hashed, plus the file name it was admitted
// under. Only an admitted decision carries one. Anything that acts on an
// admitted companion after the decision acts on THESE bytes, never on a
// re-read of the path, which could have changed in between.
type verifiedCompanion struct {
	name    string
	payload []byte
}

// admitCompanionAllowed is the per-binary decision, also returning the bytes
// it was made over when it admits. pinRoot is paths.HomeCompanionPinDir ("" when
// unresolvable, which disables only the pin rule).
//
// AN ALLOW RECORD FOR THIS PATH AND THESE BYTES IS THE WHOLE GATE. The path is
// half of it because a human allowed a file where they put it; the hash is the
// other half because they allowed those bytes, not whatever later appears
// there. A rebuild therefore refuses as hash-changed until it is allowed again
// (`just install` re-allows what it installs).
//
// One exemption, for the pin directory: PinAdmittedCompanions copies admitted
// binaries there and puts it first on the engine's PATH, so a ctxloom started
// from that PATH finds the copy at a path nobody allowed. A file under it is
// admitted when an allowed record holds the same bytes under the same name —
// the name, so one allowed companion's bytes cannot run as another's.
func admitCompanionAllowed(bin string, allowed *admission.Snapshot[CompanionKey], pinRoot string) (CompanionAdmission, verifiedCompanion) {
	raw, err := lookPath(bin)
	if err != nil {
		// not installed — ordinary, not a warning
		return newCompanionAdmission(CompanionKey{Bin: bin}, false, CompanionNotInstalled), verifiedCompanion{}
	}
	resolved, rerr := resolveCompanionPath(raw)
	if rerr != nil {
		clidiag.Warn("ctxloom", "companion %q: cannot resolve %s, withholding: %v", bin, raw, rerr)
		return newCompanionAdmission(CompanionKey{Bin: bin, Path: raw}, false, CompanionUnreadable), verifiedCompanion{}
	}
	payload, readErr := os.ReadFile(resolved) //nolint:gosec // the companion being admitted
	if readErr != nil {
		clidiag.Warn("ctxloom", "companion %q: cannot read %s to identify it, withholding: %v", bin, resolved, readErr)
		return newCompanionAdmission(CompanionKey{Bin: bin, Path: resolved}, false, CompanionUnreadable), verifiedCompanion{}
	}
	key := CompanionKey{Bin: bin, Path: resolved, SHA256: sha256Hex(payload)}
	verified := verifiedCompanion{name: filepath.Base(resolved), payload: payload}
	if allowed.Approved(key) || pinnedAllowed(key, allowed, pinRoot) {
		return newCompanionAdmission(key, true, CompanionAllowed), verified
	}
	if prev, ok := allowedAtPath(allowed, resolved); ok {
		a := newCompanionAdmission(key, false, CompanionHashChanged)
		a.Detail = fmt.Sprintf("hash changed: %s -> %s", prev.SHA256, key.SHA256)
		clidiag.WarnOnce("ctxloom", "companion %q at %s: %s since it was allowed, skipping — if this is the build you meant to run: ctxloom companion allow %s --yes",
			bin, resolved, a.Detail, resolved)
		return a, verifiedCompanion{}
	}
	clidiag.WarnOnce("ctxloom", "companion %q at %s is not allowed to run, skipping — to allow it: ctxloom companion allow %s",
		bin, resolved, resolved)
	return newCompanionAdmission(key, false, CompanionNotAllowed), verifiedCompanion{}
}

// allowedAtPath returns the approved record for path, if any — the record a
// hash-changed refusal discloses the old hash from.
func allowedAtPath(allowed *admission.Snapshot[CompanionKey], path string) (CompanionKey, bool) {
	for _, r := range allowed.Records() {
		if r.Approved && r.Key.Path == path {
			return r.Key, true
		}
	}
	return CompanionKey{}, false
}

// pinnedAllowed reports whether key is a pinned copy (under pinRoot) of bytes
// an approved record holds under the same file name.
func pinnedAllowed(key CompanionKey, allowed *admission.Snapshot[CompanionKey], pinRoot string) bool {
	if pinRoot == "" || !realpath.Under(key.Path, pinRoot) {
		return false
	}
	name := filepath.Base(key.Path)
	for _, r := range allowed.Records() {
		if r.Approved && r.Key.SHA256 == key.SHA256 && filepath.Base(r.Key.Path) == name {
			return true
		}
	}
	return false
}

// AllowFileFor renders an allow-store document admitting each binary in
// installed — a map from the path a binary will be executed at to the file
// holding its bytes now. It is how an agent image carries the allow for the
// companions baked into it: the in-image paths differ from the host's, so the
// host's records cannot simply be copied.
func AllowFileFor(installed map[string]string) ([]byte, error) {
	const at = "/" + paths.CompanionAllowFileName + ".yaml"
	mem := afero.NewMemMapFs()
	store := NewAllowStoreAt(mem, at)
	for target, src := range installed {
		payload, err := os.ReadFile(src) //nolint:gosec // a staged companion
		if err != nil {
			return nil, fmt.Errorf("allow %s: %w", target, err)
		}
		key := CompanionKey{Bin: filepath.Base(target), Path: target, SHA256: sha256Hex(payload)}
		if _, err := store.Set(key, true); err != nil {
			return nil, fmt.Errorf("allow %s: %w", target, err)
		}
	}
	return afero.ReadFile(mem, at)
}
