package companions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// ===== A failed loadout probe: an answer, or an unknown =====================
//
// A verified companion's loadout probe can end without a loadout in two ways
// that mean opposite things. The companion may ANSWER that it offers none —
// it ran and exited with a status, the shape of a binary with no `loadout`
// subcommand — and then it contributes nothing, as a fact. Or the probe may
// FAIL — a timeout, a signal, an exec error, bytes that are not an envelope —
// and then what it contributes is UNKNOWN. Writing surfaces from "nothing" in
// the second case strips that companion's hooks, MCP servers and context while
// reporting success, so its last-known loadout is carried forward instead.
//
// THE RECORD IS THE LOADOUT, NOT THE SURFACES. The project's surfaces cannot
// say which entry a companion contributed: the ownership record keys a
// writer's entries by file and top-level key, and one writer's `hooks` blob
// holds every companion's hooks. Re-rendering from the loadout the companion
// last gave is what keeps its entries exactly as they were.

// ErrLoadoutUnsupported is a companion's clean answer that it offers no loadout.
var ErrLoadoutUnsupported = errors.New("companion offers no loadout")

// ErrLoadoutProbeFailed is a loadout probe that never answered: what the
// companion contributes is unknown.
var ErrLoadoutProbeFailed = errors.New("companion loadout probe failed")

// classifyLoadoutProbe wraps a loadout exec's error in the kind it is. Only a
// process that EXITED with a status answered — an exit error carrying no
// process state cannot show that — and a timeout is checked first because the
// context's kill can surface as an exit error of its own.
func classifyLoadoutProbe(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrLoadoutProbeFailed, err)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil && exitErr.Exited() {
		return fmt.Errorf("%w: %w", ErrLoadoutUnsupported, err)
	}
	return fmt.Errorf("%w: %w", ErrLoadoutProbeFailed, err)
}

// The warnings a failed probe prints. Each names the companion and the
// command that must answer for the warning to stop.
const (
	warnLoadoutCarried = "companion %q: loadout probe failed (%v); carrying its last-known loadout forward unchanged, " +
		"so its hooks, MCP servers and context stay as they were — make `%s %s` answer (repair or reinstall it), then re-apply"
	warnLoadoutUnknown = "companion %q: loadout probe failed (%v) and no earlier loadout of it is recorded, so it contributes " +
		"nothing this time — make `%s %s` answer (repair or reinstall it), then re-apply"
	warnLoadoutRecord = "companion %q: cannot record its loadout, so a later failed probe cannot carry it forward: %v"
)

// errLoadoutRecordName refuses a companion name that is not a bare file name,
// which the one-file-per-name record cannot hold.
var errLoadoutRecordName = errors.New("companion name is not a bare file name")

// loadoutRecordSuffix names one companion's record file: the raw envelope its
// probe printed.
const loadoutRecordSuffix = ".json"

// lastKnownLoadoutPath is bin's record file.
func lastKnownLoadoutPath(bin string) (string, error) {
	if bin == "" || filepath.Base(bin) != bin || bin == "." || bin == ".." {
		return "", fmt.Errorf("%w: %q", errLoadoutRecordName, bin)
	}
	dir, err := paths.HomeCompanionLoadoutsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, bin+loadoutRecordSuffix), nil
}

// recordLoadout keeps raw as bin's last-known envelope, rewriting only when it
// changed — a probe runs per generation and the bytes rarely move. A failure
// is warned: the probe itself succeeded, only the fallback is lost.
func recordLoadout(bin string, raw []byte) {
	path, err := lastKnownLoadoutPath(bin)
	if err == nil {
		if prev, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(prev, raw) { //nolint:gosec // a path this package named
			return
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			err = safefs.WriteFile(afero.NewOsFs(), path, raw, 0o600)
		}
	}
	if err != nil {
		clidiag.Warn("ctxloom", warnLoadoutRecord, bin, err)
	}
}

// forgetLoadout drops bin's record: it answered that it offers no loadout, so
// what it once offered must not come back on a later failure.
func forgetLoadout(bin string) {
	path, err := lastKnownLoadoutPath(bin)
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		clidiag.Warn("ctxloom", warnLoadoutRecord, bin, err)
	}
}

// lastKnownLoadout is bin's recorded envelope, unwrapped, or false when there
// is none usable.
func lastKnownLoadout(bin string) (doc, sig []byte, ok bool) {
	path, err := lastKnownLoadoutPath(bin)
	if err != nil {
		return nil, nil, false
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path this package named
	if err != nil {
		return nil, nil, false
	}
	doc, sig, _, err = signing.ParseLoadoutEnvelope(raw)
	return doc, sig, err == nil
}

// failedLoadout settles a probe that produced no loadout of its own. A clean
// "none" answer contributes nothing and forgets the record. A failure carries
// the last-known loadout forward when there is one, and says which companion
// and what to fix either way.
func failedLoadout(bin, path string, err error) (*bundles.CompanionLoadout, *bundles.CompanionCandidate) {
	if errors.Is(err, ErrLoadoutUnsupported) {
		forgetLoadout(bin)
		return nil, &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateNoLoadout}
	}
	if doc, sig, ok := lastKnownLoadout(bin); ok {
		clidiag.Warn("ctxloom", warnLoadoutCarried, bin, err, path, strings.Join(loadoutArgs, " "))
		return &bundles.CompanionLoadout{Bin: bin, Path: path, Document: doc, Signature: sig, Self: bin == SelfCompanion}, nil
	}
	clidiag.Warn("ctxloom", warnLoadoutUnknown, bin, err, path, strings.Join(loadoutArgs, " "))
	return nil, &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateProbeFailed}
}
