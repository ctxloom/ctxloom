package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Default container-policy parameters. The image is the REQUIRED agent image: when
// it is absent and not locally buildable the policy degrades down the chain to
// None. That degrade is a fatal finding (ClassIsolation) the choke owner aborts on
// unless --degraded, since the image is only required for an EXPLICITLY-requested
// container.
// The binary/home are the in-container conventions the minimal image
// (and the future production image) must honour.
const (
	defaultContainerImage  = "ctxloom-agent:latest"
	defaultContainerBinary = "/usr/local/bin/ctxloom"
	// defaultContainerInstanceHome is the FIXED, well-known in-container root
	// every RELOCATED engine home (engine_home: session) hangs under, at the
	// leaf the engine declares (engine.HomeVar.Subdir): <root>/<leaf>. A
	// container cell runs exactly one engine on a filesystem ctxloom owns, so
	// there is nothing to negotiate about where the home lives, and nothing
	// to compute from $HOME — the mount is what matters. Overridable per
	// policy through Container.WithInstanceHome.
	defaultContainerInstanceHome = "/ctxloom/home"
	// defaultContainerHome is the ctxloom user's home baked into the agent
	// images (the entrypoint remaps that user to the launching uid/gid and
	// hands it this home). Auth credential mounts land under it.
	defaultContainerHome = "/home/ctxloom"
)

// Container is the container isolation policy: it runs each plugin (and its
// engine) inside a fresh container — a REAL boundary, so approvals are bypassed
// (the container replaces the in-engine prompt as the safety net). For the
// top-level run it bind-mounts the live project at its identical absolute path
// (cwd + .git resolve unchanged, WIP intact, edits land in the real files) with a
// fresh $HOME (engine global state isolated; the only things of the human's
// mounted into it are the credential stores the run's auth mode shares). Any inability to launch returns an error the caller catches and
// degrades down the chain to None; because a container tier is only ever built
// for an EXPLICIT request, that lost boundary is a fatal finding (ClassIsolation)
// the choke owner aborts on unless --degraded (CLAUDE.md fail-loudly).
//
// AUTH is the run's own Credentials (Spec.Credentials), resolved before the
// environment exists: the credential rides the engine's env over the wire to
// the in-container runner, never the `run` argv, and each shared store is
// mounted at its place under the container's $HOME (containerRelocator).
// The owner's run and every delegated agent, at any depth, authenticate the
// same way: there is no trust gate on this path, by ruling.
//
// CONFIG: ctxloom's managed-config writers (.claude/settings.json, commands, the
// framed context file under .ctxloom/cache) target the run's cwd, which here is
// the bind-mounted host project — so their writes are shadowed by scratch overlay
// mounts (containerConfigOverlay) to keep the HOST project clean. The single
// project-root file .mcp.json is NOT overlaid (a file bind-mount breaks the
// atomic write+rename); an MCP-configured container run still writes it into the
// host project root — a flagged residue whose fix (relocate via --mcp-config) is a
// follow-up.
type Container struct {
	runtime Runtime
	// base is the injected "where the container's cwd comes from" half: hostBase
	// mounts the LIVE project dir (the plain container), worktreeBase a fresh
	// per-agent git worktree (the former ContainerWorktree). It owns the
	// base-specific mounts (config overlay + gitdir mirror for the host; the .git
	// common-dir mirror for a worktree), the base-specific teardown (noop vs the
	// WIP-safe worktree teardown), and the policy name ("container" |
	// "container-worktree"). Default hostBase on the NewContainerFor/containerFor
	// paths; NewContainerWorktreeFor injects worktreeBase. Nil only on a bare test-built
	// Container{} — Name()/withSessionState nil-guard it.
	base       containerBase
	image      string
	engineSpec engineContainerSpec // backend-keyed knobs: auth, overlays, local-build recipe
	binaryPath string              // the container's ctxloom path (runs `runner <engine>`)
	home       string              // fresh $HOME inside the container
	// instanceHome is the fixed in-container root a RELOCATED engine home is
	// mounted under (defaultContainerInstanceHome; WithInstanceHome overrides).
	instanceHome string
	// baseContainerfile is the user-provided base Containerfile a local build
	// layers the agent stage onto (config isolation_base_containerfile;
	// "" = the embedded default base). Beats devcontainer auto-detection
	// (locked decision 8).
	baseContainerfile string
	// appRoot is the project root devcontainer auto-detection resolves
	// .devcontainer/devcontainer.json (or .devcontainer.json) against; ""
	// disables auto-detection (same effect as noDevcontainerBase — the zero
	// Container built by NewContainerFor never auto-detects).
	appRoot string
	// noDevcontainerBase opts out of devcontainer auto-detection (config
	// isolation_devcontainer_base: false).
	noDevcontainerBase bool
	// devcontainerService names the docker-compose service to use as the base
	// when the detected devcontainer.json declares dockerComposeFile (config
	// isolation_devcontainer_service).
	devcontainerService string
	// engine is THIS container's engine — the backend it was built for. It
	// keys the agent image: ONE engine per image, so the image identity is a
	// function of the engine alone, cacheable across projects and structurally
	// incapable of failing on another vendor's installer.
	engine string
	// state is the run's session identity (harp + project id), stamped by
	// Prepare (withSessionState); it scopes the read-write state mounts that
	// keep transcripts/session artifacts/task writes durable across teardown
	// (sessionStateMounts). Zero on paths without session accounting.
	state SessionState
	// git is the DI seam used to resolve the live project's git common-dir when
	// the project is itself a LINKED WORKTREE (or submodule) — see
	// gitdirMirrorMount. Nil on the normal construction paths
	// (NewContainerFor/containerFor); gitSeam defaults it to the
	// real git binary. Tests inject a git.Fake.
	git git.Git
}

// Ensure Container satisfies the policy interface.
var _ policy = Container{}

