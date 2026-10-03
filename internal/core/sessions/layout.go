package sessions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

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

// Scratch is the session's per-run scratch root.
func (l Layout) Scratch(harp string) string { return l.member(harp, paths.ScratchDirName) }

// Work is where the session's worktree checkouts live.
func (l Layout) Work(harp string) string { return l.member(harp, paths.WorkDirName) }

// Native is the root of the session's native engine histories.
func (l Layout) Native(harp string) string { return l.member(harp, paths.NativeDirName) }

// Transcripts holds the raw transcript forms.
func (l Layout) Transcripts(harp string) string { return l.member(harp, paths.TranscriptsDirName) }

// Segments holds the per-rotation canonical segments.
func (l Layout) Segments(harp string) string { return l.member(harp, paths.SegmentsDirName) }

// Spool is the session's mail spool.
func (l Layout) Spool(harp string) string { return l.member(harp, paths.SpoolDirName) }

// Sidecar is the identity member: the file whose presence makes the dir a
// session.
func (l Layout) Sidecar(harp string) string { return l.Member(harp, paths.IdentityMember()) }

// KeepMarker is the reaper's hand-placed exemption: a file whose presence
// spares the session under every scope.
func (l Layout) KeepMarker(harp string) string {
	return l.member(harp, paths.SessionKeepMarkerFileName)
}

// Distilled reports whether the session in dir has an essence: its recorded
// output dir holds one. Without one the transcript is the session's ONLY
// record, which is what every destroyer of transcripts asks before taking
// one. A session with no recorded output dir has nowhere an essence could be,
// so it is undistilled.
//
// It asks the disk, never the index's Summary: the index carries a Summary
// long before any essence has been written (a harp rename, a resume pass),
// so a Summary test would pass the one session this exists to protect.
func Distilled(dir string) bool {
	out, ok := OutputDirOf(dir)
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join(out, paths.EssenceFileName))
	return err == nil
}

// OutputDirOf reads the output dir recorded in the sidecar of the session in
// dir. ok is false when there is no readable sidecar or it records none.
func OutputDirOf(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, paths.SessionSidecarFileName))
	if err != nil {
		return "", false
	}
	var e Entry
	if yaml.Unmarshal(data, &e) != nil || e.OutputDir == "" {
		return "", false
	}
	return e.OutputDir, true
}

// OutputDirIn is harp's output dir as the calling process reaches it:
// EnvOutputDir when the environment names one — a containerized run, whose
// container serves exactly one session and mounts its output dir there —
// else the path the sidecar records (OutputDir).
func OutputDirIn(harp string, getenv func(string) string) (string, error) {
	if dir := getenv(EnvOutputDir); dir != "" {
		return dir, nil
	}
	return OutputDir(harp)
}

// ErrNoOutputDir is the refusal when a session records no output dir: one
// minted before output dirs existed, or a mint whose RecordOutputDir failed.
var ErrNoOutputDir = errors.New("the session records no output dir")

// OutputDir is harp's recorded output dir, read from its sidecar under the
// resolved home. The recorded path is the answer even if the output_dir
// config key has changed since: it is where that session's outputs are. A
// harp with no sidecar is ErrNotFound — there is no such session — and one
// whose sidecar records none is ErrNoOutputDir.
func OutputDir(harp string) (string, error) {
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, paths.SessionSidecarFileName)); os.IsNotExist(err) {
		return "", fmt.Errorf("%w: %q", ErrNotFound, harp)
	}
	out, ok := OutputDirOf(dir)
	if !ok {
		return "", fmt.Errorf("session %s: %w", harp, ErrNoOutputDir)
	}
	return out, nil
}
