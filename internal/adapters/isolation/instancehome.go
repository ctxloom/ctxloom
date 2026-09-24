package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// InstanceHomeRequest is one preparation of a session's engine home: which
// engine, into which instance home, for which working directory.
type InstanceHomeRequest struct {
	// Engine is the REGISTERED backend name ("claude-code", ...).
	Engine string
	// InstanceHome is the config-home ROOT to prepare — the session's
	// engine-homes container (paths.HarpSessionEngineHomes). Each engine's
	// own leaf is appended under it.
	InstanceHome string
	// WorkDir is the absolute project directory the run works in, passed
	// through to the engine's generated config so a workspace-trust answer can
	// be keyed to the directory the run actually uses. Empty is tolerated (the
	// engine skips its per-project half and says so).
	WorkDir string
	// SharedLogin: the run executes on the host and authenticates from the
	// human's own login in place (engine.HomeSpec.SharedLogin), so its env
	// needs no token and the unauthenticated refusal does not apply.
	SharedLogin bool
}

// InstanceHomeReport is what one PrepareInstanceHome call decided and wrote.
type InstanceHomeReport struct {
	// Unauthenticated is the FAIL-LOUD case: the engine authenticates from
	// its env (engine.TokenAuth) and neither its token var nor any of its
	// other auth vars is set, so an engine launched at this home would start
	// logged out. It is a DECISION rather than a Go error so the caller can
	// refuse the relocation in its own words.
	Unauthenticated bool
	// Reason is the ready-to-surface message for Unauthenticated, naming the
	// fixes that work. Empty unless Unauthenticated.
	Reason string
	// Generated lists the paths the ENGINE's own instance-config writer wrote
	// (claude's .claude.json, for instance).
	Generated []string
	// Warnings carries the engine's fail-loud notices (host-file schema drift,
	// a precedence file shadowing what was written). PrepareInstanceHome
	// prints them; they are also returned so a test can assert on them.
	Warnings []string
}

// PrepareInstanceHome readies a session's engine home: it refuses a home
// the engine could not authenticate in, then asks the ENGINE to generate its
// own instance config (claude's field-scoped .claude.json). Every byte-level
// edit of a vendor's format happens inside that vendor's package.
//
// No credential is placed in the home. The engine authenticates from the
// human's own login in place (req.SharedLogin) or from its env (see
// ExportStoredTokens for why), so the only credential question here is
// whether a run that shares no login has an env that carries anything.
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
	if a, ok := f.Home.Auth.Get(); ok && !req.SharedLogin && !envAuthenticates(a) {
		rep.Unauthenticated = true
		rep.Reason = unauthenticatedReason(a)
		// Do NOT generate a config for an instance the caller is about to
		// refuse.
		return rep, nil
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
	return rep, err
}

// envAuthenticates reports whether the process env carries the engine's
// token var or any of its other auth vars. The stored token counts: it is
// exported into this env at startup (ExportStoredTokens).
func envAuthenticates(a engine.TokenAuth) bool {
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

// unauthenticatedReason names every var that would have authenticated the
// run and the fixes that work: mint and store a token, set an API var, or
// select the real home on the binding, which runs the engine against the
// user's own login with the engine's own lock and copies nothing. It names
// no --degraded: the caller's finding is non-degradable.
func unauthenticatedReason(a engine.TokenAuth) string {
	vars := append([]string{a.TokenVar}, a.EnvTriggers...)
	return fmt.Sprintf("none of %s is set to authenticate this run — run `%s` and store what it prints with `ctxloom auth set-token`, set %s, or select the real home on the binding with `engine_home: host` (the run then uses your own login in place; nothing is copied)",
		strings.Join(vars, ", "), a.MintHint, strings.Join(a.EnvTriggers, " or "))
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