// NewContainerFor builds the container policy for a REGISTERED backend name: the
// backend's container spec picks the agent image, the auth resolver, the
// managed-config overlay set, and the build sources that let ensureImage build
// the image locally when it is absent.
//
// backend is REQUIRED and is the ENGINE name, resolved per call from whatever
// the run actually carries (a SpawnPlan's Backend, an agent binding's resolved
// engine) — never an agent label, never a fixed "". There is deliberately no
// image-only constructor: the pair it used to build (an explicit image over the
// EMPTY backend) resolves to the fail-closed default auth, so every caller of it
// aborted at PrepareWorkspace's auth gate. A caller with a resolved image of its
// own says which engine's auth it wants and overrides the image explicitly:
// NewContainerFor(rt, "mock").WithImage(img).
//
// An unmapped name still constructs — the auth gate, not this constructor, is
// where it fails (see engineContainerSpecFor's default arm) — so a degrade chain
// keeps its single failure point.
func NewContainerFor(rt Runtime, backend string) Container {
	p := engineContainerSpecFor(backend)
	return Container{
		runtime:      rt,
		base:         hostBase{},
		image:        p.image,
		engine:       backend,
		engineSpec:   p,
		binaryPath:   defaultContainerBinary,
		home:         defaultContainerHome,
		instanceHome: defaultContainerInstanceHome,
	}
}

// WithInstanceHome overrides the fixed in-container root a relocated engine
// home is mounted under (defaultContainerInstanceHome) — for an image whose
// filesystem cannot host the default, and for a test that pins its own.
func (c Container) WithInstanceHome(root string) Container {
	c.instanceHome = root
	return c
}

// containerFor builds the backend's container policy with the user's image
// configuration: an image override (config isolation_images) is run AS-IS —
// never locally built or overlaid (the user owns it) — so an absent override
// degrades with a warning instead of triggering the on-the-fly build; a base
// Containerfile (config isolation_base_containerfile), or an auto-detected
// project devcontainer, makes the on-the-fly build layer the agent stage onto
// that base instead of the embedded default. A COMPOSABLE backend's image
// resolves to the shared content-keyed composed tag (composedIdentity) —
// devcontainer detection here is BEST-EFFORT (errors only affect the TAG
// NAME, never abort construction); a real detection failure surfaces loudly
// later, when ensureImage actually needs to build.
func containerFor(rt Runtime, backend string, img ImageConfig) Container {
	c := NewContainerFor(rt, backend)
	c.baseContainerfile = img.BaseContainerfile
	c.appRoot = img.AppRoot
	c.noDevcontainerBase = img.NoDevcontainerBase
	c.devcontainerService = img.DevcontainerService
	if img.Image != "" {
		c.image = img.Image
		c.engineSpec.engineInstall = nil
		return c
	}
	devBase, _ := resolveDevBase(c.appRoot, c.noDevcontainerBase, c.devcontainerService)
	if id, ok := composedIdentity(c.engineSpec, c.baseContainerfile, devBase, c.engine); ok {
		c.image = id.ref
	}
	return c
}

// containerBuildSources resolves this container's local-build sources,
// including the auto-detected devcontainer base when enabled. A non-nil err
// means devcontainer DETECTION failed (malformed JSON, an unresolvable
// dockerComposeFile) — sources is still populated from every OTHER source
// (an explicit base Containerfile, or the embedded default), so a caller that
// downgrades the error to a fatal-unless-degraded finding and continues (see
// runEnsureImage) gets a still-usable degrade chain; Diagnose instead folds it
// into an advisory guidance line.
func (c Container) containerBuildSources(baseOverride string) (sources []buildSource, devBase *baseStage, err error) {
	devBase, err = resolveDevBase(c.appRoot, c.noDevcontainerBase, c.devcontainerService)
	sources = buildSources(c.engineSpec, buildSourcesOptions{
		baseOverride:      baseOverride,
		baseContainerfile: c.baseContainerfile,
		devBase:           devBase,
		engine:            c.engine,
	})
	return sources, devBase, err
}

// identityFor resolves this container's build identity for the given
// resolved devcontainer base: composedIdentity's engine-aware provenance and
// slot for a COMPOSABLE spec, else the legacy HostProvenanceDigest with no
// slot. The tag is always c.image, the one containerFor resolved and every
// presence check reads.
func (c Container) identityFor(devBase *baseStage) agentImageID {
	if id, ok := composedIdentity(c.engineSpec, c.baseContainerfile, devBase, c.engine); ok {
		id.ref = c.image
		return id
	}
	return agentImageID{ref: c.image, provenance: HostProvenanceDigest(c.baseContainerfile)}
}

// Name identifies the policy: the injected base names it — "container" for the
// host base (live project dir) or "container-worktree" for the worktree base. A
// nil base (a bare test-built Container{}) reports the host name.
func (c Container) Name() string {
	if c.base == nil {
		return PolicyNameContainer
	}
	return c.base.name()
}

// ResolveWorkspace materializes the container run's workspace and doubles as the
// degrade gate. It fails (→ caller falls back to None) when no runtime can
// launch, the required image is absent, OR no engine auth can be resolved
// (resolveContainerAuth). Otherwise it delegates the workspace-flavored tail to
// the injected base (host → the identical-path project dir, materialized already;
// worktree → a per-agent checkout it creates) and returns a workspace whose Dir()
// is that cwd and whose Cleanup() removes the host scratch tree then runs the
// mapping and base teardowns. agentID scopes the container name.
//
// The gate runs HERE, before the base materializes anything, deliberately: a
// missing image or unreachable runtime is decided while the only thing to unwind
// is the scratch tree, never a freshly created checkout. mount is what turns the
// resolved tree into a container mapping — NOTHING here builds a mount, so a
// caller may write into Dir() before calling mount and the run will see it.
func (c Container) resolveWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error) {
	sc, err := c.prepareContainerScratch(ctx)
	if err != nil {
		return nil, err
	}
	// The route home is settled before the base is materialized: a runner
	// that could never dial the coordinator would run and lose its work, so
	// no route is a non-degradable refusal, --degraded included.
	route, err := settleReach(ctx, c.runtime)
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	// sc.root is a real on-disk scratch tree that exists from here on but has
	// no owning workspace yet. The explicit error path below already removes it
	// on a normal resolveBase failure; this guards the case a normal error
	// return can't: a PANIC inside resolveBase (worktree.go's own ResolveWorkspace
	// carries the identical guard for ITS resources) would otherwise skip that
	// removal entirely and leak sc.root under the OS temp dir. mount needs no
	// such guard: by then the returned workspace OWNS the scratch, so a panic
	// leaves the caller something that can still Cleanup().
	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(sc.root)
			panic(r)
		}
	}()
	// The base materializes the cwd the container will mount: the host base
	// hands back the LIVE project dir untouched; the worktree base creates the
	// per-agent checkout. A base that created a resource unwinds it WIP-safely
	// before returning the error; the scratch removal is ours regardless, so a
	// degrade never leaks a temp dir.
	dir, baseCleanup, err := c.base.resolveBase(ctx, projectDir, agentID)
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	return &containerWorkspace{
		dir:         dir,
		projectDir:  projectDir,
		scratchRoot: sc.root,
		stateMounts: sc.stateMounts,
		scratchEnv:  sc.runEnv(),
		agentID:     agentID,
		baseCleanup: baseCleanup,
		reach:       route,
	}, nil
}

