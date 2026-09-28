package isolation

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"

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
	// creds are the run's credentials; stores are its shared stores,
	// resolved on the host (stageStores).
	creds  engine.Credentials
	stores []sharedStore
}

// relocator is stage 2: it presents a layout to the engine. PURE — Preview
// runs it too — and the ONLY place a path is rewritten.
type relocator interface {
	// relocate returns the Placement and, for a relocating environment, the
	// mounts that make each presented root and store true — produced
	// together. A root it cannot present is an error naming EVERY such root,
	// beside the Placement with each of them unreachable (no Engine side) and
	// no mounts.
	relocate(l layout) (launch.Placement, []mount, error)
}

// stageLayout builds stage 1 for a prepared workspace: the session home is
// CREATED and prepared here, so stage 2 maps it with everything else.
func stageLayout(s Spec, cwd string, env map[string]string, stores []sharedStore) layout {
	l := layout{cwd: cwd, env: env, creds: s.creds, stores: stores}
	dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home)
	if !ok || !prepareSessionHome(s.eng, dir, cwd) {
		return l
	}
	placeHome(&l, s.eng, dir)
	return l
}

// previewLayout is stage 1 with no effects on disk: the live project as the
// cwd and the session home the run WOULD create.
func previewLayout(s Spec, stores []sharedStore) layout {
	l := layout{cwd: s.project, creds: s.creds, stores: stores}
	if dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home); ok {
		placeHome(&l, s.eng, dir)
	}
	return l
}

// placeHome records a present session home on the layout.
func placeHome(l *layout, eng engine.Engine, dir string) {
	l.sessionHome = dir
	if home := eng.Home(); home.Relocates() {
		v := home.Vars[0]
		l.homeVar = &v
	}
}

// sessionHomeRemedy is the fix-it on an unpreparable session home. It names
// no --degraded: the finding is non-degradable, because the only fallback
// would be the SHARED host home — the one thing the session home exists to
// keep a run off, and a thing only the binding may select.
const sessionHomeRemedy = "fix what kept the session home from being prepared (the error names it), or select the real engine home on the binding with `engine_home: host` — the unsafe selection, never a default"

// prepareSessionHome creates dir owner-only (it holds engine config) and, for an
// engine that relocates its home, has the engine prepare it. It reports
// whether the home is usable. How the run authenticates is settled before
// this runs (the Spec's Credentials).
//
// An unpreparable home is NON-DEGRADABLE: falling back to the real home
// would hand the engine what only the binding may select.
func prepareSessionHome(eng engine.Engine, dir, cwd string) bool {
	name := string(eng.Root().Name)
	if home := eng.Home(); home.Relocates() {
		if _, err := PrepareInstanceHome(InstanceHomeRequest{Engine: name, InstanceHome: dir, WorkDir: cwd}); err != nil {
			strictness.FailAlways(report.KindIsolation, sessionHomeRemedy,
				"session home for %s: %v — refusing to point %s at an unprepared %s, and refusing to substitute the SHARED host config home for the per-session one this agent asked for",
				name, err, home.Vars[0].Name, dir)
			return false
		}
	}
	if err := ensureOwnerOnlyDir(dir); err != nil {
		clidiag.Warn("ctxloom", "session home for %s: cannot create %s owner-only (%v); this run has no session home", name, dir, err)
		return false
	}
	return true
}

// placementOf is the Placement for relocated paths: the workspace's own env,
// the home var at the session home's Engine side (also recorded as the home
// binding), the mode's credential over both, then storeEnv (each shared
// store's var, as this environment presents it); and the names the engine
// must not inherit.
func placementOf(paths present.Paths, l layout, storeEnv map[string]string) launch.Placement {
	env := map[string]string{}
	maps.Copy(env, l.env)
	var home []engine.HomeBinding
	if l.homeVar != nil && paths.SessionHome.Engine != "" {
		env[l.homeVar.Name] = paths.SessionHome.Engine
		home = append(home, engine.HomeBinding{Var: l.homeVar.Name, Path: paths.SessionHome.Engine})
	}
	maps.Copy(env, l.creds.Env)
	maps.Copy(env, storeEnv)
	return launch.Placement{Paths: present.Advised(paths), Env: env, Unset: slices.Clone(l.creds.Unset), Home: home}
}

// hostRelocator presents every root in place: the engine opens the host
// path itself, so Engine equals Host and nothing is mounted. It cannot raise
// present.ErrUnreachableRoot, because it maps nothing. A shared store is
// shared in place: its var carries the exact string the launching env's
// engine resolves, so the run and the human read the same store.
type hostRelocator struct{}

