package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Default container-policy parameters. The image is the REQUIRED agent image: when
// it is absent and not locally buildable the policy degrades down the chain to
// None. That degrade is a fatal finding (ClassIsolation) the choke owner aborts on
// unless --degraded, since the image is only required for an EXPLICITLY-requested
// container.
// The binary/home/socket-dir are the in-container conventions the minimal image
// (and the future production image) must honour.
const (
	defaultContainerImage  = "ctxloom-agent:latest"
	defaultContainerBinary = "/usr/local/bin/ctxloom"
	// defaultContainerHome is the ctxloom user's home baked into the agent
	// images (the entrypoint remaps that user to the launching uid/gid and
	// hands it this home). Auth credential mounts land under it.
	defaultContainerHome      = "/home/ctxloom"
	defaultContainerSocketDir = "/run/ctxloom/plugin"
)

// Container is the container isolation policy: it runs each plugin (and its
// engine) inside a fresh container — a REAL boundary, so approvals are bypassed
// (the container replaces the in-engine prompt as the safety net). For the
// top-level run it bind-mounts the live project at its identical absolute path
// (cwd + .git resolve unchanged, WIP intact, edits land in the real files) with a
// fresh $HOME (engine global state isolated, "fresh except mounted creds" — see
// below). Any inability to launch returns an error the caller catches and
// degrades down the chain to None; because a container tier is only ever built
// for an EXPLICIT request, that lost boundary is a fatal finding (ClassIsolation)
// the choke owner aborts on unless --degraded (CLAUDE.md fail-loudly).
//
// AUTH crosses the boundary deliberately and scoped (PrepareWorkspace → the
// spec's resolveAuth): the container gets the engine's scoped env passthrough
// (claude: ANTHROPIC_* when ANTHROPIC_API_KEY is set; kiro: KIRO_API_KEY) or the
// engine's credentials bind-mounted READ-ONLY into the fresh HOME (claude
// subscription OAuth). No resolvable auth → PrepareWorkspace errors → the caller
// degrades down the chain to None — a fatal finding (ClassIsolation) the choke
// owner aborts on unless --degraded, since the container was EXPLICITLY
// requested. Only the TRUSTED top-level run reaches this; low-trust fan-out auth
// (per-agent keys/budgets, T1.5) is a later concern.
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
	binaryPath string              // the container's ctxloom path (runs `llm serve <backend>`)
	home       string              // fresh $HOME inside the container
	socketDir  string              // fixed in-container unix-socket dir (bind-mount target)
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

