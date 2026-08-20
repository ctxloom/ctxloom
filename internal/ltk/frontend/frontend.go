// Package frontend defines the polymorphic seam between shell dialects and the
// IR. Each shell family implements Frontend; a Registry dispatches a command
// string to the right one by Shell. The rest of the system depends only on this
// interface and on ir.Script, never on a concrete parser.
package frontend

import (
	"context"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// ErrUnsupportedShell is returned when no frontend is registered for a shell.
var ErrUnsupportedShell = errors.New("unsupported shell")

// Frontend parses raw command strings for one or more shell dialects into the
// common Command-Graph IR.
type Frontend interface {
	// Shells reports the dialects this frontend can parse.
	Shells() []ir.Shell
	// Parse lowers src for the given shell into a Script. The shell is always
	// one returned by Shells. On a parse error a frontend should still return a
	// non-nil (possibly empty) Script alongside the error so callers can apply
	// an on_parse_error policy.
	Parse(ctx context.Context, shell ir.Shell, src string) (*ir.Script, error)
}

// Registry maps shells to the frontend that handles them.
type Registry struct {
	byShell map[ir.Shell]Frontend
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byShell: make(map[ir.Shell]Frontend)}
}

// Register adds f for each shell it reports. A later registration for the same
// shell wins, which lets callers override a default frontend.
func (r *Registry) Register(f Frontend) {
	for _, sh := range f.Shells() {
		r.byShell[sh] = f
	}
}

// Parse dispatches to the frontend registered for shell. It stamps the shell
// onto the returned script when the frontend left it blank.
func (r *Registry) Parse(ctx context.Context, shell ir.Shell, src string) (*ir.Script, error) {
	f, ok := r.byShell[shell]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedShell, shell)
	}
	sc, err := f.Parse(ctx, shell, src)
	if sc != nil && sc.Shell == "" {
		sc.Shell = shell
	}
	return sc, err
}
