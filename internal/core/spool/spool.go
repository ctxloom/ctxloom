// Package spool is the file-based message substrate for agent coordination.
//
// A message is a FILE in a per-session spool directory rooted at
// ~/.ctxloom/sessions/<harp>/persist/spool (paths.HarpPersistDir). The file is
// the durable truth and the only carrier of payload; any wire traffic that
// accompanies it carries a REFERENCE (a Ref) and nothing else, so a lost
// notification costs latency and never a message — a sweep of the directory
// re-derives the whole picture.
//
// Three properties this package exists to hold, each of which has a named
// counter-example in the design record:
//
//   - A Ref is view-independent. The same file is
//     /home/<user>/.ctxloom/sessions/<harp>/persist/spool/in/<name> on the host
//     and <containerHome>/.ctxloom/sessions/<harp>/persist/spool/in/<name>
//     inside a container. A raw absolute path is a SENDER-view artifact that
//     resolves to nothing (or to something unintended) on the other side, so
//     what travels is harp+dir+name and each side renders its own view through
//     its own PathMapper.
//   - Refs arrive from a less-trusted peer, so Resolve is the validation
//     chokepoint: the harp must satisfy the harp grammar (harp.Validate, the
//     same guard paths.HarpDir applies for the same reason) and the name must
//     be a bare filename. Rejected, never sanitised — a sanitiser turns a
//     hostile ref into a plausible one.
//   - Nothing is ever silently dropped. A malformed file is a named Problem
//     returned from Sweep, not a skipped entry; a rename that lost its race is
//     ErrAlreadyGone, not a generic error a caller can mistake for success.
//
// Layering: this package depends on internal/core/paths and internal/shared/harp
// only. internal/core/coord depends on IT.
package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// Dir is the closed set of spool subdirectories a Ref may name. It is a
// closed enum on purpose: an unknown directory string must fail loudly at the
// chokepoint rather than reach a path join.
type Dir string

const (
	// DirIn holds messages TO the agent whose spool this is. Single writer:
	// the coordinator.
	DirIn Dir = "in"
	// DirOut holds messages FROM that agent. Single writer: its runner.
	DirOut Dir = "out"
	// DirOutConsumed holds out/ messages the coordinator processed.
	DirOutConsumed Dir = "out/consumed"
	// DirInWithdrawn holds in/ messages the WRITER retracted before they were
	// consumed. Rename-won means retracted; ENOENT means the reader won.
	DirInWithdrawn Dir = "in/withdrawn"
)

// SpoolDirName is the spool root's name under the session persist dir — the
// table row paths.HarpMembers marks Mounted, so container mail rides the
// session-state mount.
const SpoolDirName = paths.SpoolDirName

// tmpDirName is the write-staging directory, a SIBLING of in/ and out/ under
// the spool root. Same filesystem as every target dir, so the publish rename
// is atomic by construction; a separate dir (rather than a dot-prefixed name
// in the target) means a reader never has to know which files to ignore, and
// a crash leaves debris nowhere a sweep will look.
const tmpDirName = "tmp"

// allDirs is every Dir, in creation order (parents before children).
var allDirs = []Dir{DirIn, DirOut, DirOutConsumed, DirInWithdrawn}

// Dirs returns every Dir in the closed set, in creation order (parents before
// children).
//
// It exists so that a table in ANOTHER package can be checked for
// exhaustiveness against the authority rather than against a second hand-kept
// list — notably the wire-enum mapping the doorbell needs, which must fail a
// test the day a sixth directory is added instead of silently not mapping it.
// The returned slice is a copy: an exhaustiveness authority a caller can
// mutate is not one.
func Dirs() []Dir { return append([]Dir(nil), allDirs...) }

// Valid reports whether d is one of the closed set.
func (d Dir) Valid() bool {
	for _, known := range allDirs {
		if d == known {
			return true
		}
	}
	return false
}

