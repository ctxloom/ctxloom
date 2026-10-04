package isolation

import (
	"fmt"
	"maps"
	"path"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	sessionpaths "github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
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
	// nativeHome is where the session keeps the engine's native history
	// (launch.NativeHome), linked from the session home; "" when none.
	nativeHome string
	// env is what the workspace itself provisioned (a worktree's scratch dir
	// and git identity).
	env map[string]string
	// creds are the run's credentials; stores are its shared stores,
	// resolved on the host (stageStores).
	creds  engine.Credentials
	stores []sharedStore
	// trust is the engine's verdict on cwd's repository (repoTrust).
	trust engine.WorkspaceTrust
	// envHost is what a host engine inherits (Spec.curatedEnv); only the
	// host relocator places it.
	envHost agents.EnvHost
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
// inContainer is whether the prepared environment is a container
// (nativeHomeFor).
func stageLayout(s Spec, cwd string, env map[string]string, stores []sharedStore, inContainer bool) layout {
	l := layout{cwd: cwd, env: env, creds: s.creds, stores: stores, trust: repoTrust(s.eng, cwd), envHost: s.curatedEnv()}
	dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home)
	if !ok {
		return l
	}
	native, _ := launch.NativeHome(s.sessionDir, s.eng, s.home)
	inHome := historyInHome(inContainer)
	if inHome {
		clidiag.WarnOnce("ctxloom", "%s", historyInHomeNotice)
	}
	req := InstanceHomeRequest{InstanceHome: dir, NativeHome: native, HistoryInHome: inHome, WorkDir: cwd, Trust: l.trust, Auth: s.creds.Mode}
	if !prepareSessionHome(s.eng, req) {
		return l
	}
	placeHome(&l, s.eng, dir)
	l.nativeHome = nativeHomeFor(s, inContainer)
	return l
}

// previewLayout is stage 1 with no effects on disk: the live project as the
// cwd (Preview puts a worktree's checkout there) and the session home the run
// WOULD create.
func previewLayout(s Spec, stores []sharedStore, inContainer bool) layout {
	l := layout{cwd: s.project, creds: s.creds, stores: stores, trust: repoTrust(s.eng, s.project), envHost: s.curatedEnv()}
	if dir, ok := launch.SessionHome(s.sessionDir, s.eng, s.home); ok {
		placeHome(&l, s.eng, dir)
		l.nativeHome = nativeHomeFor(s, inContainer)
	}
	return l
}

// historyInHome reports that the run keeps the engine's history as a real
// directory in the mounted session home rather than linked into native/: a
// container run on a host whose directory links do not resolve inside a
// container (platform.DirLinker.LinksResolveInContainers: Windows' junctions
// name absolute host paths). The home starts from native/'s history
// (InstanceHomeRequest.HistoryInHome), and what the run adds moves into
// native/ when a host run next adopts it or Close deletes the home
// (sessions.KeepHomeHistory), whichever comes first.
func historyInHome(inContainer bool) bool {
	return inContainer && !hostOS.LinksResolveInContainers()
}

// nativeHomeFor is the native history dir the run mounts and links
// (launch.NativeHome), or "" for none — none when it keeps its history in
// the home (historyInHome).
func nativeHomeFor(s Spec, inContainer bool) string {
	if historyInHome(inContainer) {
		return ""
	}
	native, _ := launch.NativeHome(s.sessionDir, s.eng, s.home)
	return native
}

// placeHome records a present session home on the layout.
func placeHome(l *layout, eng engine.Engine, dir string) {
	l.sessionHome = dir
	if home := eng.Home(); home.Relocates() {
		v := home.Vars[0]
		l.homeVar = &v
	}
}

// repoTrust is eng's verdict on cwd's repository, read from the human's own
// record under the host home. An engine that declares none trusts nothing;
// a record that cannot be read is untrusted, and said so — the run goes
// ahead without the repository's surfaces rather than not at all.
func repoTrust(eng engine.Engine, cwd string) engine.WorkspaceTrust {
	t, ok := eng.Trust().Get()
	if !ok {
		return engine.TrustUntrusted
	}
	home, err := hostHomeDir()
	if err != nil {
		home = ""
	}
	v, err := t.Verdict(nil, engine.TrustQuery{HostHome: home, WorkDir: cwd})
	if err != nil {
		clidiag.Warn("ctxloom", "%s: %v — running %s as an untrusted repository (its own settings, hooks and MCP servers will not load)", eng.Root().Name, err, cwd)
		return engine.TrustUntrusted
	}
	return v
}

// historyInHomeNotice is the once-per-process announcement that a container
// run keeps its conversation history in the session home (historyInHome).
var historyInHomeNotice = fmt.Sprintf("native history: %s directory links do not resolve inside a container, so a container run keeps its conversation history in the session home, starting from the session's own; it moves back into the session's native history at the next host run or when the session closes", platform.Name)