// Ensure Container satisfies the Policy interface.
var _ Policy = Container{}

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
		runtime:    rt,
		base:       hostBase{},
		image:      p.image,
		engine:     backend,
		engineSpec: p,
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		socketDir:  defaultContainerSocketDir,
	}
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
	if image, _, ok := composedIdentity(c.engineSpec, c.baseContainerfile, devBase, c.engine); ok {
		c.image = image
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

// provenanceFor resolves this container's provenance label for the given
// resolved devcontainer base: composedIdentity's engine-aware digest for a
// COMPOSABLE spec, else the legacy HostProvenanceDigest.
func (c Container) provenanceFor(devBase *baseStage) string {
	if _, prov, ok := composedIdentity(c.engineSpec, c.baseContainerfile, devBase, c.engine); ok {
		return prov
	}
	return HostProvenanceDigest(c.baseContainerfile)
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

// PrepareWorkspace provisions the container run's host-side scratch and doubles as
// the degrade gate. It fails (→ caller falls back to None) when no runtime can
// launch, the required image is absent, OR no engine auth can be resolved
// (resolveContainerAuth). Otherwise it delegates the workspace-flavored tail to
// the injected base (host → the identical-path project dir; worktree → a per-agent
// checkout) and returns a workspace whose Dir() is that cwd and whose Cleanup()
// removes the host scratch tree then runs the base teardown; the container itself
// is torn down via the client's Kill. agentID scopes the container name.
func (c Container) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	sc, err := c.prepareContainerScratch(ctx)
	if err != nil {
		return nil, err
	}
	// sc.root is a real on-disk scratch tree that exists from here on but has
	// no owning Workspace yet. The explicit error path below already removes it
	// on a normal prepareBase failure; this guards the case a normal error
	// return can't: a PANIC inside prepareBase (worktree.go's own PrepareWorkspace
	// carries the identical guard for ITS resources) would otherwise skip that
	// removal entirely and leak sc.root under the OS temp dir.
	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(sc.root)
			panic(r)
		}
	}()
	// The base provisions the cwd the container mounts and its own mounts: the
	// host base shadows the LIVE project's managed-config dirs (overlays) and
	// mirrors a pointer-file .git; the worktree base creates the per-agent
	// checkout and mirrors its .git common-dir. A base that created a resource
	// (a worktree) unwinds it WIP-safely before returning the error; the scratch
	// removal is ours regardless, so a degrade never leaks a temp dir.
	dir, baseMounts, baseCleanup, err := c.base.prepareBase(ctx, c.runtime, projectDir, agentID, sc.root, c.engineSpec, c.gitSeam())
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	// Order is inert (SD4): every mount targets a distinct in-container path and
	// renders as an independent --mount. auth + scoped state ride every axis; the
	// base mounts (overlays/gitdir mirror, or the worktree .git mirror) layer on.
	extraMounts := append(append(append([]Mount(nil), sc.auth.mounts...), sc.stateMounts...), baseMounts...)
	// The shared-filesystem probe runs HERE — now that every real mount root is
	// known: dir (the project dir, or the worktree checkout prepareBase just
	// created), sc.root (covers the socket dir, config overlays, session-state
	// mounts, and any copy-based credential mount), and every OTHER mount's host
	// path (a direct credential-file mount, or a linked worktree's gitdir
	// mirror). Probing a synthetic tempdir elsewhere (the prior behavior) only
	// ever proved THAT directory's sharing status — a standing false positive on
	// a partially-shared Docker Desktop file-sharing list, exactly the platform
	// this probe exists to protect. A mismatch on ANY root means that
	// identical-path mount would resolve against a DIFFERENT filesystem and the
	// plugin handshake would hang — erroring here turns that hang into the
	// caller's clean per-axis degrade. The base already succeeded (a worktree
	// may already exist), so a probe failure unwinds it exactly like Cleanup()
	// would: scratch first, then the base teardown.
	roots := mountProbeRoots(dir, sc.root, extraMounts)
	if perr := sharedFSCheck(ctx, c.runtime, c.image, roots); perr != nil {
		_ = os.RemoveAll(sc.root)
		_ = baseCleanup()
		return nil, sharedFSGateError(c.runtime, perr)
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
	extraEnv := append(sc.runEnv(), gitIdentityEnv(agentID)...)
	return &containerWorkspace{
		dir:         dir,
		scratchRoot: sc.root,
		socketDir:   sc.socketDir,
		extraEnv:    extraEnv,
		extraMounts: extraMounts,
		authMode:    sc.auth.mode,
		agentID:     agentID,
		baseCleanup: baseCleanup,
	}, nil
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
// chain — ISO1's ACP container transport (internal/acp), whose caller (an
// editor-launched session) must never silently degrade a requested container
// to the host: PrepareWorkspace's error is the ONLY outcome of a failed
// gate, unlike chainFor's degrade-to-None-unless-strict chain.
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

// ExecSpec builds the RunSpec for running an ARBITRARY in-container command
// against a workspace this Container already prepared via PrepareWorkspace —
// ISO1's use case: the container hosts the target agent's OWN ACP subprocess
// directly (e.g. `claude-code-acp`), piped over plain stdio, rather than
// go-plugin's gRPC-over-socket transport for the `ctxloom llm serve`
// protocol (which SpawnClient/launchSpec build instead). ws must be the
// Workspace THIS Container returned from PrepareWorkspace.
//
// It assembles the SAME three ingredients buildRunSpec (runner.go) computes
// for the go-plugin path, minus the plugin-handshake-specific pieces (the
// socket-dir mount, the curated handshake env, the loopback-port publish):
// the identical-path project mount (ExposeIdentical — the SAME primitive
// buildRunSpec's project mount and gitCommonDirMount use, so a non-identity
// path mapper, when one exists, applies here too without another call site
// to update), the workspace's own auth/overlay/gitdir/state mounts
// (cw.extraMounts) plus any caller-supplied extraMounts (e.g. ISO1's
// reach-back socket-dir mount), and the fixed container base env
// (containerBaseEnv) + the workspace's scoped auth env passthrough
// (cw.extraEnv) plus any caller-supplied extraEnv.
func (c Container) ExecSpec(ws Workspace, command []string, extraEnv []string, extraMounts []Mount) (RunSpec, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return RunSpec{}, fmt.Errorf("container exec: unexpected workspace %T (expected a container workspace)", ws)
	}
	// An empty command renders as NOTHING after the image (renderRunSpec), so the
	// container would start the image's own default entrypoint while this returned
	// a valid-looking spec — a success with zero payload, and the caller then
	// speaks its protocol at whatever that entrypoint is. Refuse instead.
	if len(command) == 0 {
		return RunSpec{}, fmt.Errorf("container exec: empty command (the container would run the image's default entrypoint instead)")
	}
	mounts := make([]Mount, 0, 1+len(cw.extraMounts)+len(extraMounts))
	mounts = append(mounts, c.runtime.ExposeIdentical(cw.dir, false))
	mounts = append(mounts, cw.extraMounts...)
	mounts = append(mounts, extraMounts...)

	env := make([]string, 0, len(containerBaseEnv)+len(cw.extraEnv)+len(extraEnv))
	env = append(env, containerBaseEnv...)
	env = append(env, cw.extraEnv...)
	env = append(env, extraEnv...)

	return RunSpec{
		Image:   c.image,
		Name:    containerName(cw.agentID),
		WorkDir: c.runtime.mapper().toContainer(cw.dir),
		Home:    c.home,
		Command: command,
		Env:     env,
		Mounts:  mounts,
	}, nil
}