// settleReach is the workspace gate's route check: rt's route home, or a
// non-degradable ClassIsolation finding and the refusal.
func settleReach(ctx context.Context, rt Runtime) (hostRoute, error) {
	route, err := rt.reachRoute(ctx)
	if err != nil {
		strictness.FailAlways(report.KindIsolation, noHostReachRemedy, "refusing to run a container that cannot dial home: %v", err)
		return hostRoute{}, err
	}
	return route, nil
}

// noHostReachRemedy names the ways a container gets a route to the host.
const noHostReachRemedy = "give the host a default route, or use a runtime whose containers reach the host privately: a rootless translator with a loopback route (pasta, slirp4netns) or a rootful bridge"

// remintReach re-mints the runner's reach-back for its container: the
// coordinator's host-side URL on spawnEnv becomes the URL the runtime's route
// dials, as a mounted path's host side becomes its container side. An env
// without a reach-back (a launch that carries none) passes through.
func remintReach(cw *containerWorkspace, spawnEnv map[string]string) (map[string]string, error) {
	hostURL, ok := spawnEnv[sessions.EnvCoordURL]
	if !ok || cw.reach.dial == "" {
		return spawnEnv, nil
	}
	r, err := present.ReachOnHost(hostURL).Via(cw.reach.dial)
	if err != nil {
		return nil, err
	}
	out := maps.Clone(spawnEnv)
	out[sessions.EnvCoordURL] = r.Engine
	return out, nil
}

// mount maps an already-materialized container workspace into the container: it
// assembles the run's bind mounts and per-run env, proves the mounts can resolve
// through the daemon, and stamps the resulting plan onto the workspace so the
// spawn (startRunner / interactiveRunner) renders it.
//
// It changes no workspace CONTENT — whatever the tree held on entry is what the
// container sees. The one thing it does write inside the tree is the managed-
// config overlay MOUNTPOINTS (empty directories a bind mount requires to exist;
// see containerConfigOverlay for why they must be pre-created as the invoking
// user), which it keeps: see containerConfigOverlay.
func (c Container) bind(ctx context.Context, ws workspace) (mountPlan, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return mountPlan{}, fmt.Errorf("container mount: unexpected workspace %T (expected a container workspace)", ws)
	}
	// The base's own mounts: the host base shadows the LIVE project's
	// managed-config dirs (overlays) and mirrors a pointer-file .git; the
	// worktree base mirrors its checkout's .git common-dir.
	baseMounts, err := c.base.mountBase(ctx, c.runtime, cw.projectDir, cw.dir, cw.scratchRoot, c.engineSpec, c.gitSeam())
	if err != nil {
		return mountPlan{}, err
	}
	// Order is inert (SD4): every mount targets a distinct in-container path and
	// renders as an independent --mount. Scoped state rides every axis; the
	// base mounts (overlays/gitdir mirror, or the worktree .git mirror) layer on.
	mounts := append(append([]mount(nil), cw.stateMounts...), baseMounts...)
	// The shared-filesystem probe runs HERE, once every real mount root is
	// known (mountProbeRoots): cw.dir (the project dir, or the worktree
	// checkout resolveBase created), cw.scratchRoot (the config overlays), and
	// every mount's own host path (the session-state mounts, a linked
	// worktree's gitdir mirror). Probing a synthetic tempdir instead would only
	// prove THAT directory's sharing status — a standing false positive on a
	// partially-shared Docker Desktop file-sharing list, exactly the platform
	// this probe exists to protect. A mismatch on ANY root means that
	// identical-path mount would resolve against a DIFFERENT filesystem and the
	// handshake would hang — erroring here turns that hang into the caller's
	// clean per-axis degrade. The mapping leaves nothing to undo on failure; the
	// workspace owns the scratch and the base resource and tears them down
	// through Cleanup().
	roots := mountProbeRoots(cw.dir, cw.scratchRoot, mounts)
	if perr := sharedFSCheck(ctx, c.runtime, c.image, roots); perr != nil {
		return mountPlan{}, sharedFSGateError(c.runtime, perr)
	}
	// Scope the container run's git identity to this agent, the SAME way the
	// host+worktree path does (worktreeWorkspace.Env → gitIdentity). The .git
	// common dir is bind-mounted READ-WRITE and SHARED (gitCommonDirMount), so a
	// containerized commit resolves the shared .git/config exactly like a host
	// linked worktree does — a scoped GIT_AUTHOR_*/GIT_COMMITTER_* pair (which
	// outranks repo-local config) is what stops one agent's commit from
	// misattributing across that shared config. Deliberately NO TMPDIR/GOTMPDIR
	// here (unlike the host Env(), which needs them): each container has its OWN
	// mount namespace, so /tmp is already private per-container — there is no
	// cross-agent tmpfs collision to scope away. Only the shared, RW-mounted .git
	// forces an isolation lever, so ONLY the git-identity vars are injected. (Same
	// shape of reasoning as the host fix's deliberate GOCACHE omission.)
	plan := mountPlan{
		Mounts: mounts,
		Env:    append(append([]string(nil), cw.scratchEnv...), gitIdentityEnv(cw.agentID)...),
	}
	cw.extraMounts = plan.Mounts
	cw.extraEnv = plan.Env
	return plan, nil
}

// PrepareWorkspace resolves and maps in one step (see prepareWorkspace).
func (c Container) prepareWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error) {
	return resolveAndBind(ctx, c, projectDir, agentID)
}