// Validate returns a non-nil error for any Dir outside the closed set,
// including the empty string (the unspecified value a wire enum decodes to).
func (d Dir) Validate() error {
	if d == "" {
		return fmt.Errorf("spool: directory is required (unspecified is never valid)")
	}
	if !d.Valid() {
		return fmt.Errorf("spool: unknown spool directory %q: want one of %s", string(d), dirList())
	}
	return nil
}

// String renders the slash-separated logical form ("in/consumed").
func (d Dir) String() string { return string(d) }

// Consumed returns the directory a message in d is renamed into when it is
// consumed. Only out/ has one: an in/ message is not moved when it is
// delivered but deleted, its identity recorded (Deliver), and consuming a
// file already in consumed/ or withdrawn/ is a caller bug, not a state.
func (d Dir) Consumed() (Dir, error) {
	if d == DirOut {
		return DirOutConsumed, nil
	}
	return "", fmt.Errorf("spool: %q has no consumed directory (only %q is consumable; an inbox message is delivered with Deliver)", string(d), string(DirOut))
}

// Withdrawn returns the directory a message in d is renamed into when its
// WRITER retracts it. Only in/ supports withdrawal: out/ is written by the
// agent and read by the coordinator, and an agent unsending its own report is
// not a state the design has.
func (d Dir) Withdrawn() (Dir, error) {
	if d == DirIn {
		return DirInWithdrawn, nil
	}
	return "", fmt.Errorf("spool: %q has no withdrawn directory (only %q is withdrawable)", string(d), string(DirIn))
}

func dirList() string {
	names := make([]string, 0, len(allDirs))
	for _, d := range allDirs {
		names = append(names, `"`+string(d)+`"`)
	}
	return strings.Join(names, ", ")
}

// Ref is the logical, view-independent identity of one spool file: what
// travels on the wire, what every log line names, and the only thing a
// PathMapper accepts. Never a filesystem path.
type Ref struct {
	// Harp names whose spool the file lives in. On the receiving side of a
	// wire frame this field is ADVISORY: a coordinator resolves refs from a
	// child's channel against THAT child's spool, never against a harp the
	// child names, or a child could aim the reader at a sibling's spool.
	Harp string
	// Dir is the subdirectory within that spool.
	Dir Dir
	// Name is the bare filename, e.g.
	// "00001754919000123456789.00000042.coord.md".
	Name string
}

// Validate checks every field as an untrusted wire value: harp against the
// harp grammar (harp.Validate — the same chokepoint paths.HarpDir uses), dir
// against the closed enum, name against the bare-filename grammar. It never
// repairs a value; a ref that does not validate is refused.
func (r Ref) Validate() error {
	if err := harpid.Validate(r.Harp); err != nil {
		return fmt.Errorf("spool: invalid ref harp: %w", err)
	}
	if err := r.Dir.Validate(); err != nil {
		return err
	}
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	return nil
}

// String renders the logical coordinate for logs: "<harp>:<dir>/<name>".
func (r Ref) String() string {
	return r.Harp + ":" + string(r.Dir) + "/" + r.Name
}

// ValidateName enforces the bare-filename grammar for a spool file name: one
// path segment, no separators, no "." or "..", no leading dot, no control
// characters.
//
// The ".." and separator rules are the traversal fence. They are stated
// SEPARATELY because they catch different attacks: "a/../../etc/passwd" is
// caught by the separator rule, but a bare ".." has no separator at all and
// would resolve to the spool directory's own parent.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("spool: file name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("spool: invalid file name %q: names a directory, not a message", name)
	}
	if strings.ContainsAny(name, `/\:`) {
		return fmt.Errorf("spool: invalid file name %q: must not contain a path separator (/, \\ or :)", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("spool: invalid file name %q: must not begin with a dot", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("spool: invalid file name %q: must not contain control characters", name)
		}
	}
	return nil
}

