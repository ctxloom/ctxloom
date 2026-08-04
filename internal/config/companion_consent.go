package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
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
// THE SHAPE. The first time a given companion would be exec'd, ctxloom asks and
// RECORDS the answer — the ssh known_hosts pattern. A non-interactive session
// (an agent, CI) never prompts: it SKIPS the unconfirmed companion with a
// warning, fail-closed, matching how the probe already degrades on failure.
//
// THE KEY. A record is keyed on the RESOLVED ABSOLUTE PATH **and the binary's
// SHA-256**. Path alone would let a replace-in-place swap inherit an existing
// approval; name alone would let a binary earlier in $PATH inherit an approval
// granted to a completely different file (the motivating case: node_modules/.bin
// usually sits EARLY in PATH, and companionsOnPathByConvention mirrors shell
// order, first directory wins).
//
// THE ASYMMETRY. An APPROVAL requires an exact (path, sha256) match, so any
// byte change at an approved path re-prompts. A DENIAL matches on PATH ALONE,
// so "I never want this run" survives the attacker rebuilding — rejection is
// supreme here for the same reason it is step 1 of EffectiveTrust.

// CompanionConsentRecord is one recorded human decision about executing a
// companion binary. It is data, not a signature: its authority is the
// filesystem permissions on the user's home directory, exactly like the
// unsigned approval markers of signature-envelope spec §9.5. That is why the
// record is PERSONAL-only and has no committable project twin (see
// paths.CompanionConsentFileName): a repo you cloned must never be able to
// arrive carrying pre-approved binaries.
type CompanionConsentRecord struct {
	// Bin is the companion name as discovered (filepath.Base of Path). Display
	// and grouping only — never the key, because a name is exactly what an
	// attacker gets to choose.
	Bin string `yaml:"bin"`
	// Path is the resolved, symlink-followed absolute path of the approved
	// binary. Half of the key.
	Path string `yaml:"path"`
	// SHA256 is the lowercase hex SHA-256 of the binary's bytes at the moment
	// the decision was recorded. The other half of the key, and the half that
	// makes a replace-in-place swap re-prompt.
	SHA256 string `yaml:"sha256"`
	// Approved is the decision. A false record is a DENIAL and is deliberately
	// honored path-wide (see companionConsent.decision).
	Approved bool `yaml:"approved"`
	// RecordedAt is untrusted display metadata for humans — never an input to
	// any decision, the same standing every timestamp has in this codebase.
	RecordedAt time.Time `yaml:"recorded_at"`
}

// companionConsentDoc is the on-disk shape of the consent record.
type companionConsentDoc struct {
	// Version exists so a future format change fails loud instead of being
	// silently misread as an empty (= "nothing consented") store.
	Version    int                      `yaml:"version"`
	Companions []CompanionConsentRecord `yaml:"companions"`
}

// companionConsentVersion is the only on-disk version this build understands.
const companionConsentVersion = 1

// companionConsentPath is the seam for the record's location; production
// resolves ~/.ctxloom/companion_consent.yaml.
var companionConsentPath = paths.HomeCompanionConsentPath

// CompanionConsentPath returns the file the companion exec-consent record
// lives in (~/.ctxloom/companion_consent.yaml).
func CompanionConsentPath() (string, error) { return companionConsentPath() }

// companionConsent is the parsed record plus the fault flag. A store that
// EXISTS but cannot be read or parsed is NOT "nothing consented": it may hold a
// DENIAL, and reading that silence as permission would re-open a door a human
// closed. Such a store denies every companion, first-party included — the same
// fail-closed shape EffectiveTrust's unreadable-approvals-store gate has, for
// the same reason.
type companionConsent struct {
	records []CompanionConsentRecord
	fault   error
}

// loadCompanionConsent reads the record. An ABSENT file is the ordinary
// "nobody has decided anything yet" state and is not a fault.
func loadCompanionConsent() companionConsent {
	path, err := companionConsentPath()
	if err != nil {
		return companionConsent{fault: err}
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is ctxloom's own home record
	if errors.Is(err, os.ErrNotExist) {
		return companionConsent{}
	}
	if err != nil {
		return companionConsent{fault: fmt.Errorf("read %s: %w", path, err)}
	}
	var doc companionConsentDoc
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return companionConsent{fault: fmt.Errorf("parse %s: %w", path, uerr)}
	}
	if doc.Version != companionConsentVersion {
		return companionConsent{fault: fmt.Errorf(
			"%s declares version %d, this build understands %d", path, doc.Version, companionConsentVersion)}
	}
	return companionConsent{records: doc.Companions}
}