// sharedFSGateError renders the shared-filesystem probe's outcome as the
// PrepareWorkspace gate's error, distinguishing a DEFINITIVE negative verdict
// (errors.As a *sharedFSMismatch — the probe ran and proved a mount root is
// not shared) from a TRANSIENT run failure (daemon down/cold, image
// unreadable, our own timeout, a cancelled ctx) exactly the way diagnoseProbe
// (diagnose.go) already does — before this fix the headline unconditionally
// read "does not share this process's filesystem" even for a run that never
// produced a sharing verdict at all, which pointed a transient daemon hiccup
// at the wrong fix-it (a real sharing gap) instead of "the daemon didn't
// answer, try again". Behavior was already correct (either way the gate
// fails and the caller degrades); this only aligns the MESSAGE with the
// actual cause.
func sharedFSGateError(rt Runtime, perr error) error {
	var mism *sharedFSMismatch
	if errors.As(perr, &mism) {
		hint := "bind mounts of this process's paths cannot resolve through the daemon"
		if InContainer() {
			hint += "; this looks like a dev container using the host's daemon (docker-outside-of-docker) — enable the docker-in-docker feature, or drop `runtime: container`"
		}
		return fmt.Errorf("container runtime %s does not share this process's filesystem (%s): %w", runtimeName(rt), hint, perr)
	}
	return fmt.Errorf("shared-filesystem probe for container runtime %s could not run: %w", runtimeName(rt), perr)
}

// WithSessionState stamps the run's session identity (harp + project id) onto
// this Container, matching Prepare's withSessionState for the Container case
// (isolation.go). Exposed for a caller that constructs a Container directly
// via NewContainerFor rather than going through the Resolve/Prepare axes
// chain. Such a caller must never silently degrade a requested container to
// the host: on that path PrepareWorkspace's error is the ONLY outcome of a
// failed gate, unlike chainFor's degrade-to-None-unless-strict chain.
func (c Container) WithSessionState(state SessionState) Container {
	c.state = state
	if c.base != nil {
		c.base = c.base.withState(state)
	}
	return c
}

// WithImage overrides this Container's resolved image, keeping the spec's
// auth/overlay/transcript knobs (whichever engine's auth the caller can
// actually resolve) but running a DIFFERENT image — e.g. a docker-gated
// test's minimal harness image, which has nothing to do with the spec's
// own engine but needs SOME resolvable auth to clear PrepareWorkspace's gate.
// Exposed as a narrow, explicit override (not a general spec mutator) so
// production callers (NewContainerFor/containerFor) are unaffected: only a
// caller that deliberately wants this specific mismatch reaches for it.
//
// The image is USER-OWNED, so the spec's local-build recipe is dropped with
// it — the same pairing containerFor makes for an isolation_images override, and
// for the same two reasons: ctxloom must not rebuild a tag the caller supplied,
// and runAsIs() must report true so checkRunAsIsIdentity's pre-start contract
// check actually runs on it. A wrong-identity container LAUNCHES cleanly and
// then root-owns every file it writes into the mounted project, so that check is
// the only signal there is.
func (c Container) WithImage(image string) Container {
	c.image = image
	c.engineSpec.engineInstall = nil
	return c
}

// gitSeam returns the container's git DI seam, defaulting to the real git binary
// when unset (the normal construction paths leave it nil). Tests inject a
// git.Fake to drive the host base's gitdir mirror without a real linked worktree.
func (c Container) gitSeam() git.Git {
	if c.git == nil {
		return git.NewExec()
	}
	return c.git
}

// containerBase is the injectable base half the Container policy composes: WHERE
// the container's cwd comes from and the mounts/teardown/name that follow from
// it. Two implementations collapse what used to be two policy types: hostBase
// (the plain Container over the LIVE project dir) and worktreeBase (the former
// ContainerWorktree, a per-agent git worktree). The shared front-half
// (prepareContainerScratch — runtime/image/auth gate + host scratch) stays on
// Container; the base only supplies the workspace-flavored tail.
type containerBase interface {
	// resolveBase MATERIALIZES the cwd the container will mount and returns a
	// cleanup that tears down whatever it created (nil when it created nothing).
	// Filesystem only: it builds no mounts and touches no runtime, so a caller
	// may write into the returned dir before mountBase runs. A failure returns
	// the error so the caller removes the shared scratch and the chain degrades;
	// a base that got partway unwinds its own resource WIP-safely first.
	resolveBase(ctx context.Context, projectDir, agentID string) (dir string, cleanup func() error, err error)
	// mountBase builds the base-specific mounts for an ALREADY-MATERIALIZED dir.
	// rt/g are the runtime + git seams, scratchRoot the
	// already-created host scratch (the caller removes it), spec the
	// managed-config overlay set. It must not change the dir's CONTENT.
	// projectDir is the user's LIVE project; dir is the already-materialized
	// cwd. They are the SAME path for the host base and DIFFERENT for the
	// worktree base, whose cwd is an ephemeral checkout — a base that must
	// reach project-level state (the .ctxloom config tree) has to read the
	// former and mount into the latter.
	mountBase(ctx context.Context, rt Runtime, projectDir, dir, scratchRoot string, spec engineContainerSpec, g git.Git) (mounts []mount, err error)
	// withState stamps the run's session identity onto the base — worktreeBase
	// stamps its Worktree's ephemeral-scratch home; hostBase is a no-op. Returns
	// the stamped base (bases are value types).
	withState(state SessionState) containerBase
	// name identifies the composed policy: "container" | "container-worktree".
	name() string
}

// hostBase is the plain-Container base: the container's cwd IS the LIVE project
// dir, bind-mounted identical-path. Its mounts are the managed-config scratch
// overlays (keeping the host project clean while the engine writes to scratch)
// plus, when the live project is itself a linked worktree/submodule, the .git
// common-dir mirror. It creates no host-side resource of its own, so its cleanup
// is a noop (Container removes the shared scratch).
type hostBase struct{}

// name identifies the plain container policy.
func (hostBase) name() string { return PolicyNameContainer }

// withState is a no-op: the host base persists no per-session scratch of its own
// (the container's durable state rides Container.sessionStateMounts).
func (hostBase) withState(SessionState) containerBase { return hostBase{} }