// sessionHomeRemedy is the fix-it on an unpreparable session home. It names
// no --degraded: the finding is non-degradable, because the only fallback
// would be the SHARED host home — the one thing the session home exists to
// keep a run off, and a thing only the binding may select.
const sessionHomeRemedy = "fix what kept the session home from being prepared (the error names it), or select the real engine home on the binding with `engine_home: host` — the unsafe selection, never a default"

// prepareSessionHome creates dir owner-only (it holds engine config) and, for an
// engine that relocates its home, has the engine prepare it for the run's
// auth mode and links its history store into native (when the session keeps
// one). It reports whether the home is usable. How the run authenticates is
// settled before this runs (the Spec's Credentials).
//
// An unpreparable home is NON-DEGRADABLE: falling back to the real home
// would hand the engine what only the binding may select.
func prepareSessionHome(eng engine.Engine, req InstanceHomeRequest) bool {
	name := string(eng.Root().Name)
	dir := req.InstanceHome
	req.Engine = name
	if home := eng.Home(); home.Relocates() {
		if _, err := PrepareInstanceHome(req); err != nil {
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
	return launch.Placement{Paths: present.Advised(paths), Env: env, Unset: slices.Clone(l.creds.Unset), Home: home, Trust: l.trust}
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
	pl := placementOf(paths, l, storeEnv)
	pl.EnvHost = l.envHost
	return pl, nil, nil
}

func inPlace(dir string) present.Root { return present.Root{Host: dir, Engine: dir} }

// containerRelocator presents each root inside the container, WITH the mount
// that makes it true (relocateRoot). The project is placed where the
// runtime's mapper routes it; a relocated session home at its declared leaf
// under the fixed instance root; a non-relocating engine's session home as
// the container's $HOME (the SCRATCH ruling). A shared credential store is
// never presented: a run declaring one is refused (refuseStores).
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
	project, err := relocateRoot(r.rt, l.cwd, "")
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
		home, err := relocateRoot(r.rt, l.sessionHome, target)
		if err != nil {
			refuse("session home", err)
		}
		paths.SessionHome = home.root
		mounts = append(mounts, home.mount)
		if l.nativeHome != "" && l.homeVar != nil {
			// Beside the instance root, at the depth the home's relative link
			// climbs out of it: <root>/<leaf>/<rel> -> ../../native/<leaf>/<rel>.
			native, err := relocateRoot(r.rt, l.nativeHome, path.Join(path.Dir(r.instanceHome), sessionpaths.NativeDirName, l.homeVar.Subdir))
			if err != nil {
				refuse("native history", err)
			}
			mounts = append(mounts, native.mount)
		}
	}
	if refused != nil {
		return containerPlacement(paths, l), nil, refused
	}
	if err := refuseStores(l.stores); err != nil {
		return containerPlacement(paths, l), nil, err
	}
	return containerPlacement(paths, l), mounts, nil
}

// hostOnlyRemedy is the fix for a container run whose mode shares a store:
// only the human's own session in login mode declares one, and the token is
// what a container carries.
const hostOnlyRemedy = "set `auth: token` in your config, or run this agent with `runtime: host`"

// refuseStores refuses a container run whose credentials declare any shared
// store (engine.ErrHostOnlyStore): no container is given the human's own
// credential store, so the run would start logged out.
func refuseStores(stores []sharedStore) error {
	if len(stores) == 0 {
		return nil
	}
	return report.Errorf(hostOnlyRemedy, "%w: a container cannot be given the human's own credential store this auth mode shares: %w", engine.ErrHostOnlyStore, engine.ErrNoCredential)
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
// target "" places the root where the runtime's seam routes it; a fixed
// target (the instance home, $HOME) still has its host side routed, so a
// source the runtime cannot reach fails here rather than at the daemon,
// returning the root unreachable: its Host side, no Engine side, no mount.
func relocateRoot(rt Runtime, host, target string) (relocated, error) {
	seam := rt.paths()
	routed, err := seam.targetFor(host)
	if err != nil {
		return relocated{root: present.Root{Host: host}}, fmt.Errorf("%w: %s: %w", present.ErrUnreachableRoot, host, err)
	}
	if target == "" {
		target = routed
	}
	return relocated{root: present.Root{Host: host, Engine: target}, mount: seam.bind(host, target, false)}, nil
}

// workspaceEnv is what a prepared workspace provisioned for the run, or nil
// for a workspace that provisioned nothing of its own.
func workspaceEnv(ws workspace) map[string]string {
	if e, ok := ws.(envWorkspace); ok {
		return e.Env()
	}
	return nil
}
