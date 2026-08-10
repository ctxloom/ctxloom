package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// The hold helpers behind `ctxloom deps hold` / `deps unhold` (declared in
// deps.go). A hold freezes a bundle at its currently-locked SHA so
// `deps upgrade` will not advance it; unhold releases that freeze. Per-item
// content review is a different question and lives in `ctxloom review`.

// holdItem sets the hold flag on name's active lockfile entry and reports the
// outcome to out (or, when there was nothing to flip, to errOut via
// reportNothingToHold).
func holdItem(cfg *config.Config, name string, out, errOut io.Writer) error {
	found, err := operations.SetItemPin(cfg, name, true)
	if err != nil {
		return err
	}
	if !found {
		return reportNothingToHold(cfg, name, "hold", errOut)
	}
	fmt.Fprintf(out, "Held %q at its locked SHA.\n", name)
	return nil
}

// reportNothingToHold handles the branch where SetItemPin flipped nothing: the
// name has no entry in the active lockfile. That covers two situations the
// lockfile cannot tell apart, and they are not the same kind of event.
//
//   - The name IS a bundle this project carries locally. A local bundle has no
//     upstream and no lockfile entry, so `deps upgrade` can never advance it —
//     the guarantee "hold" exists to give is already unconditionally true, and
//     "unhold" has nothing to release either. The user's requested postcondition
//     holds. That is a benign no-op: say so and exit 0. That contract is
//     specified in tests/acceptance/features/cli/deps.feature ("Holding a local
//     bundle reports it is not lockfile-tracked" — hold AND unhold succeed).
//
//   - The name resolves to NOTHING — a typo, the wrong project, or a `deps
//     pull` that never ran. Here the user asked to freeze a dependency, nothing
//     was frozen, and exit 0 tells them it was: the exit-0-on-failure family.
//     Refuse, and name the recovery.
//
// The notice is a diagnostic about work NOT done, so it rides stderr even on the
// exit-0 path — `ctxloom deps hold x > pins.txt` should not collect it.
func reportNothingToHold(cfg *config.Config, name, verb string, errOut io.Writer) error {
	if _, err := operations.GetBundle(cfg, name); err != nil {
		return fmt.Errorf("%q is neither a local bundle nor an entry in the active lockfile, so there was nothing to %s — run `ctxloom deps pull` to lock it, or `ctxloom deps list` to check the name", name, verb)
	}
	fmt.Fprintf(errOut, "%q is a local bundle, not tracked in the active lockfile; nothing to %s. `deps upgrade` never advances a local bundle, so it is already frozen.\n", name, verb)
	return nil
}