// MCPCommandOverride returns the in-container ctxloom binary path
// (defaultContainerBinary, threaded as c.binaryPath — the container axis's
// single source of truth) that a container cell's MCP-surface writer should
// stamp into the ctxloom-managed stdio command instead of the host self-exec
// path (agent.CtxloomCommand). This is dire-five's fix: the engine inside the
// container reads its bind-mounted, identical-path .mcp.json, but the process
// that MATERIALIZED that surface is not necessarily the one running inside
// the container — relying on self-exec resolution to "just happen" to agree
// is fragile, so the container policy states its binary path explicitly.
//
// Exposed as a narrow capability (not a Policy interface method, probed via
// operations.MCPCommandOverrideForPolicy) rather than a Policy method so
// None/Worktree need no method at all: their absence IS "no override",
// which is exactly what preserves the host self-exec-absolute invariant
// (CtxloomCommand's doc: staged/installed divergence) on every non-container
// cell. A leak of this override onto a non-container cell would reintroduce
// that exact divergence bug — see the host-unchanged unit test pinning it.
func (c Container) MCPCommandOverride() string { return c.binaryPath }

// Runtime returns the container's launch runtime (docker/podman) — the seam the
// docker-exec vpio.Launcher needs to render `exec -it` for an interactive
// top-level container turn (Phase 2a-A). Exposed as a narrow accessor probed
// via operations.RuntimeForPolicy, like MCPCommandOverride, so None/Worktree
// (which carry no runtime) need no method.
func (c Container) Runtime() Runtime { return c.runtime }

