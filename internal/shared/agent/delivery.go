package agent

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// This file defines the handle every delivery returns and the session-scoped
// scratch placement a shared-cwd launch writes its race-safe files into.

// Delivered is the handle returned from delivering one surface of a loadout: it
// owns the cleanup that undoes that delivery. Each mechanism strategy returns a
// Delivered whose Cleanup reverses exactly what the strategy did — remove the
// written file, reconcile a settings edit, deregister an MCP server, or no-op
// for an in-band delivery that leaves nothing behind.
type Delivered interface {
	// Cleanup undoes the delivery this handle represents (rm file / reconcile /
	// deregister / no-op).
	Cleanup() error
}

// ephemeralPlacement writes into the session's regenerable ephemeral directory
// (~/.ctxloom/sessions/<harp>/ephemeral): the Placement for surfaces whose
// materialized files are session-scoped scratch that teardown may discard. When
// harp is empty or its ephemeral dir cannot be resolved, it falls back to the OS
// temp dir so a file-writing strategy always has a writable location.
type ephemeralPlacement struct {
	harp string
}

// Dir returns the harp's ephemeral directory, falling back to os.TempDir() when
// harp is empty or the ephemeral dir cannot be resolved.
func (p ephemeralPlacement) Dir() string {
	if p.harp != "" {
		if dir, err := paths.HarpEphemeralDir(p.harp); err == nil {
			return dir
		}
	}
	return os.TempDir()
}
