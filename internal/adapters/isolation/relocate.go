package isolation

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// layout is stage 1: the run's LOCAL layout at host paths, the same on every
// environment. Nothing in it is presented; stage 2 (a relocator) decides
// where the engine sees each piece.
type layout struct {
	// cwd is the engine's working directory on the host: the live project,
	// or the checkout a worktree workspace created.
	cwd string
	// sessionHome is the session's home for the engine (launch.SessionHome),
	// "" when this run has none.
	sessionHome string
	// homeVar is the engine's declared home var when it relocates one, nil
	// for an engine that relocates nothing.
	homeVar *engine.HomeVar
	// env is what the workspace itself provisioned (a worktree's scratch dir
	// and git identity).
	env map[string]string
	// login is the shared login a run at the session home authenticates from
	// where the environment shares the human's login in place; nil otherwise.
	login map[string]string
}

// relocator is stage 2: it presents a layout to the engine. PURE — Preview
// runs it too — and the ONLY place a path is rewritten.
type relocator interface {
	// sharesLogin reports whether the engine runs where the human's own login
	// is in place (the host), which is what a shared login needs.
	sharesLogin() bool
	// relocate returns the Placement and, for a relocating environment, the
	// mounts that make each presented root true — produced together.
	relocate(l layout) (launch.Placement, []mount, error)
}

// stageLayout builds stage 1 for a prepared workspace: the session home is
// CREATED and prepared here, so stage 2 maps it with everything else.
func stageLayout(s Spec, cwd string, env map[string]string, sharesLogin bool) layout {
	l := layout{cwd: cwd, env: env}
	dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home)
	if !ok {
		return l
	}
	login := sharedLoginEnv(s.eng)
	if !prepareSessionHome(s.eng, dir, cwd, sharesLogin && login != nil) {
		return l
	}
	placeHome(&l, s.eng, dir, login, sharesLogin)
	return l
}

// previewLayout is stage 1 with no effects on disk: the live project as the
// cwd and the session home the run WOULD create.
func previewLayout(s Spec, sharesLogin bool) layout {
	l := layout{cwd: s.project}
	if dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home); ok {
		placeHome(&l, s.eng, dir, sharedLoginEnv(s.eng), sharesLogin)
	}
	return l
}

// placeHome records a present session home on the layout.
func placeHome(l *layout, eng engine.Engine, dir string, login map[string]string, sharesLogin bool) {
	l.sessionHome = dir
	if home := eng.Home(); home.Relocates() {
		v := home.Vars[0]
		l.homeVar = &v
	}
	if sharesLogin {
		l.login = login
	}
}

// sharedLoginEnv is the engine's shared login (engine.HomeSpec.SharedLogin):
// its credential-storage var set to the exact string the launching env
// resolves, and its token var blanked, because an engine may read a token
// ahead of any credential (claude does). nil for an engine that declares
// none, or relocates no home to share it from.
func sharedLoginEnv(eng engine.Engine) map[string]string {
	home := eng.Home()
	login, ok := home.SharedLogin.Get()
	if !ok || !home.Relocates() {
		return nil
	}
	env := map[string]string{login.Var: login.Value(os.LookupEnv)}
	if a, ok := home.Auth.Get(); ok {
		env[a.TokenVar] = ""
	}
	return env
}

// sessionHomeRemedy is the fix-it on an unpreparable session home. It names
// no --degraded: the finding is non-degradable, because the only fallback
// would be the SHARED host home — the one thing the session home exists to
// keep a run off, and a thing only the binding may select.
const sessionHomeRemedy = "store the engine's long-lived token with `ctxloom auth set-token` (or export one of its auth vars), or select the real engine home on the binding with `engine_home: host` — the unsafe selection, never a default"