// ContainerPersistDir returns the IN-CONTAINER path the host's session persist
// dir (~/.ctxloom/sessions/<harp>/persist) is bind-mounted to: the container's
// fresh HOME rebases it (sessionStateMounts binds the host persist under
// c.home/.ctxloom/sessions/<harp>/persist). It is where the docker-exec turn
// reads the RunStart handoff (§5.A4) — the SAME file the host wrote to the host
// persist dir, at its container path. "" for a blank harp (no session state).
func (c Container) ContainerPersistDir(harp string) string {
	if harp == "" {
		return ""
	}
	return filepath.Join(c.home, paths.AppDirName, paths.SessionsDir, harp, paths.PersistDirName)
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
	// prepareBase provisions the base workspace: the cwd dir the container mounts,
	// the base-specific mounts, and a cleanup that tears the base's own resource
	// down (called by containerWorkspace.Cleanup AFTER the shared scratch is
	// removed). rt/g are the runtime + git seams, scratchRoot the already-created
	// host scratch (the caller removes it), spec the managed-config overlay set.
	// A failure returns the error so the caller removes the scratch and the chain
	// degrades; a base that already created a resource (a worktree) unwinds it
	// WIP-safely before returning.
	prepareBase(ctx context.Context, rt Runtime, projectDir, agentID, scratchRoot string, spec engineContainerSpec, g git.Git) (dir string, mounts []Mount, cleanup func() error, err error)
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

// prepareBase mounts the LIVE project dir: the managed-config overlays shadow the
// engine's config writers off the host project, and a pointer-file .git gets its
// common dir mirrored so in-container git resolves. Failure returns the error
// (the caller removes the scratch); nothing host-side is created to unwind.
func (hostBase) prepareBase(ctx context.Context, rt Runtime, projectDir, _, scratchRoot string, spec engineContainerSpec, g git.Git) (string, []Mount, func() error, error) {
	overlays, created, err := containerConfigOverlay(rt, projectDir, scratchRoot, spec.overlayDirs)
	if err != nil {
		return "", nil, nil, err
	}
	// When the LIVE project is itself a linked worktree (or a submodule) its .git
	// is a POINTER FILE whose common dir lives OUTSIDE projectDir — and so is not
	// covered by the identical-path project mount. Mirror that common dir so
	// in-container git resolves the repo, exactly as the worktree base does. A
	// resolution failure fails this workspace so the chain degrades
	// (fatal-unless-degraded), never a silent broken-git launch.
	if gitMount, ok, gerr := gitdirMirrorMount(ctx, rt, g, projectDir); gerr != nil {
		pruneCreatedOverlayTargets(projectDir, created)
		return "", nil, nil, gerr
	} else if ok {
		overlays = append(overlays, gitMount)
	}
	// The host base creates no workspace of its own, but it DOES create the
	// overlay mountpoints inside the user's live project. Undoing those is this
	// base's whole teardown: nothing is supposed to be written through them (the
	// overlay shadows every write into scratch), so a target this run created and
	// that is still empty is pure residue.
	return projectDir, overlays, func() error {
		pruneCreatedOverlayTargets(projectDir, created)
		return nil
	}, nil
}

// gitdirMirrorMount returns the git common-dir mirror mount the plain container
// needs when the LIVE PROJECT is itself a linked worktree (or a submodule) — i.e.
// projectDir/.git is a POINTER FILE, not a directory — whose common git dir lives
// OUTSIDE projectDir and so is NOT covered by the identical-path project mount,
// leaving in-container `git` unable to resolve the repo. ok=false (no extra mount)
// when .git is a directory or absent: the common dir is inside the project mount
// already (a normal main-repo checkout), or there is no repo to mirror. It reuses
// the same identical-path mirror the worktree base builds (gitCommonDirMount).
func gitdirMirrorMount(ctx context.Context, rt Runtime, g git.Git, projectDir string) (Mount, bool, error) {
	gitPath := filepath.Join(projectDir, ".git")
	info, err := os.Stat(gitPath)
	// ABSENT is a real "no mirror needed" (no repo to mirror). An unreadable .git
	// is not: it means we could not tell a pointer file from a directory, and
	// answering "no mirror needed" launches a container whose git cannot resolve
	// the repo. Fail the workspace so the chain degrades loudly instead.
	if errors.Is(err, os.ErrNotExist) {
		return Mount{}, false, nil
	}
	if err != nil {
		return Mount{}, false, fmt.Errorf("stat %s to decide the container gitdir mirror: %w", gitPath, err)
	}
	if info.IsDir() {
		return Mount{}, false, nil
	}
	m, err := gitCommonDirMount(ctx, rt, g, projectDir)
	if err != nil {
		return Mount{}, false, err
	}
	return m, true, nil
}

// containerScratch is the host-side scratch every container run needs regardless
// of its workspace: the temp root removed on Cleanup, the `sock` subdir go-plugin
// creates the unix-socket dir under (bind-mounted into the container), the
// resolved engine auth (env passthrough or read-only credential mounts), and the
// host terminal description forwarded into the run.
type containerScratch struct {
	root      string
	socketDir string
	auth      containerAuth
	termEnv   []string
	// stateMounts are the scoped RW session-state mounts (transcript store,
	// session persist dir, shared task log — see sessionStateMounts) every
	// container run threads into its spec regardless of workspace axis.
	stateMounts []Mount
}

// runEnv composes the per-run env threaded into the container spec: the scoped
// auth passthrough (name-only entries — see containerAuth.envPassthrough — so the
// secret value never enters the argv) plus the host terminal description
// (TERM/COLORTERM as KEY=VAL, non-secret), which the curated handshake env
// deliberately drops. Returns a fresh slice so callers never alias the scratch's
// fields. The two forms mix in one slice and render uniformly through
// renderRunSpec's `-e <entry>` loop (docker's `-e` grammar accepts both).
func (sc containerScratch) runEnv() []string {
	return append(append([]string(nil), sc.auth.envPassthrough...), sc.termEnv...)
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

// containerScratchBase returns the PARENT directory for a run's host-side scratch
// tree (the empty default means os.TempDir). It exists to keep the plugin unix
// socket path short on darwin: go-plugin creates the socket at
// <scratch>/sock/plugin-dir<rand>/plugin<rand>, and on macOS the default $TMPDIR
// is a long per-user /var/folders/… path that pushes the full path past darwin's
// ~104-byte AF_UNIX sun_path limit — so the host dial fails with "invalid
// argument" even when the boundary is otherwise fine. /tmp (a Docker Desktop
// default-shared path, under the shared /private tree) is short and keeps the
// socket comfortably under the limit. Linux and every other OS keep os.TempDir
// unchanged — the limit is generous there (~108 bytes) and $TMPDIR is short.
func containerScratchBase() string {
	if runtime.GOOS == "darwin" {
		return "/tmp"
	}
	return ""
}

// prepareContainerScratch runs the container degrade gate — a launchable runtime,
// the required image present (or locally buildable, see ensureImage), and
// resolvable engine auth (the spec's resolver) — then provisions the host
// scratch (temp root + socket dir). Any gate failure returns an error so the
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
	// The host-side scratch root is created BEFORE auth resolution and passed to
	// resolveAuth, historically because a resolver could COPY a credential into
	// it and mount that copy read-write. No resolver does that anymore — claude's
	// token-refresh case bind-mounts the REAL host credential read-write
	// (auth.go's claudeCredentialMounts), so the scratch dir it is handed goes
	// unused there — but the root is still needed at this point for the socket
	// dir carved out of it just below, so its creation stays here.
	root, err := os.MkdirTemp(containerScratchBase(), "ctxloom-iso-")
	if err != nil {
		// root is normally "" here (MkdirTemp itself failed) — defensive
		// against a mutant flipping this check and discarding a dir MkdirTemp
		// actually created (matches the socketDir guard just below, which
		// already does this).
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
	}
	auth, ok := c.engineSpec.resolveAuth(c.home, root)
	if !ok {
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container auth: %s", c.engineSpec.authHint)
	}
	// Session-state persistence is part of the container gate: a run whose
	// state dirs cannot be prepared errors here so the caller's degrade chain
	// raises the fatal-unless-degraded ClassIsolation finding, exactly like an
	// absent image or unresolvable auth — never a silent state-losing launch.
	stateMounts, err := c.sessionStateMounts()
	if err != nil {
		_ = os.RemoveAll(root)
		return containerScratch{}, err
	}
	socketDir := filepath.Join(root, "sock")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container socket scratch: %w", err)
	}
	return containerScratch{root: root, socketDir: socketDir, auth: auth, termEnv: hostTerminalEnv(os.Getenv), stateMounts: stateMounts}, nil
}

