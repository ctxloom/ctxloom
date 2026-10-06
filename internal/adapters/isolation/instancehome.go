package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// InstanceHomeRequest is one preparation of a session's engine home: which
// engine, into which instance home, for which working directory.
type InstanceHomeRequest struct {
	// Engine is the REGISTERED backend name ("claude-code", ...).
	Engine string
	// InstanceHome is the session home to prepare — the directory the
	// engine's home var names (launch.SessionHome). The engine writes into
	// it; nothing appends a leaf of its own.
	InstanceHome string
	// WorkDir is the absolute project directory the run works in, passed
	// through to the engine's generated config so a workspace-trust answer can
	// be keyed to the directory the run actually uses. Empty is tolerated (the
	// engine skips its per-project half and says so).
	WorkDir string
	// Trust is the engine's verdict on WorkDir's repository (repoTrust):
	// the engine writes its trust answer only for a trusted one.
	Trust engine.WorkspaceTrust
	// Auth is the run's auth mode (engine.Credentials.Mode): only the human's
	// own login session's instance carries the login's credential half.
	Auth engine.AuthMode
	// NativeHome is where the session keeps the engine's native history
	// (launch.NativeHome); "" when it keeps none here, and then nothing is
	// linked.
	NativeHome string
	// HistoryInHome is that the engine runs where a link into NativeHome does
	// not resolve (a container, on a host whose links name absolute paths:
	// platform.DirLinker.LinksResolveInContainers), so the home keeps its
	// history as a real directory, started from NativeHome's
	// (restoreNativeHistory) instead of linked to it.
	HistoryInHome bool
	// Root is ctxloom's root the home is prepared on: its Private makes the
	// home owner-only and checks what the engine wrote. Zero is the
	// controller's own filesystem (safefs.New).
	Root safefs.Root
}

// root is req.Root, or the controller's own filesystem when none was given.
func (req InstanceHomeRequest) root() safefs.Root {
	if req.Root.Fs == nil {
		return safefs.New()
	}
	return req.Root
}

// InstanceHomeReport is what one PrepareInstanceHome call decided and wrote.
type InstanceHomeReport struct {
	// Generated lists the paths the ENGINE's own instance-config writer wrote
	// (claude's .claude.json, for instance).
	Generated []string
	// Warnings carries the engine's fail-loud notices (host-file schema drift,
	// a precedence file shadowing what was written). PrepareInstanceHome
	// prints them; they are also returned so a test can assert on them.
	Warnings []string
}

// PrepareInstanceHome readies a session's engine home: it asks the ENGINE
// to generate its own instance config (claude's field-scoped .claude.json).
// Every byte-level edit of a vendor's format happens inside that vendor's
// package.
//
// The home is owner-only (Root.Private.Ensure) BEFORE the engine writes, so
// what the engine writes inherits it where the platform's protection is an
// inherited ACL (Windows), and a home that exists loosened is tightened. The
// home and every file the engine reports writing are then held to
// owner-only, and a home that fails it is an error, not a warning: it holds
// the run's trust answer and account identity, the same exposure the
// credential store refuses.
//
// No credential file is placed in the home: a run authenticates in its mode
// (engine.Auth.Credentials), settled before the home is prepared. The mode
// rides req.Auth only so the engine can keep a login's credential half (claude:
// primaryApiKey) to the human's own login session.
//
// The real host home is READ and never written by this call; tests/arch's
// real-home byte-identity gate is what proves it.
//
// The whole operation is serialized under the project lock keyed to
// req.InstanceHome, so two launches sharing ONE session instance (two under
// the same harp, as a resume is) cannot interleave their load-modify-write of
// the same config file. A delegated child is not one of them: it gets a harp,
// and so an instance, of its own. An instance home the lock cannot be keyed
// for, or whose lock cannot be held, is refused (errInstanceHomeUnkeyed,
// errInstanceHomeUnlocked) rather than prepared unserialized.
func PrepareInstanceHome(req InstanceHomeRequest) (InstanceHomeReport, error) {
	f, ok := factsFor(req.Engine)
	if !ok {
		return InstanceHomeReport{}, fmt.Errorf("instance home: backend %q is not a composed engine (internal error)", req.Engine)
	}
	if req.InstanceHome == "" {
		return InstanceHomeReport{}, fmt.Errorf("instance home for %s: no instance home to prepare (internal error)", req.Engine)
	}
	unlock, err := lockInstanceHome(req.InstanceHome)
	if err != nil {
		return InstanceHomeReport{}, fmt.Errorf("instance home for %s: %w", req.Engine, err)
	}
	defer unlock()

	var rep InstanceHomeReport
	if err := req.root().Private.Ensure(req.InstanceHome); err != nil {
		return rep, fmt.Errorf("instance home for %s: restrict %s to its owner: %w", req.Engine, req.InstanceHome, err)
	}
	if req.NativeHome != "" && f.Home.TranscriptStoreRel != "" {
		place := linkNativeHistory
		if req.HistoryInHome {
			place = restoreNativeHistory
		}
		if err := place(req.InstanceHome, req.NativeHome, f.Home.TranscriptStoreRel); err != nil {
			return rep, fmt.Errorf("instance home for %s: %w", req.Engine, err)
		}
	}
	if f.Home.InstanceConfig == nil {
		return rep, nil
	}
	return writeInstanceConfig(req, f.Home.InstanceConfig)
}