// resolveBase materializes nothing: the plain container's cwd IS the user's LIVE
// project dir, which already exists and is never torn down. Hence the nil
// cleanup — there is no resource of this base's making to release.
func (hostBase) resolveBase(_ context.Context, projectDir, _ string) (string, func() error, error) {
	return projectDir, nil, nil
}

// mountBase maps the LIVE project dir: the managed-config overlays shadow the
// engine's config writers off the host project, and a pointer-file .git gets its
// common dir mirrored so in-container git resolves. dir is the already-resolved
// cwd (== the project dir for this base). Failure returns the error (the caller
// tears the workspace down); nothing but overlay mountpoints is created here,
// and those are kept (see containerConfigOverlay).
// The live project and the resolved cwd are the SAME dir for this base (its
// resolveBase hands the project dir straight back), so it works from the
// resolved one and ignores the duplicate.
func (hostBase) mountBase(ctx context.Context, rt Runtime, _, projectDir, scratchRoot string, spec engineContainerSpec, g git.Git) ([]mount, error) {
	overlays, err := containerConfigOverlay(rt, projectDir, scratchRoot, spec.overlayDirs)
	if err != nil {
		return nil, err
	}
	// When the LIVE project is itself a linked worktree (or a submodule) its .git
	// is a POINTER FILE whose common dir lives OUTSIDE projectDir — and so is not
	// covered by the identical-path project mount. Mirror that common dir so
	// in-container git resolves the repo, exactly as the worktree base does. A
	// resolution failure fails this workspace so the chain degrades
	// (fatal-unless-degraded), never a silent broken-git launch.
	if gitMount, ok, gerr := gitdirMirrorMount(ctx, rt, g, projectDir); gerr != nil {
		return nil, gerr
	} else if ok {
		overlays = append(overlays, gitMount)
	}
	return overlays, nil
}

// gitdirMirrorMount returns the git common-dir mirror mount the plain container
// needs when the LIVE PROJECT is itself a linked worktree (or a submodule) — i.e.
// projectDir/.git is a POINTER FILE, not a directory — whose common git dir lives
// OUTSIDE projectDir and so is NOT covered by the identical-path project mount,
// leaving in-container `git` unable to resolve the repo. ok=false (no extra mount)
// when .git is a directory or absent: the common dir is inside the project mount
// already (a normal main-repo checkout), or there is no repo to mirror. It reuses
// the same identical-path mirror the worktree base builds (gitCommonDirMount).
func gitdirMirrorMount(ctx context.Context, rt Runtime, g git.Git, projectDir string) (mount, bool, error) {
	gitPath := filepath.Join(projectDir, ".git")
	info, err := os.Stat(gitPath)
	// ABSENT is a real "no mirror needed" (no repo to mirror). An unreadable .git
	// is not: it means we could not tell a pointer file from a directory, and
	// answering "no mirror needed" launches a container whose git cannot resolve
	// the repo. Fail the workspace so the chain degrades loudly instead.
	if errors.Is(err, os.ErrNotExist) {
		return mount{}, false, nil
	}
	if err != nil {
		return mount{}, false, fmt.Errorf("stat %s to decide the container gitdir mirror: %w", gitPath, err)
	}
	if info.IsDir() {
		return mount{}, false, nil
	}
	m, err := gitCommonDirMount(ctx, rt, g, projectDir)
	if err != nil {
		return mount{}, false, err
	}
	return m, true, nil
}

// containerScratch is the host-side scratch every container run needs regardless
// of its Workspace: the temp root removed on Cleanup and the host terminal
// description forwarded into the run.
type containerScratch struct {
	root    string
	termEnv []string
	// stateMounts are the scoped RW session-state mounts (transcript store,
	// session persist dir, shared task log — see sessionStateMounts) every
	// container run threads into its spec regardless of workspace axis.
	stateMounts []mount
}

// runEnv composes the per-run env threaded into the container spec: the host
// terminal description (TERM/COLORTERM as KEY=VAL, non-secret), which the
// curated handshake env deliberately drops. Returns a fresh slice so callers
// never alias the scratch's fields. A credential never rides here: it reaches
// the engine through the launch's own env, over the wire.
func (sc containerScratch) runEnv() []string {
	return append([]string(nil), sc.termEnv...)
}

