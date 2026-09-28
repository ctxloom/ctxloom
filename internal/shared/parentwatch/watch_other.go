//go:build !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package parentwatch

import "context"

// WithParent passes ctx through on the platforms that carry this guarantee
// OUTSIDE the child. Linux: the spawner armed PR_SET_PDEATHSIG
// (isolation's setRunnerPdeathsig), and a second in-child watcher would only
// race it. Windows: there is no parent-exit event to watch; the guarantee
// belongs to a Job Object at the spawn site (isolation's
// procsession_windows.go), not here. It never fails.
func WithParent(ctx context.Context) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, cancel, nil
}
