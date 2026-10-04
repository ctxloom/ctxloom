package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
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

// ensureOwnerOnlyDir is owneronly.EnsureDir, indirected so a test can make
// it fail: no ACL a test can write stops an elevated Windows administrator
// (the account CI runs as) from replacing a DACL, so the failure has no
// honest on-disk fixture there.
var ensureOwnerOnlyDir = owneronly.EnsureDir

// PrepareInstanceHome readies a session's engine home: it asks the ENGINE
// to generate its own instance config (claude's field-scoped .claude.json).
// Every byte-level edit of a vendor's format happens inside that vendor's
// package.
//
// The home is owner-only (owneronly.EnsureDir) BEFORE the engine writes, so
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
// and so an instance, of its own. An instance home outside any
// .ctxloom tree cannot be keyed and proceeds unlocked — that is only ever the
// harpless worktree fallback under the OS temp dir, whose home is per-AGENT and
// therefore has no second writer to race.
func PrepareInstanceHome(req InstanceHomeRequest) (InstanceHomeReport, error) {
	f, ok := factsFor(req.Engine)
	if !ok {
		return InstanceHomeReport{}, fmt.Errorf("instance home: backend %q is not a composed engine (internal error)", req.Engine)
	}
	if req.InstanceHome == "" {
		return InstanceHomeReport{}, fmt.Errorf("instance home for %s: no instance home to prepare (internal error)", req.Engine)
	}
	unlock := lockInstanceHome(req.InstanceHome)
	defer unlock()

	var rep InstanceHomeReport
	if err := ensureOwnerOnlyDir(req.InstanceHome); err != nil {
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
	if err := owneronly.Check(append([]string{req.InstanceHome}, rep.Generated...)...); err != nil {
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
	if err := os.MkdirAll(target, owneronly.DirMode); err != nil {
		return fmt.Errorf("native history %s: %w", target, err)
	}
	link := filepath.Join(instanceHome, filepath.FromSlash(rel))
	linked, err := historyLinked(link, target)
	if err != nil || linked {
		return err
	}
	if isRealDir(link) {
		if err := adoptHistory(link, target); err != nil {
			return fmt.Errorf("native history: move %s into %s: %w", link, target, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), owneronly.DirMode); err != nil {
		return fmt.Errorf("native history link %s: %w", link, err)
	}
	if err := hostOS.LinkDir(link, target); err != nil {
		return fmt.Errorf("native history link %s: %w", link, err)
	}
	return nil
}

// historyLinked reports whether link is the history link to target. Nothing
// at link, or a real directory, is false; anything else is
// ErrHistoryNotLinked.
func historyLinked(link, target string) (bool, error) {
	ok, err := hostOS.LinksTo(link, target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("native history link %s: %w", link, err)
	case !ok && !isRealDir(link):
		return false, fmt.Errorf("%w: %s", ErrHistoryNotLinked, link)
	}
	return ok, nil
}

// lockFileMode and lockDirMode are the modes this instance-home lock's
// sidecar and its parent directory are created with, before umask — not
// group- or world-WRITABLE, matching every other lock site in this project
// (see internal/core/agent/rmw_lock.go's identically-reasoned pair).
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// lockInstanceHome takes the lock for instanceHome and returns the release.
// The instance is a member of the home-rooted session dir (paths.HarpSessionEngineHomes),
// so its sidecar goes to the home locks store (paths.HomePathFor) — the
// session dir itself holds only paths.HarpMembers. A home that cannot be
// keyed to a lock location, or an acquisition failure, returns a no-op
// release rather than failing the run — see PrepareInstanceHome's doc for
// why that case has no second writer.
func lockInstanceHome(instanceHome string) func() {
	lockPath, err := paths.HomePathFor(instanceHome)
	if err != nil {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		clidiag.Warn("ctxloom", "instance home: could not prepare the instance lock directory for %s (%v); proceeding unserialized", lockPath, err)
		return func() {}
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	err = fl.Lock()
	stop()
	if err != nil {
		clidiag.Warn("ctxloom", "instance home: could not take the instance lock %s (%v); proceeding unserialized", lockPath, err)
		return func() {}
	}
	return func() { _ = fl.Unlock() }
}