// hostTerminalEnv forwards the host's terminal description into the container
// (a docker/podman -e overrides the image ENV): the curated handshake env
// (containerHandshakeEnv) deliberately drops the host environment, which would
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

// SpawnClient launches the plugin inside a container for the prepared workspace
// via the go-plugin RunnerFunc + AddrTranslator transport. The returned client is
// an *LLMRunner (pb.Client) whose Kill force-removes the container. It is the
// SINGLE spawn path for BOTH bases now that ContainerWorktree collapsed into
// Container{base}: the workspace→LaunchSpec adaptation is factored into launchSpec
// (cw.dir is the base-provided cwd — the live project dir for the host base, the
// per-agent worktree checkout for the worktree base), so no per-policy LaunchSpec
// construction is duplicated any more.
func (c Container) SpawnClient(backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (pb.Client, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return nil, fmt.Errorf("container spawn: unexpected workspace %T (expected a container workspace)", ws)
	}
	spec := c.launchSpec(backendName, label, verbosity, cw)
	spec.SpawnEnv = spawnEnv
	return c.runtime.Spawn(spec)
}

// launchSpec is the workspace→LaunchSpec adaptation the runtime's Spawn consumes:
// the container conventions come from the Container (image/binary/home/socket),
// the per-run cwd + host socket scratch + scoped auth env/credential/overlay/gitdir
// mounts from the prepared workspace. Centralizing it here is what makes SpawnClient
// "just adaptation plus runtime.Spawn" — the single builder that replaced the two
// near-identical per-policy constructions the collapse removed.
func (c Container) launchSpec(backendName, label string, verbosity int, cw *containerWorkspace) LaunchSpec {
	return LaunchSpec{
		BackendName:        backendName,
		Label:              label,
		Verbosity:          verbosity,
		AgentID:            cw.agentID,
		Image:              c.image,
		BinaryPath:         c.binaryPath,
		Home:               c.home,
		ContainerSocketDir: c.socketDir,
		WorkDir:            cw.dir,
		HostSocketDir:      cw.socketDir,
		ExtraEnv:           cw.extraEnv,
		ExtraMounts:        cw.extraMounts,
		AuthMode:           cw.authMode,
	}
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
func containerConfigOverlay(rt Runtime, projectDir, scratchRoot string, overlayDirs []string) ([]Mount, []string, error) {
	mounts := make([]Mount, 0, len(overlayDirs))
	var created []string
	for i, rel := range overlayDirs {
		host := filepath.Join(scratchRoot, fmt.Sprintf("cfg%d", i))
		if err := os.MkdirAll(host, 0o755); err != nil {
			return nil, nil, fmt.Errorf("container config overlay scratch: %w", err)
		}
		target := filepath.Join(projectDir, rel)
		if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
			// Ours to undo: the project did not have this directory before the
			// run. Recorded BEFORE the MkdirAll below, which is the only moment
			// the distinction is still observable.
			created = append(created, target)
		}
		seedOverlay(target, host)
		// Pre-create the overlay TARGET (as the invoking user — this process runs
		// as it) BEFORE docker sees the mount. The target is nested inside the
		// identical-path project bind (mounted rw at the SAME host path), so a
		// still-missing target would make a rootful docker daemon create the bind
		// mountpoint AS ROOT — and that root-owned dir lands in the real HOST
		// project through the identical-path bind, EACCES-ing every later host
		// run's managed-config writers. Creating it ourselves makes docker find it
		// existing. Idempotent (a no-op — never a chmod — when it already exists).
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, nil, fmt.Errorf("container config overlay target: %w", err)
		}
		mounts = append(mounts, rt.Expose(host, target, false))
	}
	return mounts, created, nil
}