// gitIdentityEnv renders the container run's per-agent git identity as the four
// GIT_AUTHOR_*/GIT_COMMITTER_* KEY=VAL entries a docker/podman `-e` loop
// consumes — the container-env counterpart to the host path's
// worktreeWorkspace.Env (both derive name/email from the SAME gitIdentity
// helper, so the identity format lives in exactly one place). It returns nil for
// an empty agentID (no backend/agent context — nothing to scope), so the run
// falls back to whatever git identity the container's own config resolves.
func gitIdentityEnv(agentID string) []string {
	name, email := gitIdentity(agentID)
	if name == "" {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

// prepareContainerScratch runs the container degrade gate — a launchable
// runtime, the required image present (or locally buildable, see
// ensureImage), and an engine that declared a container story — then provisions the host
// scratch root under the session's ephemeral dir. Any gate failure returns an error so the
// caller degrades (the top-level run → None; a fan-out member → a bare worktree).
// It is the shared front-half of BOTH the top-level Container workspace and the
// worktree-in-container composition; each layers its own extra mounts (config
// overlay / .git gitdir mirror) on top.
func (c Container) prepareContainerScratch(ctx context.Context) (containerScratch, error) {
	if c.runtime == nil || !c.runtime.Available() {
		return containerScratch{}, fmt.Errorf("container runtime %q cannot launch", runtimeName(c.runtime))
	}
	if err := c.ensureImage(ctx); err != nil {
		return containerScratch{}, err
	}
	// The image is present — but a USER-OWNED run-as-is image must also satisfy
	// the identity contract BEFORE anything starts: a wrong-identity container
	// LAUNCHES fine (invisible to the fatal launch gate) and then root-owns
	// every file it writes into the bind-mounted project.
	c.checkRunAsIsIdentity(ctx)
	// The shared-filesystem probe used to run HERE, against a single throwaway
	// tempdir under os.TempDir() — which only ever proved THAT directory's own
	// sharing status, never the REAL roots this run bind-mounts (a partially
	// shared Docker Desktop file-sharing list shares /tmp by default but not,
	// say, the project's own path, so the old probe was a standing false
	// positive on exactly the platform it exists to protect). It now runs in
	// PrepareWorkspace, AFTER the base (project dir / worktree checkout,
	// config overlays, gitdir mirror) is prepared, so it can probe the ACTUAL
	// mount set (see mountProbeRoots) instead.
	//
	// The host-side scratch root is the tree Cleanup removes. It lives under the
	// session's ephemeral dir so an owner that dies before Cleanup leaves it
	// where the session layout accounts for it, never in the OS temp dir. A run
	// with no usable harp has nowhere to put it and is refused: the error
	// becomes the caller's fatal ClassIsolation finding, like an unpreparable
	// state dir below.
	base, err := c.state.ephemeralDir()
	if err != nil {
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
	}
	root, err := os.MkdirTemp(base, "ctxloom-iso-")
	if err != nil {
		// root is normally "" here (MkdirTemp itself failed) — defensive
		// against a mutant flipping this check and discarding a dir MkdirTemp
		// actually created.
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
	}
	if !c.engineSpec.declared {
		_ = os.RemoveAll(root)
		return containerScratch{}, report.Error{Msg: "container: " + noContainerHint, Fix: noContainerRemedy}
	}
	// Session-state persistence is part of the container gate: a run whose
	// state dirs cannot be prepared errors here so the caller's degrade chain
	// raises the fatal-unless-degraded ClassIsolation finding, exactly like an
	// absent image — never a silent state-losing launch.
	stateMounts, err := c.sessionStateMounts()
	if err != nil {
		_ = os.RemoveAll(root)
		return containerScratch{}, err
	}
	return containerScratch{root: root, termEnv: hostTerminalEnv(os.Getenv), stateMounts: stateMounts}, nil
}

// hostTerminalEnv forwards the host's terminal description into the container
// (a docker/podman -e overrides the image ENV): the curated run env
// deliberately drops the host environment, which would
// leave the engine's TERM at the image default — or `dumb` — and strip
// color/cursor control from every CLI it spawns. TERM/COLORTERM carry no
// secrets and describe the terminal the user is actually watching, so they
// cross verbatim; an unset var is omitted and the image default applies.
func hostTerminalEnv(getenv func(string) string) []string {
	var out []string
	for _, key := range presentEnvKeys(getenv, []string{"TERM", "COLORTERM"}) {
		out = append(out, key+"="+getenv(key))
	}
	return out
}

// containerConfigOverlay builds one bind mount per managed-config directory
// (the spec's overlayDirs — project-relative DIRECTORIES the engine's
// managed-config writers target under the run's cwd), backed by a scratch dir
// under scratchRoot SEEDED from the project's existing content, whose container
// target shadows the same path inside the bind-mounted project. For a container
// top-level run the project is bind-mounted rw at its identical path, so these
// writes would otherwise land in the HOST project; the overlay keeps it clean
// (writes go to scratch) while the seed keeps the engine's view complete
// (user-authored commands/settings are visible, not hidden by an empty shadow).
// Directories only — a single-file overlay would break the atomic write+rename
// the writers use, which is why the project-root file .mcp.json is deliberately
// NOT overlaid (flagged residue, see the Container doc).
func containerConfigOverlay(rt Runtime, projectDir, scratchRoot string, overlayDirs []string) ([]mount, error) {
	mounts := make([]mount, 0, len(overlayDirs))
	for i, rel := range overlayDirs {
		host := filepath.Join(scratchRoot, fmt.Sprintf("cfg%d", i))
		if err := os.MkdirAll(host, 0o755); err != nil {
			return nil, fmt.Errorf("container config overlay scratch: %w", err)
		}
		target := filepath.Join(projectDir, rel)
		seedOverlay(target, host)
		// Pre-create the overlay TARGET (as the invoking user — this process runs
		// as it) BEFORE docker sees the mount. The target is nested inside the
		// identical-path project bind (mounted rw at the SAME host path), so a
		// still-missing target would make a rootful docker daemon create the bind
		// mountpoint AS ROOT — and that root-owned dir lands in the real HOST
		// project through the identical-path bind, EACCES-ing every later host
		// run's managed-config writers. Creating it ourselves makes docker find it
		// existing. Idempotent (a no-op — never a chmod — when it already exists).
		//
		// The target is KEPT after the run, never pruned at teardown: the project
		// is shared, so a concurrent run on it mounts the same target, and
		// removing it while empty would detach that run's overlay. An empty
		// .claude/ or .ctxloom/cache/ in a container-only project is the accepted
		// cost.
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, fmt.Errorf("container config overlay target: %w", err)
		}
		mounts = append(mounts, rt.expose(host, target, false))
	}
	return mounts, nil
}

// gitCommonDirMount builds the identical-path .git mirror mount from a checkout's
// git common-dir, so a `gitdir:` POINTER FILE (a linked worktree or submodule,
// whose common dir lives OUTSIDE the mounted checkout) resolves inside the
// container. Read-write by design: the per-checkout admin files (index, HEAD)
// under <common>/worktrees/<name> are written there, exactly as a host-native
// checkout writes to the shared .git. Shared by the worktree base (whose
// worktree .git is ALWAYS a pointer file) and the host base (only when the
// live project is itself a linked worktree — see gitdirMirrorMount).
//
// Over-mount blast radius (live-gag, ACCEPTED): the key property that makes
// this mount SUFFICIENT is that the per-worktree admin dir
// <common>/worktrees/<name> — the ONE thing a linked checkout actually needs
// — is a SUBDIRECTORY of <common>. Mounting the whole common dir identical-
// path is therefore the simplest mount that covers it, but it ALSO exposes
// every OTHER worktree's admin dir (and the main checkout's own index/refs)
// to this container — real blast radius, not merely "no new" one. A surgical
// mount (worktree dir + just its own <common>/worktrees/<name> + read-only
// objects/refs) is possible in principle, but git needs write access to
// refs/logs and the packed-refs/objects layout in ways that make a partial
// mount fragile and easy to get subtly wrong. DECISION: keep the whole-
// common-dir mount (correct, simple, RW-justified above) and accept the
// wider exposure; revisit only if per-agent git isolation becomes a
// requirement (already flagged a "later concern" — container_worktree.go's
// worktreeBase doc). Every ctxloom-managed worktree is out-of-repo by the
// standing layout (~/workspace/worktrees/<proj>--<branch>), so the worktree
// base relies on this mount set in production already; the host base needs
// it only when the user's OWN project dir happens to be a linked worktree —
// proven end-to-end (real git, real container, payload-asserted) by
// container_hostworktree_integration_test.go, alongside
// container_worktree_integration_test.go's worktree-base proof.
func gitCommonDirMount(ctx context.Context, rt Runtime, g git.Git, dir string) (mount, error) {
	common, err := g.CommonDir(ctx, dir)
	if err != nil {
		return mount{}, fmt.Errorf("resolve git common dir for container gitdir mount: %w", err)
	}
	// exposeMapped (not expose(common, common, ...)) routes through the
	// runtime's pathMapper — the SAME translation the project root gets.
	m, err := rt.exposeMapped(common, false)
	if err != nil {
		return mount{}, fmt.Errorf("git common dir %s has no route into the container: %w", common, err)
	}
	return m, nil
}

// seedOverlay copies the project's managed-config directory into its fresh
// scratch overlay, so the container starts from the project's existing config
// instead of an empty shadow. The in-container managed writers then reconcile
// their own managed subset on top exactly as they do on the host (settings
// merge, manifest-tracked commands, context append) — user-authored content
// survives, and every write still lands in scratch, never the host. Best-effort
// per CLAUDE.md fault tolerance: an absent source is the fresh-project case,
// and a copy failure degrades to a partial (or empty) overlay with a warning —
// the run is never blocked.
func seedOverlay(src, dst string) {
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return
	}
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		clidiag.Warn("ctxloom", "container overlay seed from %s incomplete: %v", src, err)
	}
}

