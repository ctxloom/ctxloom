package companions

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ===== A failed loadout probe: an answer, or an unknown =====================
//
// A verified companion's loadout probe can end without a loadout in two ways
// that mean opposite things. The companion may ANSWER that it offers none —
// it ran and exited with a status, the shape of a binary with no `loadout`
// subcommand — and then it contributes nothing, as a fact, quietly. Or the
// probe may FAIL — a timeout, a signal, an exec error, bytes that are not an
// envelope — and then what it contributes is UNKNOWN, which is said out loud.

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

// warnLoadoutFailed names the companion and the command that must answer for
// the warning to stop.
const warnLoadoutFailed = "companion %q: loadout probe failed (%v), so its hooks, MCP servers and context are unknown and it " +
	"contributes nothing this time — make `%s %s` answer (repair or reinstall it), then re-apply"

// failedLoadout settles a probe that produced no loadout: a clean "none"
// answer is a quiet no-loadout candidate, a failure a warned probe-failed one.
func failedLoadout(bin, path string, err error) *bundles.CompanionCandidate {
	if errors.Is(err, ErrLoadoutUnsupported) {
		return &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateNoLoadout}
	}
	clidiag.Warn("ctxloom", warnLoadoutFailed, bin, err, path, strings.Join(loadoutArgs, " "))
	return &bundles.CompanionCandidate{Bin: bin, Path: path, Reason: bundles.CandidateProbeFailed}
}