// pruneCreatedOverlayTargets removes the managed-config directories this run
// created inside the user's HOST project, walking each one upward through the
// intermediate directories MkdirAll made with it and stopping at projectDir.
//
// Removal is gated on an EXPLICIT emptiness check, never on Remove failing for a
// non-empty directory: that is true on the OS filesystem but FALSE on afero's
// MemMapFs, and this package's tests are not the only consumers of that
// asymmetry. Anything a directory holds belongs to the user (or to a writer the
// overlay did not shadow), so content stops the walk — as does any directory the
// project already owned, which never enters created in the first place. Failures
// are silent by design: an unprunable residue is a cosmetic empty directory, and
// warning about it on every teardown would be noise, not signal.
func pruneCreatedOverlayTargets(projectDir string, created []string) {
	for _, target := range created {
		for dir := target; strings.HasPrefix(dir, projectDir+string(filepath.Separator)); dir = filepath.Dir(dir) {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(dir); err != nil {
				break
			}
		}
	}
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
func gitCommonDirMount(ctx context.Context, rt Runtime, g git.Git, dir string) (Mount, error) {
	common, err := g.CommonDir(ctx, dir)
	if err != nil {
		return Mount{}, fmt.Errorf("resolve git common dir for container gitdir mount: %w", err)
	}
	// ExposeIdentical (not Expose(common, common, ...)) routes through the
	// runtime's pathMapper (lanky-pod's seam) — identity today, so this is
	// unchanged; a non-identity mapper would need the SAME translation the
	// project mount gets, which is exactly ExposeIdentical's contract.
	return rt.ExposeIdentical(common, false), nil
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
	return exec.CommandContext(cctx, c.runtime.Binary(), "image", "inspect", c.image).Run() == nil
}

// overrideIdentityFixIt names the ways out when a user-supplied image cannot
// satisfy the identity contract: make the image entrypoint-governed, or accept
// the image's own identity via degraded mode.
const overrideIdentityFixIt = "base the isolation_images override on a ctxloom-built agent image (or install ctxloom-entrypoint as its ENTRYPOINT — see `ctxloom container build`), or pass --degraded to run it with the image's own identity"

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
	if d, ok := rt.(Docker); ok && d.rootless {
		// Rootless docker passes no PUID: container-ROOT is the one uid that
		// maps to the launching host user, so the image must run as root.
		if rootishUser(id.User) {
			return ""
		}
		return fmt.Sprintf("its USER %q maps to a subordinate uid under the rootless docker daemon (only container-root maps to the launching user)", id.User)
	}
	// Every PUID-passing mode (rootful docker, podman both modes — and any
	// unknown runtime, conservatively) relies on the baked ctxloom entrypoint,
	// started as root, to remap and drop.
	if !entrypointGoverned(id.Entrypoint) {
		return "it does not run the ctxloom identity-remap entrypoint, so the PUID/PGID remap is inert and the engine runs as the image's own user"
	}
	if !rootishUser(id.User) {
		return fmt.Sprintf("its USER %q prevents the identity remap (the ctxloom entrypoint must start as root to remap and drop)", id.User)
	}
	return ""
}