// PathMapper renders a logical Ref into THIS instance's filesystem view, and
// inverts it for files this instance wrote.
//
// It is an interface rather than a helper because its consumers hold
// different views of the same file: the wire doorbell (ref <-> the local view
// of whichever side received it), the readers (a runner's Home for a child's
// spool, the owner's turn-start hook for the owner's), and host-side operator
// tooling (host view).
//
// Implementations MUST validate the ref before joining anything: harp and
// name both arrive over the wire from a less-trusted peer. Resolve must fail
// loudly rather than guess — there is no "close enough" path.
type PathMapper interface {
	// Resolve returns the absolute path of ref in this instance's view.
	Resolve(ref Ref) (string, error)
	// RefOf inverts Resolve for a path in this instance's view, so a writer
	// can build a wire reference from the file it just renamed.
	RefOf(path string) (Ref, error)
}

// HomeMapper resolves refs against the HOME-relative session layout, and is
// the default mapper on BOTH sides of a container boundary.
//
// That one implementation serves both views is a property of the mount
// contract, and this doc is where that contract is stated rather than left as
// an accident two path joins happen to share: Container.sessionStateMounts
// binds host ~/.ctxloom/sessions/<harp>/persist to
// <containerHome>/.ctxloom/sessions/<harp>/persist — a HOME-RELATIVE target
// with an identical shape — and the child's env pins CTXLOOM_SESSION_HARP. So
// paths.HarpPersistDir(harp)+"/spool", which resolves against $HOME, yields
// the host view on the host and the container view in the container with zero
// extra plumbing. A mapper that resolved against the PROJECT instead would
// break that symmetry: the project tree is a different mount (and a different
// copy entirely for a worktree-base child).
//
// A runtime that ever breaks home-symmetry supplies its own PathMapper
// through the isolation policy rather than bending this one.
type HomeMapper struct{}

// NewHomeMapper returns the default home-relative mapper.
//
// It takes no home argument by design: home resolution belongs to
// internal/core/paths (os.UserHomeDir, honouring $HOME), and a second mechanism
// here would be a second answer to "where is home" that could disagree with
// every other ctxloom path.
func NewHomeMapper() HomeMapper { return HomeMapper{} }

// Resolve implements PathMapper.
func (HomeMapper) Resolve(ref Ref) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	root, err := homeSpoolRoot(ref.Harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(string(ref.Dir)), ref.Name), nil
}

