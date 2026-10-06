package paths

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// EnsureHomeRoots establishes the home directories that hold private state —
// the coordinator roots (HomeCoordDir), the sessions (HomeSessionsDir: spool,
// scratch and engine homes live under each sessions/<harp>/) and the
// ownership records (HomeRecordsDir) — owner-only through private. Every
// ctxloom process that may write beneath them calls it once at startup;
// below them, writers only create directories (safefs.PrivateDirMode),
// because an owner-only parent is what protects them: on unix a 0700 parent
// blocks traversal, on Windows its protected DACL is inherited. A root
// loosened after startup stays loose until the next process establishes it.
func EnsureHomeRoots(private safefs.Private) error {
	var errs []error
	for _, dir := range []func() (string, error){HomeCoordDir, HomeSessionsDir, HomeRecordsDir} {
		d, err := dir()
		if err == nil {
			err = private.Ensure(d)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("establish ctxloom's private home roots: %w", err)
	}
	return nil
}