// writeInstanceConfig has the engine write its own instance config into the
// prepared home, then holds the home and everything written to owner-only.
func writeInstanceConfig(req InstanceHomeRequest, writer engine.InstanceConfigWriter) (InstanceHomeReport, error) {
	var rep InstanceHomeReport
	hostHome, err := hostHomeDir()
	if err != nil {
		hostHome = ""
	}
	engineRep, err := writer.WriteInstanceConfig(engine.InstanceConfigRequest{
		HostHome:     hostHome,
		InstanceHome: req.InstanceHome,
		WorkDir:      req.WorkDir,
		Trust:        req.Trust,
		Auth:         req.Auth,
	}, nil)
	rep.Generated = engineRep.Wrote
	rep.Warnings = engineRep.Warnings
	for _, w := range rep.Warnings {
		clidiag.Warn("ctxloom", "%s instance config: %s", req.Engine, w)
	}
	if err != nil {
		return rep, err
	}
	if err := req.root().Private.Check(append([]string{req.InstanceHome}, rep.Generated...)...); err != nil {
		return rep, fmt.Errorf("instance home for %s: %w", req.Engine, err)
	}
	return rep, nil
}

// ErrHistoryNotLinked is the refusal of a session home whose history dir is
// neither the link into native/ nor a real directory to adopt: a link
// somewhere else, or something that is not a directory. It is not the
// session's history, so it is neither replaced nor moved.
var ErrHistoryNotLinked = errors.New("the session home's history dir is not the link into the session's native history")

