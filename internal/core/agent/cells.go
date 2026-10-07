package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// This file holds the cross-backend surface vocabulary (SurfaceKind and its
// parser) and the refusal an approach gives when the root it writes beneath was never
// resolved for the run.

// ErrUnrootedSessionHome is returned by an approach that writes beneath the
// SESSION HOME when that root was never resolved for the run. A "" root
// joined into a well-known path yields a bare relative path that lands
// wherever the process happens to be; and the tempting fallback — the engine's REAL home, ~/.claude and the
// like — is the user's own, shared across every session, and writing it
// because a private one was not advised is the shared/dangerous default the
// seam refuses to take on anyone's behalf. The remedy is in the message,
// because the refusal is the whole interface for the failure.
var ErrUnrootedSessionHome = errors.New("delivery: the session home was never resolved — this approach writes beneath the session's private home, which a run whose agent binding selects engine_home: host does not have; drop that selection, or select a project-file approach for this surface")

// SessionHomeRooted is the entry check for an approach that lands beneath the
// session home. It is exported
// because the approaches that need it live in the engine packages, and the
// seam wants them to refuse the same way rather than each inventing its
// own check.
func SessionHomeRooted(start present.Start) error {
	if start.Paths().SessionHome.Host == "" {
		return ErrUnrootedSessionHome
	}
	return nil
}

// SurfaceKind is present.Kind under this package's established name: the
// cross-backend category a delivery surface belongs to. The vocabulary is
// declared once, in core/present; the parser below stays here because it
// walks this package's surfaceOrder.
type SurfaceKind = present.Kind

const (
	SurfaceContext  = present.Context
	SurfaceMCP      = present.MCP
	SurfaceSettings = present.Settings
	SurfaceHooks    = present.Hooks
	SurfaceCommands = present.Commands
	SurfaceSkills   = present.Skills
)

// ParseSurfaceKind is SurfaceKind.String's inverse, and lives beside it so the
// two cannot drift: a kind renamed for a user's eyes is renamed for their
// keyboard in the same edit. It exists because the vocabulary is now typed by
// humans (`agent create --surface context=unsafe-file`), not only
// rendered to them.
//
// An unrecognised name is an ERROR rather than a zero value. SurfaceContext is
// iota 0, so returning the zero value on a typo would silently retarget an
// override at the context surface — the one kind whose delivery a user is most
// likely to be overriding on purpose.
func ParseSurfaceKind(s string) (SurfaceKind, error) {
	for _, k := range surfaceOrder {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, fmt.Errorf("unknown surface kind %q (known: %s)", s, strings.Join(SurfaceKindNames(), ", "))
}

// surfaceOrder is the stable cross-engine surface order — every Kind, in
// Kind order — so the names ParseSurfaceKind reads and SurfaceKindNames
// renders are one enumeration.
var surfaceOrder = []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceHooks, SurfaceCommands, SurfaceSkills}

// SurfaceKindNames lists every kind's label, in kind order. It reads
// surfaceOrder — the one enumeration — so error text, --help and shell
// completion cannot fall out of step with the enum or with each other. A kind
// added to surfaceOrder appears in all three without touching them.
func SurfaceKindNames() []string {
	names := make([]string, 0, len(surfaceOrder))
	for _, k := range surfaceOrder {
		names = append(names, k.String())
	}
	return names
}