func (hostRelocator) relocate(l layout) (launch.Placement, []mount, error) {
	paths := present.Paths{ProjectRoot: inPlace(l.cwd)}
	if l.sessionHome != "" {
		paths.SessionHome = inPlace(l.sessionHome)
	}
	storeEnv := map[string]string{}
	for _, st := range l.stores {
		if st.Var != "" {
			storeEnv[st.Var] = st.Value
		}
	}
	return placementOf(paths, l, storeEnv), nil, nil
}

func inPlace(dir string) present.Root { return present.Root{Host: dir, Engine: dir} }

// containerRelocator presents each root inside the container, WITH the mount
// that makes it true (relocateRoot). The project is placed where the
// runtime's mapper routes it; a relocated session home at its declared leaf
// under the fixed instance root; a non-relocating engine's session home as
// the container's $HOME (the SCRATCH ruling); each shared store at its place
// under the container's $HOME, with its var blanked so the engine looks
// there.
type containerRelocator struct {
	rt           Runtime
	instanceHome string
	home         string
}

func (r containerRelocator) relocate(l layout) (launch.Placement, []mount, error) {
	var refused error
	refuse := func(root string, err error) {
		err = fmt.Errorf("%s: %w", root, err)
		if refused != nil {
			err = fmt.Errorf("%w; %w", refused, err)
		}
		refused = err
	}
	project, err := relocateRoot(r.rt, l.cwd, "", false)
	if err != nil {
		refuse("project root", err)
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
		home, err := relocateRoot(r.rt, l.sessionHome, target, false)
		if err != nil {
			refuse("session home", err)
		}
		paths.SessionHome = home.root
		mounts = append(mounts, home.mount)
	}
	if refused != nil {
		return placementOf(paths, l, nil), nil, refused
	}
	storeEnv, storeMounts, err := r.relocateStores(l.stores)
	if err != nil {
		return placementOf(paths, l, nil), nil, err
	}
	return placementOf(paths, l, storeEnv), append(mounts, storeMounts...), nil
}

// errStoreNotADirectory: a shared store that is no directory under $HOME (an
// OS keychain) cannot be presented inside a container.
var errStoreNotADirectory = errors.New("the credential store is not a directory a container can mount")

// relocateStores mounts each shared store at its place under the
// container's $HOME — never AS $HOME, and read-only when the store is — and
// blanks its var, which points the engine at that place. A store with no
// place under $HOME (claude's login in the macOS Keychain) refuses.
func (r containerRelocator) relocateStores(stores []sharedStore) (map[string]string, []mount, error) {
	env := map[string]string{}
	var mounts []mount
	for _, st := range stores {
		if st.HomeRel == "" || st.hostDir == "" {
			return nil, nil, report.Errorf("declare `auth: token` on a container agent, or run it with `runtime: host`",
				"%w: this auth mode shares a credential store the OS keeps outside any directory (the macOS Keychain, for claude's login), so no container can reach it: %w", errStoreNotADirectory, engine.ErrNoCredential)
		}
		rel, err := relocateRoot(r.rt, st.hostDir, path.Join(r.home, st.HomeRel), st.ReadOnly)
		if err != nil {
			return nil, nil, fmt.Errorf("credential store: %w", err)
		}
		mounts = append(mounts, rel.mount)
		if st.Var != "" {
			env[st.Var] = ""
		}
	}
	return env, mounts, nil
}

// unrouted is the Placement of a layout no runtime routes: every root
// unreachable, so nothing is presented and no home var names a path.
func unrouted(l layout) launch.Placement {
	return placementOf(present.Paths{ProjectRoot: present.Root{Host: l.cwd}, SessionHome: present.Root{Host: l.sessionHome}}, l, nil)
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
// source the runtime cannot reach fails here rather than at the daemon,
// returning the root unreachable: its Host side, no Engine side, no mount.
func relocateRoot(rt Runtime, host, target string, readOnly bool) (relocated, error) {
	routed, err := rt.mapper().toContainer(host)
	if err != nil {
		return relocated{root: present.Root{Host: host}}, fmt.Errorf("%w: %s: %w", present.ErrUnreachableRoot, host, err)
	}
	if target == "" {
		target = routed
	}
	return relocated{root: present.Root{Host: host, Engine: target}, mount: rt.expose(host, target, readOnly)}, nil
}

// workspaceEnv is what a prepared workspace provisioned for the run, or nil
// for a workspace that provisioned nothing of its own.
func workspaceEnv(ws workspace) map[string]string {
	if e, ok := ws.(envWorkspace); ok {
		return e.Env()
	}
	return nil
}