// linkNativeHistory makes <instanceHome>/<rel> the platform's directory link
// (platform.DirLinker) to <nativeHome>/<rel>, creating the target. An
// existing correct link is left alone; a real directory there (a container
// run's history on a host whose links do not resolve in a container, or a
// home that predates native history) is adopted first (adoptHistory);
// anything else is ErrHistoryNotLinked.
func linkNativeHistory(instanceHome, nativeHome, rel string) error {
	target := filepath.Join(nativeHome, filepath.FromSlash(rel))
	if err := os.MkdirAll(target, safefs.PrivateDirMode); err != nil {
		return fmt.Errorf("native history %s: %w", target, err)
	}
	link := filepath.Join(instanceHome, filepath.FromSlash(rel))
	at, err := historyAt(link, nativeHome, rel)
	switch {
	case err != nil:
		return err
	case at == historyLinked:
		return nil
	case at == historyRealDir:
		if err := sessions.AdoptHistory(afero.NewOsFs(), link, target); err != nil {
			return fmt.Errorf("native history: move %s into %s: %w", link, target, err)
		}
	case at == historyLinkedBeforeRename:
		if err := hostOS.UnlinkDir(link); err != nil {
			return fmt.Errorf("native history link %s: %w", link, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), safefs.PrivateDirMode); err != nil {
		return fmt.Errorf("native history link %s: %w", link, err)
	}
	if err := hostOS.LinkDir(link, target); err != nil {
		return fmt.Errorf("native history link %s: %w", link, err)
	}
	return nil
}

// historyState is what sits where a session home's history link belongs.
type historyState int

const (
	// historyAbsent: nothing.
	historyAbsent historyState = iota
	// historyLinked: the link into the session's native history.
	historyLinked
	// historyLinkedBeforeRename: the link the session made under a name it
	// has since been renamed from (renamedSessionLink).
	historyLinkedBeforeRename
	// historyRealDir: a real directory of history.
	historyRealDir
)

// historyAt is what sits at link, the history link into
// <nativeHome>/<rel>. Anything that is not one of historyState's is
// ErrHistoryNotLinked.
func historyAt(link, nativeHome, rel string) (historyState, error) {
	target := filepath.Join(nativeHome, filepath.FromSlash(rel))
	ok, err := hostOS.LinksTo(link, target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return historyAbsent, nil
	case err != nil:
		return historyAbsent, fmt.Errorf("native history link %s: %w", link, err)
	case ok:
		return historyLinked, nil
	case sessions.IsRealDir(afero.NewOsFs(), link):
		return historyRealDir, nil
	case renamedSessionLink(link, nativeHome, rel):
		return historyLinkedBeforeRename, nil
	}
	return historyAbsent, fmt.Errorf("%w: %s", ErrHistoryNotLinked, link)
}

// renamedSessionLink reports whether the link at link names this session's
// native history as it was before the session was renamed. A link that
// names its target absolutely (a Windows junction) keeps naming the old
// session dir after Manager.Rename moves the session, history and all; the
// home holding the link moved with it, so a link of the exact shape
// <sessions root>/<old name>/<native>/<leaf>/<rel> whose old session dir no
// longer exists can only be this session's own. One into a session dir that
// exists is another session's history.
func renamedSessionLink(link, nativeHome, rel string) bool {
	got, err := hostOS.LinkTarget(link)
	if err != nil {
		return false
	}
	sessionDir := filepath.Dir(filepath.Dir(nativeHome))
	suffix, err := filepath.Rel(sessionDir, filepath.Join(nativeHome, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	oldName := filepath.Base(strings.TrimSuffix(got, string(filepath.Separator)+suffix))
	oldDir := filepath.Join(filepath.Dir(sessionDir), oldName)
	if oldDir == sessionDir || filepath.Join(oldDir, suffix) != got {
		return false
	}
	_, err = os.Lstat(oldDir)
	return errors.Is(err, fs.ErrNotExist)
}

// lockFileMode and lockDirMode are the modes this instance-home lock's
// sidecar and its parent directory are created with, before umask — not
// group- or world-WRITABLE, matching every other lock site in this project
// (see internal/core/agent/rmw_lock.go's identically-reasoned pair).
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// errInstanceHomeUnkeyed refuses an instance home the home lock store cannot
// key (paths.HomePathFor failed), so it cannot be serialized.
var errInstanceHomeUnkeyed = errors.New("instance home: cannot key the instance lock")

// errInstanceHomeUnlocked refuses an instance home whose lock could be keyed
// but not held — its lock directory cannot be created or the lock cannot be
// taken — so it cannot be serialized either.
var errInstanceHomeUnlocked = errors.New("instance home: cannot take the instance lock")

// lockInstanceHome takes the lock for instanceHome and returns the release.
// The instance is a member of the home-rooted session dir (paths.HarpSessionEngineHomes),
// so its sidecar goes to the home locks store (paths.HomePathFor) — the
// session dir itself holds only paths.HarpMembers. A home that cannot be
// keyed to a lock location is refused (errInstanceHomeUnkeyed), and so is
// one whose lock directory cannot be created or whose lock cannot be taken
// (errInstanceHomeUnlocked): no path proceeds without holding the lock.
func lockInstanceHome(instanceHome string) (func(), error) {
	lockPath, err := paths.HomePathFor(instanceHome)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errInstanceHomeUnkeyed, instanceHome, err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		return nil, fmt.Errorf("%w %s: prepare the lock directory: %w", errInstanceHomeUnlocked, lockPath, err)
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	err = fl.Lock()
	stop()
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errInstanceHomeUnlocked, lockPath, err)
	}
	return func() { _ = fl.Unlock() }, nil
}