// prepareSessionHome creates dir (0700: it holds engine config) and, for an
// engine that relocates its home, has the engine prepare it — refusing a home
// nothing authenticates. It reports whether the home is usable.
//
// An unauthenticated home is NON-DEGRADABLE: handing the engine an empty home
// it cannot authenticate against trades a working run for a mysterious 401,
// and falling back to the real home would hand it what only the binding may
// select.
func prepareSessionHome(eng engine.Engine, dir, cwd string, sharedLogin bool) bool {
	name := string(eng.Root().Name)
	if home := eng.Home(); home.Relocates() {
		rep, err := PrepareInstanceHome(InstanceHomeRequest{Engine: name, InstanceHome: dir, WorkDir: cwd, SharedLogin: sharedLogin})
		if err == nil && rep.Unauthenticated {
			err = errors.New(rep.Reason)
		}
		if err != nil {
			strictness.FailAlways(report.KindIsolation, sessionHomeRemedy,
				"session home for %s: %v — refusing to point %s at an unauthenticated %s, and refusing to substitute the SHARED host config home for the per-session one this agent asked for",
				name, err, home.Vars[0].Name, dir)
			return false
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		clidiag.Warn("ctxloom", "session home for %s: cannot create %s (%v); this run has no session home", name, dir, err)
		return false
	}
	return true
}

// placementOf is the Placement for relocated paths: the workspace's own env,
// the home var at the session home's Engine side (also recorded as the home
// binding), then extra (the shared login) over both.
func placementOf(paths present.Paths, l layout, extra map[string]string) launch.Placement {
	env := map[string]string{}
	maps.Copy(env, l.env)
	var home []engine.HomeBinding
	if l.homeVar != nil && paths.SessionHome.Engine != "" {
		env[l.homeVar.Name] = paths.SessionHome.Engine
		home = append(home, engine.HomeBinding{Var: l.homeVar.Name, Path: paths.SessionHome.Engine})
	}
	maps.Copy(env, extra)
	return launch.Placement{Paths: present.Advised(paths, nil), Env: env, Home: home}
}

// hostRelocator presents every root in place: the engine opens the host
// path itself, so Engine equals Host and nothing is mounted. It cannot raise
// present.ErrUnreachableRoot, because it maps nothing.
type hostRelocator struct{}

func (hostRelocator) sharesLogin() bool { return true }

func (hostRelocator) relocate(l layout) (launch.Placement, []mount, error) {
	paths := present.Paths{ProjectRoot: inPlace(l.cwd)}
	if l.sessionHome != "" {
		paths.SessionHome = inPlace(l.sessionHome)
	}
	return placementOf(paths, l, l.login), nil, nil
}

func inPlace(dir string) present.Root { return present.Root{Host: dir, Engine: dir} }

// containerRelocator presents each root inside the container, WITH the mount
// that makes it true (relocateRoot). The project is placed where the
// runtime's mapper routes it; a relocated session home at its declared leaf
// under the fixed instance root; a non-relocating engine's session home as
// the container's $HOME (the SCRATCH ruling).
type containerRelocator struct {
	rt           Runtime
	instanceHome string
	home         string
}

func (containerRelocator) sharesLogin() bool { return false }

func (r containerRelocator) relocate(l layout) (launch.Placement, []mount, error) {
	project, err := relocateRoot(r.rt, l.cwd, "")
	if err != nil {
		return launch.Placement{}, nil, fmt.Errorf("project root: %w", err)
	}
	paths := present.Paths{ProjectRoot: project.root}
	mounts := []mount{project.mount}
	if l.sessionHome != "" {
		target := r.home
		if l.homeVar != nil {
			// A container path, so joined with forward slashes whatever the
			// host's separator.
			target = path.Join(r.instanceHome, l.homeVar.Subdir)
		}
		home, err := relocateRoot(r.rt, l.sessionHome, target)
		if err != nil {
			return launch.Placement{}, nil, fmt.Errorf("session home: %w", err)
		}
		paths.SessionHome = home.root
		mounts = append(mounts, home.mount)
	}
	return placementOf(paths, l, nil), mounts, nil
}

// relocated is one root as the container presents it, and its mount.
type relocated struct {
	root  present.Root
	mount mount
}

// relocateRoot is the ONE way the container relocator presents a host path
// to the engine: the engine-side path and the mount that makes it true are
// produced together, so a presented root cannot exist without its mount.
// target "" places the root where the runtime's mapper routes it; a fixed
// target (the instance home, $HOME) still has its host side routed, so a
// source the runtime cannot reach fails here rather than at the daemon.
func relocateRoot(rt Runtime, host, target string) (relocated, error) {
	routed, err := rt.mapper().toContainer(host)
	if err != nil {
		return relocated{}, fmt.Errorf("%w: %s: %w", present.ErrUnreachableRoot, host, err)
	}
	if target == "" {
		target = routed
	}
	return relocated{root: present.Root{Host: host, Engine: target}, mount: rt.expose(host, target, false)}, nil
}

// workspaceEnv is what a prepared workspace provisioned for the run, or nil
// for a workspace that provisioned nothing of its own.
func workspaceEnv(ws workspace) map[string]string {
	if e, ok := ws.(envWorkspace); ok {
		return e.Env()
	}
	return nil
}