// RefOf implements PathMapper. The path must be inside this view's sessions
// root and name a file in one of the closed set of spool directories;
// anything else is an error rather than a best-effort ref.
func (HomeMapper) RefOf(path string) (Ref, error) {
	if !filepath.IsAbs(path) {
		return Ref{}, fmt.Errorf("spool: cannot take a ref from relative path %q: an absolute path in this view is required", path)
	}
	sessions, err := paths.HomeSessionsDir()
	if err != nil {
		return Ref{}, err
	}
	rel, err := filepath.Rel(sessions, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Ref{}, fmt.Errorf("spool: path %q is outside the sessions root %q", path, sessions)
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	// <harp>/persist/spool/<dir...>/<name>: 4 fixed head segments plus at
	// least one dir segment and the name.
	const headSegments = 3 // harp, persist, spool
	if len(segs) < headSegments+2 {
		return Ref{}, fmt.Errorf("spool: path %q is not a spool file (too few path segments below %q)", path, sessions)
	}
	if segs[1] != paths.PersistDirName || segs[2] != SpoolDirName {
		return Ref{}, fmt.Errorf("spool: path %q is not a spool file (expected <harp>/%s/%s/<dir>/<name>)", path, paths.PersistDirName, SpoolDirName)
	}
	ref := Ref{
		Harp: segs[0],
		Dir:  Dir(strings.Join(segs[headSegments:len(segs)-1], "/")),
		Name: segs[len(segs)-1],
	}
	if err := ref.Validate(); err != nil {
		return Ref{}, fmt.Errorf("spool: path %q does not name a valid spool file: %w", path, err)
	}
	return ref, nil
}

// homeSpoolRoot is the one place the spool's location is composed:
// <persist>/spool, where persist is paths.HarpPersistDir (which validates the
// harp and resolves against $HOME).
func homeSpoolRoot(harp string) (string, error) {
	persist, err := paths.HarpPersistDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(persist, SpoolDirName), nil
}

// Root returns the spool root directory for harp in mapper m's view.
//
// It is derived from Resolve rather than added to the PathMapper interface so
// that EVERY mapper — including a future remote or rewriting one — gets a
// correct root for free, and cannot drift from the paths its own Resolve
// hands out.
func Root(m PathMapper, harp string) (string, error) {
	if m == nil {
		return "", errors.New("spool: a PathMapper is required")
	}
	probe, err := m.Resolve(Ref{Harp: harp, Dir: DirIn, Name: rootProbeName})
	if err != nil {
		return "", err
	}
	// probe is <root>/in/<name>: climb the name and the one dir segment.
	return filepath.Dir(filepath.Dir(probe)), nil
}

// rootProbeName is a syntactically valid name used only to derive the root
// from a Resolve. It is never created.
const rootProbeName = "0.probe.md"

// localDirs are the reader-local in-flight and terminal directories: siblings
// of in/ and out/ that a reader moves files into without any peer being told,
// so they carry no wire obligation and stay OUT of the closed Dir set (see
// ClaimedDirName and FailedDirName). They are addressable through DirPath and
// Sweep — the reader that owns them, and an operator, must be able to look —
// but never through a Ref, which is a wire value.
var localDirs = []Dir{ClaimedDirName, FailedDirName, FailedOutDirName}

// LocalDirs returns every reader-local directory, so a scanner that wants to
// surface what a reader set aside enumerates them from the authority rather
// than from a second hand-kept list.
func LocalDirs() []Dir { return append([]Dir(nil), localDirs...) }

// local reports whether d is one of the reader-local directories.
func (d Dir) local() bool {
	for _, known := range localDirs {
		if d == known {
			return true
		}
	}
	return false
}

// validateAddressable is the check for a directory a READER may name — the
// closed wire set plus the reader-local directories. It is deliberately not
// Validate, which stays the wire chokepoint.
func (d Dir) validateAddressable() error {
	if d.local() {
		return nil
	}
	return d.Validate()
}

// DirPath returns the absolute path of one spool directory in m's view: any
// member of the closed set, or a reader-local directory (LocalDirs).
func DirPath(m PathMapper, harp string, dir Dir) (string, error) {
	if err := dir.validateAddressable(); err != nil {
		return "", err
	}
	root, err := Root(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(string(dir))), nil
}

// EnsureDirs creates the whole spool layout for harp in m's view, including
// the staging directory. It is idempotent, and both sides of a mount may call
// it: the directories are shared bytes.
//
// The root is made owner-only first (ensureRoot), so everything beneath it
// is created inside that boundary.
func EnsureDirs(m PathMapper, harp string) error {
	root, err := ensureRoot(m, harp)
	if err != nil {
		return err
	}
	want := make([]string, 0, len(allDirs)+1)
	for _, d := range allDirs {
		want = append(want, filepath.Join(root, filepath.FromSlash(string(d))))
	}
	want = append(want, filepath.Join(root, tmpDirName))
	for _, dir := range want {
		if err := os.MkdirAll(dir, owneronly.DirMode); err != nil {
			return fmt.Errorf("spool: create %s: %w", dir, err)
		}
	}
	return nil
}

// ensureRoot creates harp's spool root if it is missing and makes it
// owner-only, returning it. The root is the spool's one protection boundary:
// the trust boundary is the user account (any same-user process can write
// into these dirs — stated in the design's honest-counter §9.4), and an
// owner-only root is what bounds it there. On unix nobody else can traverse
// into it whatever the modes below; on Windows its protected DACL is
// inherited by everything created beneath. It is re-applied on every call,
// so a root loosened after the fact is tightened by the next writer.
func ensureRoot(m PathMapper, harp string) (string, error) {
	root, err := Root(m, harp)
	if err != nil {
		return "", err
	}
	if err := owneronly.EnsureDir(root); err != nil {
		return "", fmt.Errorf("spool: restrict %s to its owner: %w", root, err)
	}
	return root, nil
}