// deniedPath reports whether a recorded DENIAL covers this path. Hash-blind on
// purpose: a decline must survive the binary being rebuilt, or "no" would only
// ever have meant "no, until you change one byte". It is also the arm that
// needs no hash, which is what lets the admission cascade check a refusal
// before it pays to hash anything.
func (c companionConsent) deniedPath(path string) bool {
	for _, r := range c.records {
		if r.Path == path && !r.Approved {
			return true
		}
	}
	return false
}

// approved reports whether a recorded APPROVAL covers this exact path AND these
// exact bytes. Both halves are required: that is the whole content of the
// path+hash key.
func (c companionConsent) approved(path, sha string) bool {
	for _, r := range c.records {
		if r.Approved && r.Path == path && r.SHA256 == sha {
			return true
		}
	}
	return false
}

// saveCompanionConsent writes rec into the record, replacing any prior entry
// for the same (path, sha256) pair and dropping every other entry for the same
// PATH — a path holds exactly one live decision, so re-deciding never leaves a
// stale approval behind for an older build of the same binary.
func saveCompanionConsent(rec CompanionConsentRecord) error {
	path, err := companionConsentPath()
	if err != nil {
		return err
	}
	existing := loadCompanionConsent()
	if existing.fault != nil {
		// Refuse to overwrite a record we could not read: the file may hold
		// decisions this write would silently erase.
		return fmt.Errorf("companion consent record is unreadable, refusing to overwrite it: %w", existing.fault)
	}
	kept := make([]CompanionConsentRecord, 0, len(existing.records)+1)
	for _, r := range existing.records {
		if r.Path != rec.Path {
			kept = append(kept, r)
		}
	}
	kept = append(kept, rec)
	return writeCompanionConsent(path, kept)
}

// writeCompanionConsent serializes recs to path, 0600 (it records decisions
// about executing code — world-readable would leak the machine's companion
// layout, and world-WRITABLE would hand the decision away).
func writeCompanionConsent(path string, recs []CompanionConsentRecord) error {
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Path != recs[j].Path {
			return recs[i].Path < recs[j].Path
		}
		return recs[i].SHA256 < recs[j].SHA256
	})
	data, err := yaml.Marshal(companionConsentDoc{Version: companionConsentVersion, Companions: recs})
	if err != nil {
		return fmt.Errorf("marshal companion consent: %w", err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), mkErr)
	}
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		return fmt.Errorf("write %s: %w", path, werr)
	}
	return nil
}

// ListCompanionConsent returns every recorded decision, sorted by path. It is
// the read half of the user-facing `ctxloom trust companion list`.
func ListCompanionConsent() ([]CompanionConsentRecord, error) {
	c := loadCompanionConsent()
	if c.fault != nil {
		return nil, c.fault
	}
	out := append([]CompanionConsentRecord(nil), c.records...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// SetCompanionConsent records an explicit decision for the binary at target
// (an absolute path, or a bare name resolved through $PATH), hashing the file
// as it stands right now. It is the scriptable form of the interactive prompt —
// the escape hatch a non-interactive CI run needs, deliberately requiring a
// human to type it rather than inferring consent from the environment.
func SetCompanionConsent(target string, approved bool) (CompanionConsentRecord, error) {
	resolved, err := resolveCompanionTarget(target)
	if err != nil {
		return CompanionConsentRecord{}, err
	}
	sum, err := companionBinarySHA256(resolved)
	if err != nil {
		return CompanionConsentRecord{}, err
	}
	rec := CompanionConsentRecord{
		Bin:        filepath.Base(resolved),
		Path:       resolved,
		SHA256:     sum,
		Approved:   approved,
		RecordedAt: time.Now().UTC(),
	}
	if serr := saveCompanionConsent(rec); serr != nil {
		return CompanionConsentRecord{}, serr
	}
	return rec, nil
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
	path, perr := companionConsentPath()
	if perr != nil {
		return 0, perr
	}
	c := loadCompanionConsent()
	if c.fault != nil {
		return 0, c.fault
	}
	kept := make([]CompanionConsentRecord, 0, len(c.records))
	for _, r := range c.records {
		if r.Path != resolved {
			kept = append(kept, r)
		}
	}
	removed := len(c.records) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	if werr := writeCompanionConsent(path, kept); werr != nil {
		return 0, werr
	}
	return removed, nil
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