// imagePresent reports whether the required image exists locally (no implicit
// pull — a missing image degrades to None). Best-effort with a short timeout.
func (c Container) imagePresent(ctx context.Context) bool {
	if c.runtime.Binary() == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, c.runtime.Binary(), c.runtime.imageInspectArgs("", c.image)...).Run() == nil
}

// overrideIdentityRemedy names the ways out when a user-supplied image cannot
// satisfy the identity contract: make the image entrypoint-governed, or accept
// the image's own identity via degraded mode.
// It names no flag on purpose: this finding is non-degradable (see
// checkRunAsIsIdentity), so offering --degraded would hand the user a remedy
// that does not work. Both routes here are followable and both end with a run
// that owns its files correctly.
const overrideIdentityRemedy = "base the isolation_images override on a ctxloom-built agent image (or install ctxloom-entrypoint as its ENTRYPOINT — see `ctxloom container build`), or drop the isolation_images override so ctxloom builds the agent image itself"

// runAsIs reports whether this policy runs a USER-OWNED image as-is (an
// isolation_images override, or an explicit image on a spec with no local
// recipe): no build source exists, so nothing ctxloom authored — the identity
// entrypoint included — is guaranteed to be in the image.
func (c Container) runAsIs() bool {
	sources, _, _ := c.containerBuildSources("")
	return len(sources) == 0
}

// entrypointGoverned reports whether the image's PID-1 entrypoint is the
// ctxloom identity-remap script — the contract that makes the PUID/PGID env
// the runtime passes actually change who the engine runs as.
func entrypointGoverned(entrypoint []string) bool {
	for _, e := range entrypoint {
		if strings.Contains(e, "ctxloom-entrypoint") {
			return true
		}
	}
	return false
}

// rootishUser reports whether an image-config USER resolves to root (empty is
// the container default: root).
func rootishUser(user string) bool {
	u, _, _ := strings.Cut(user, ":")
	return u == "" || u == "0" || u == "root"
}

// runAsIsIdentityProblem describes why a user-owned image would START with the
// WRONG identity under the given runtime ("" = the contract holds). A prior
// change replaced rootful docker's blanket `--user uid:gid` (which owned ALL images)
// with the PUID env + baked-entrypoint remap; that governs only images that
// RUN the entrypoint, so a run-as-is image needs this static contract check —
// the one pre-start signal, since a wrong-identity container launches cleanly.
func runAsIsIdentityProblem(rt Runtime, id imageIdentity) string {
	if !rt.passesPUID() {
		// A mode that passes no PUID keeps the run container-ROOT, the one uid
		// that maps to the launching host user, so the image must run as root.
		if rootishUser(id.User) {
			return ""
		}
		return fmt.Sprintf("its USER %q maps to a subordinate uid under %s (only container-root maps to the launching user)", id.User, rt.Name())
	}
	// Every PUID-passing mode relies on the baked ctxloom entrypoint, started
	// as root, to remap and drop.
	if !entrypointGoverned(id.Entrypoint) {
		return "it does not run the ctxloom identity-remap entrypoint, so the PUID/PGID remap is inert and the engine runs as the image's own user"
	}
	if !rootishUser(id.User) {
		return fmt.Sprintf("its USER %q prevents the identity remap (the ctxloom entrypoint must start as root to remap and drop)", id.User)
	}
	return ""
}

// checkRunAsIsIdentity gates a run-as-is (user-owned) image on the identity
// contract. A violation — or an unverifiable config — routes a NON-DEGRADABLE
// ClassIsolation finding, so the choke owner aborts BEFORE the runner starts in both
// modes. Locally-built images bake the entrypoint, so the contract holds by
// construction and no inspect runs.
//
// Non-degradable because the harm is done BY launching and cannot be undone
// afterwards: the run writes root-owned (or otherwise foreign-owned) files into
// the user's own project tree, and no later flag un-owns them. That is damage,
// not a thinner run, so --degraded does not reach it. An UNVERIFIABLE image is
// refused on the same terms as a known-bad one: "I could not check" is not
// evidence of safety, and the whole point of a user-supplied image is that
// ctxloom did not build it.
func (c Container) checkRunAsIsIdentity(ctx context.Context) {
	if !c.runAsIs() {
		return
	}
	id, err := c.imageIdentityConfig(ctx)
	if err != nil {
		strictness.FailAlways(report.KindIsolation, overrideIdentityRemedy,
			"refusing to run user-supplied container image %q: its identity contract cannot be verified (%v), so it may start with the wrong identity and write wrongly-owned files into your project", c.image, err)
		return
	}
	if problem := runAsIsIdentityProblem(c.runtime, id); problem != "" {
		strictness.FailAlways(report.KindIsolation, overrideIdentityRemedy,
			"refusing to run user-supplied container image %q: it would start with the WRONG identity on %s: %s — files it writes into the mounted project would not be owned by you (e.g. root-owned)", c.image, runtimeName(c.runtime), problem)
	}
}

