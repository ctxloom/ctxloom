package companions

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

// ===== Companion identity ====================================================
//
// What a discovered companion IS: the name it was found under and the file that
// name resolves to. Admission — whether ctxloom may execute it — is decided
// entirely from a signature over that file (see companion_admission.go), so
// nothing here records or consults a decision.
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
// THE HOLE IT CLOSES is still the one above, answered differently: a
// name-squatted binary is refused because nobody you trust signed it, rather
// than because you were asked about it once and said no.

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

// resolveCompanionPath canonicalizes a companion binary's location: absolute,
// with every symlink followed. Both halves matter. A relative $PATH entry (the
// scan deliberately does not filter them) would otherwise record a key that
// means a different file from a different working directory, and an unresolved
// symlink would let the file the record vouches for be swapped without the
// recorded path changing.
// The 84% match against paths.HomePathFor is a template match
// on Go's abs-then-wrap idiom, not shared logic, and extraction was tried
// before this waiver was written. The two disagree on the thing that matters:
// this resolves a companion binary's REAL path and MUST fail loudly when
// symlinks cannot be resolved, because the resolved path is the identity a
// trust decision is made against; HomePathFor builds a lock FILENAME and
// resolves no symlinks at all. The nearest real helper, realpath.Resolve,
// deliberately never errors — the opposite contract — so routing this through
// it would silently downgrade an identity failure to a best-effort guess.
// reprise:ignore
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

// ===== First-party pinning ===================================================

// lookPath is the PATH-resolution seam: the one place this package asks the
// host which binaries exist. Tests fake it so companion discovery is a
// property of the test, not of the developer's machine.
var lookPath = exec.LookPath

// SetLookPathForTesting overrides the PATH-resolution seam and returns a
// restore function.
func SetLookPathForTesting(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}
