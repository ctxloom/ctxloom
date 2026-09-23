package sessions

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Layout is the harp-keyed tree under the ctxloom home (Root). The project
// tree holds NO session state: the session engine homes, the spool, the
// transcripts and every other member of a session are paths.HarpMembers rows under
// Dir(harp), and Member is the one join that places them.
//
// Layout does not validate the harp: it composes paths for a harp the caller
// already holds as an Identity (Identity.Validate) or resolved through the
// validating paths.HarpDir family, which HomeLayout is asserted to agree
// with.
type Layout struct{ Root string }

// HomeLayout is the Layout over the resolved ctxloom home — the same root
// every paths.Harp* helper resolves through, so the two are one tree.
func HomeLayout() (Layout, error) {
	root, err := paths.HomeConfigDir()
	if err != nil {
		return Layout{}, err
	}
	return Layout{Root: root}, nil
}

// SessionsRoot is the directory holding every session dir.
func (l Layout) SessionsRoot() string { return filepath.Join(l.Root, paths.SessionsDir) }

// Dir is the session dir.
func (l Layout) Dir(harp string) string { return filepath.Join(l.SessionsRoot(), harp) }

// Member places one table row under the session dir.
func (l Layout) Member(harp string, m paths.HarpMember) string {
	return filepath.Join(l.Dir(harp), filepath.FromSlash(m.Rel()))
}

func (l Layout) member(harp, name string) string {
	for _, m := range paths.HarpMembers {
		if m.Name == name {
			return l.Member(harp, m)
		}
	}
	panic("paths.HarpMembers has no row named " + name)
}

// SessionEngineHomes is the session's engine config-home CONTAINER: the
// home/ member under which each engine's own config-home instance (settings,
// the MCP registration, commands, skills, the context file, the credential
// copy) lands at its own leaf — under the ctxloom home, never the project
// tree.
func (l Layout) SessionEngineHomes(harp string) string {
	return l.member(harp, paths.SessionEngineHomesDirName)
}

// Persist is what survives workspace teardown.
func (l Layout) Persist(harp string) string { return l.member(harp, paths.PersistDirName) }

// Ephemeral is the session's scratch.
func (l Layout) Ephemeral(harp string) string { return l.member(harp, paths.EphemeralDirName) }

// Segments holds the per-native-session distilled segments.
func (l Layout) Segments(harp string) string { return l.member(harp, paths.SegmentsDirName) }

// Spool stays under persist/: that directory is what the container's
// session-state mount carries, and container mail rides it.
func (l Layout) Spool(harp string) string { return l.member(harp, paths.SpoolDirName) }

// Sidecar is the identity member: the file whose presence makes the dir a
// session.
func (l Layout) Sidecar(harp string) string { return l.Member(harp, paths.IdentityMember()) }

// KeepMarker is the reaper's hand-placed exemption: a file whose presence
// spares the session under every scope.
func (l Layout) KeepMarker(harp string) string {
	return l.member(harp, paths.SessionKeepMarkerFileName)
}

// Distilled reports whether the session dir holds an essence. Without one the
// transcript is the session's ONLY record, which is what every destroyer of
// transcripts asks before taking one.
//
// It asks the disk, never the index's Summary: the index carries a Summary
// long before any essence has been written (a harp rename, a resume pass),
// so a Summary test would pass the one session this exists to protect.
func Distilled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, paths.EssenceFileName))
	return err == nil
}