// containerWorkspace is the container policy's workspace, unified across both
// bases: Dir() is the cwd the container mounts identical-path — the LIVE project
// dir (host base) or the per-agent worktree checkout (worktree base) — and
// Cleanup() removes the host-side scratch tree (config overlays)
// then runs the base's own teardown (a noop for the host base; the WIP-safe,
// nested-aware worktree teardown for the worktree base). The container is killed
// via the client BEFORE Cleanup. extraEnv/extraMounts carry the resolved auth env
// + credential/overlay/gitdir mounts threaded into the run spec at StartRunner.
type containerWorkspace struct {
	dir string // identical-path cwd (project dir or worktree checkout)
	// projectDir is the user's LIVE project — the dir PrepareWorkspace was
	// called with, retained because dir is NOT it for every base: the worktree
	// base's cwd is an ephemeral checkout elsewhere. mount needs the live
	// project to deliver project-level state (the .ctxloom config tree) that
	// exists only there, so it cannot be recovered from dir afterwards.
	// Identical to dir for the host base.
	projectDir  string
	scratchRoot string // host scratch tree removed by Cleanup
	// stateMounts/scratchEnv are the scratch's contributions to the
	// mapping, resolved when the workspace was resolved and consumed by mount.
	// They are held apart from extraEnv/extraMounts because those two are the
	// MAPPING's output, not its input: they are empty until mount runs.
	stateMounts []mount  // scoped RW session-state mounts
	scratchEnv  []string // host terminal description
	extraEnv    []string // mount's env plan (scratch env + scoped git identity)
	extraMounts []mount  // mount's mount plan (state + base mounts)
	agentID     string
	// reach is the runtime's route home for this workspace's runner,
	// settled at resolveWorkspace (remintReach, the environment's Listen).
	reach hostRoute
	// baseCleanup tears down the base's own resource AFTER the scratch is
	// removed: a noop for the host base (the live project dir is never torn
	// down), the worktree's WIP-safe teardown for the worktree base. Nil only on
	// a bare test-built workspace — Cleanup nil-guards it.
	baseCleanup func() error
	// workDir and roots are the container relocator's outcome for this
	// workspace (Container.environment): the project root's Engine side, and
	// the mounts produced WITH every presented root (relocateRoot). The
	// runner spec renders exactly these, so a presented path and its mount
	// have one producer.
	workDir string
	roots   []mount
}

// Dir returns the identical-path cwd (the container mounts it there so cwd + .git
// resolve unchanged; the caller threads it into RunOptions.WorkDir).
func (w *containerWorkspace) Dir() string { return w.dir }

// Cleanup removes the host scratch tree, then runs the base teardown. Idempotent —
// safe to call once after the run's client is killed (a second call skips the
// already-removed scratch, and the worktree base teardown guards its own cleared
// dir). A scratch-removal failure is surfaced by warnCleanupResidue AND returned
// (SD3: the plain-Container always-warn+return semantic now covers both bases;
// callers discard the returned error by contract). The base teardown's own error
// joins it: both bases are WIP-safe/noop and report none today, but that is a
// property of the base's implementation, not of this one, so it is joined rather
// than assumed away — neither half can hide the other.
func (w *containerWorkspace) Cleanup() error {
	var errs error
	if w.scratchRoot != "" {
		dir := w.scratchRoot
		w.scratchRoot = ""
		if err := os.RemoveAll(dir); err != nil {
			warnCleanupResidue("container scratch", dir, err)
			errs = fmt.Errorf("remove container scratch: %w", err)
		}
	}
	if w.baseCleanup != nil {
		if err := w.baseCleanup(); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// warnCleanupResidue surfaces a workspace-teardown removal failure: the
// residue is typically root-owned files a wrong-identity container wrote —
// the CONSEQUENCE DETECTOR for every identity hole — which the launching user
// cannot delete. Streamed only, deliberately never recorded as a strictness
// finding: cleanup runs AFTER the run, outside any checkpoint→gate window
// (the caller already released its Mark — see strictness.Close), so a
// recorded finding here would land in an ORPHANED window nothing is watching,
// silently swallowed rather than surfaced — never useful, only ever
// misleading if something changes to make it visible again.
func warnCleanupResidue(what, path string, err error) {
	clidiag.Warn("ctxloom", "%s %s could not be removed (%v) — likely root-owned residue from a wrong-identity container; inspect and remove it manually (e.g. `sudo rm -rf %s`)", what, path, err, path)
}

// runtimeName renders a possibly-nil runtime for diagnostics.
func runtimeName(rt Runtime) string {
	if rt == nil {
		return "none"
	}
	return rt.Name()
}

// containerNameSafe strips characters a container name may not contain; docker/
// podman require [a-zA-Z0-9][a-zA-Z0-9_.-]*.
var containerNameSafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// containerNamePrefix is the fixed prefix containerName stamps every ctxloom
// container name with. ReapOrphanedContainers (container_reap.go) reads this
// same symbol to scope its Enumerate query and, defense-in-depth, to re-check
// every candidate it gets back — so a runtime that ever returned something
// unprefixed (a fake in a test, a future Enumerate bug) can never be reaped.
const containerNamePrefix = "ctxloom-iso-"

// containerName builds a unique, teardown-targetable container name from the
// agent id plus a random suffix (concurrent members must not collide).
func containerName(agentID string) string {
	return fmt.Sprintf("%s%s-%s", containerNamePrefix, sanitizeAgentID(agentID), randToken())
}

// randToken returns a short random hex token for uniqueness. On the (astronomically
// unlikely) rand failure it falls back to a nanosecond stamp — uniqueness matters,
// cryptographic quality does not.
func randToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
