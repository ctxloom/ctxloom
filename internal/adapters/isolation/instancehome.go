package isolation

import (
	"fmt"
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
// No credential is placed in the home: a run authenticates from what its
// agent's auth mode resolves to (engine.Auth.Credentials), settled before the
// home is prepared.
//
// The real host home is READ and never written by this call; tests/arch's
// real-home byte-identity gate is what proves it.
//
// The whole operation is serialized under the project lock keyed to
// req.InstanceHome, so two runs sharing ONE session instance (a coordinator and
// its in-tree delegated child, which inherits the harp) cannot interleave their
// load-modify-write of the same config file. An instance home outside any
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
	writer := f.Home.InstanceConfig
	if writer == nil {
		return rep, nil
	}
	hostHome, err := hostHomeDir()
	if err != nil {
		hostHome = ""
	}
	engineRep, err := writer.WriteInstanceConfig(engine.InstanceConfigRequest{
		HostHome:     hostHome,
		InstanceHome: req.InstanceHome,
		WorkDir:      req.WorkDir,
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