// checkRunAsIsIdentity gates a run-as-is (user-owned) image on the identity
// contract. A violation — or an unverifiable config — routes a ClassIsolation
// finding: in strict mode the choke owner aborts on it BEFORE SpawnClient
// (never a silent wrong-identity start), while --degraded records nothing and
// the run proceeds on the image's own identity with the streamed warning.
// Locally-built images bake the entrypoint, so the contract holds by
// construction and no inspect runs.
func (c Container) checkRunAsIsIdentity(ctx context.Context) {
	if !c.runAsIs() {
		return
	}
	id, err := c.imageIdentityConfig(ctx)
	if err != nil {
		strictness.Fail(strictness.ClassIsolation, overrideIdentityFixIt,
			"cannot verify the identity contract of user-supplied container image %q (%v); it may start with the wrong identity and write wrongly-owned files into the project", c.image, err)
		return
	}
	if problem := runAsIsIdentityProblem(c.runtime, id); problem != "" {
		strictness.Fail(strictness.ClassIsolation, overrideIdentityFixIt,
			"user-supplied container image %q would start with the WRONG identity on %s: %s — files it writes into the mounted project would not be owned by you (e.g. root-owned)", c.image, runtimeName(c.runtime), problem)
	}
}

// containerWorkspace is the container policy's workspace, unified across both
// bases: Dir() is the cwd the container mounts identical-path — the LIVE project
// dir (host base) or the per-agent worktree checkout (worktree base) — and
// Cleanup() removes the host-side scratch tree (socket dir + config overlays)
// then runs the base's own teardown (a noop for the host base; the WIP-safe,
// nested-aware worktree teardown for the worktree base). The container is killed
// via the client BEFORE Cleanup. extraEnv/extraMounts carry the resolved auth env
// + credential/overlay/gitdir mounts threaded into the run spec at SpawnClient.
type containerWorkspace struct {
	dir         string            // identical-path cwd (project dir or worktree checkout)
	scratchRoot string            // host scratch tree removed by Cleanup
	socketDir   string            // scratchRoot/sock — go-plugin's unix-socket temp dir
	extraEnv    []string          // scoped auth env (passthrough)
	extraMounts []Mount           // auth credential mounts + config overlays / gitdir mirror
	authMode    containerAuthMode // how auth was resolved (diagnostics; no secrets)
	agentID     string
	// baseCleanup tears down the base's own resource AFTER the scratch is
	// removed: a noop for the host base (the live project dir is never torn
	// down), the worktree's WIP-safe teardown for the worktree base. Nil only on
	// a bare test-built workspace — Cleanup nil-guards it.
	baseCleanup func() error
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

// containerName builds a unique, teardown-targetable container name from the
// agent id plus a random suffix (concurrent members must not collide).
func containerName(agentID string) string {
	return fmt.Sprintf("ctxloom-iso-%s-%s", sanitizeAgentID(agentID), randToken())
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
