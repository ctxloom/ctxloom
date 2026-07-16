package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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
// profile's resolveAuth): the container gets the engine's scoped env passthrough
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
	// "container-worktree"). Default hostBase on every NewContainer* path;
	// NewContainerWorktree* inject worktreeBase. Nil only on a bare test-built
	// Container{} — Name()/withSessionState nil-guard it.
	base       containerBase
	image      string
	profile    containerProfile // backend-keyed knobs: auth, overlays, local-build recipe
	binaryPath string           // the container's ctxloom path (runs `llm serve <backend>`)
	home       string           // fresh $HOME inside the container
	socketDir  string           // fixed in-container unix-socket dir (bind-mount target)
	// baseContainerfile is the user-provided base Containerfile a local build
	// layers the agent stage onto (config isolation_base_containerfile;
	// "" = the embedded default base). Beats devcontainer auto-detection
	// (locked decision 8).
	baseContainerfile string
	// appRoot is the project root devcontainer auto-detection resolves
	// .devcontainer/devcontainer.json (or .devcontainer.json) against; ""
	// disables auto-detection (same effect as noDevcontainerBase — the zero
	// Container built by NewContainer/NewContainerFor never auto-detects).
	appRoot string
	// noDevcontainerBase opts out of devcontainer auto-detection (config
	// isolation_devcontainer_base: false).
	noDevcontainerBase bool
	// devcontainerService names the docker-compose service to use as the base
	// when the detected devcontainer.json declares dockerComposeFile (config
	// isolation_devcontainer_service).
	devcontainerService string
	// engines selects which engine fragments compose into a COMPOSABLE
	// backend's shared agent image (config isolation_engines); empty = every
	// engine with a known official-installer fragment (composableEngines()).
	engines []string
	// state is the run's session identity (harp + project id), stamped by
	// Prepare (withSessionState); it scopes the read-write state mounts that
	// keep transcripts/session artifacts/task writes durable across teardown
	// (sessionStateMounts). Zero on paths without session accounting.
	state SessionState
	// git is the DI seam used to resolve the live project's git common-dir when
	// the project is itself a LINKED WORKTREE (or submodule) — see
	// gitdirMirrorMount. Nil on the normal construction paths
	// (NewContainer/NewContainerFor/containerFor); gitSeam defaults it to the
	// real git binary. Tests inject a git.Fake.
	git git.Git
}

// Ensure Container satisfies the Policy interface.
var _ Policy = Container{}

// NewContainer builds a container policy bound to a runtime + an EXPLICIT image
// over the default (claude-oriented) profile, with no local-build recipe — the
// image the caller names either exists or the gate degrades. Exposed for the
// docker-gated integration test and callers with a resolved image;
// Resolve("container", backend) goes through NewContainerFor instead.
func NewContainer(rt Runtime, image string) Container {
	c := NewContainerFor(rt, "")
	c.image = image
	return c
}

// NewContainerFor builds the container policy for a REGISTERED backend name: the
// backend's container profile picks the agent image, the auth resolver, the
// managed-config overlay set, and the build sources that let ensureImage build
// the image locally when it is absent. Unknown/empty names get the default
// profile (the generic agent image + claude auth, no local build).
func NewContainerFor(rt Runtime, backend string) Container {
	p := containerProfileFor(backend)
	return Container{
		runtime:    rt,
		base:       hostBase{},
		image:      p.image,
		profile:    p,
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
	c.engines = img.Engines
	if img.Image != "" {
		c.image = img.Image
		c.profile.officialImage = ""
		c.profile.containerfile = nil
		c.profile.engineInstall = nil
		return c
	}
	devBase, _ := resolveDevBase(c.appRoot, c.noDevcontainerBase, c.devcontainerService)
	if image, _, ok := composedIdentity(c.profile, c.baseContainerfile, devBase, c.engines); ok {
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
	sources = buildSources(c.profile, buildSourcesOptions{
		baseOverride:      baseOverride,
		baseContainerfile: c.baseContainerfile,
		devBase:           devBase,
		engines:           c.engines,
	})
	return sources, devBase, err
}

// provenanceFor resolves this container's provenance label for the given
// resolved devcontainer base: composedIdentity's engine-aware digest for a
// COMPOSABLE profile, else the legacy HostProvenanceDigest.
func (c Container) provenanceFor(devBase *baseStage) string {
	if _, prov, ok := composedIdentity(c.profile, c.baseContainerfile, devBase, c.engines); ok {
		return prov
	}
	return HostProvenanceDigest(c.baseContainerfile)
}

// Name identifies the policy: the injected base names it — "container" for the
// host base (live project dir) or "container-worktree" for the worktree base. A
// nil base (a bare test-built Container{}) reports the host name.
func (c Container) Name() string {
	if c.base == nil {
		return "container"
	}
	return c.base.name()
}

// Approvals bypasses the engine's in-tool prompt: the container is the boundary,
// so isolated runs launch with approvals off (better UX, blast radius contained).
func (Container) Approvals() Approvals { return ApprovalsBypass }

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
	dir, baseMounts, baseCleanup, err := c.base.prepareBase(ctx, c.runtime, projectDir, agentID, sc.root, c.profile, c.gitSeam())
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	// Order is inert (SD4): every mount targets a distinct in-container path and
	// renders as an independent --mount. auth + scoped state ride every axis; the
	// base mounts (overlays/gitdir mirror, or the worktree .git mirror) layer on.
	extraMounts := append(append(append([]Mount(nil), sc.auth.mounts...), sc.stateMounts...), baseMounts...)
	return &containerWorkspace{
		dir:         dir,
		scratchRoot: sc.root,
		socketDir:   sc.socketDir,
		extraEnv:    sc.runEnv(),
		extraMounts: extraMounts,
		authMode:    sc.auth.mode,
		agentID:     agentID,
		baseCleanup: baseCleanup,
	}, nil
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

// WithImage overrides this Container's resolved image, keeping the profile's
// auth/overlay/transcript knobs (whichever engine's auth the caller can
// actually resolve) but running a DIFFERENT image — e.g. a docker-gated
// test's minimal harness image, which has nothing to do with the profile's
// own engine but needs SOME resolvable auth to clear PrepareWorkspace's gate.
// Exposed as a narrow, explicit override (not a general profile mutator) so
// production callers (NewContainerFor/containerFor) are unaffected: only a
// caller that deliberately wants this specific mismatch reaches for it.
func (c Container) WithImage(image string) Container {
	c.image = image
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
	// host scratch (the caller removes it), profile the managed-config overlay set.
	// A failure returns the error so the caller removes the scratch and the chain
	// degrades; a base that already created a resource (a worktree) unwinds it
	// WIP-safely before returning.
	prepareBase(ctx context.Context, rt Runtime, projectDir, agentID, scratchRoot string, profile containerProfile, g git.Git) (dir string, mounts []Mount, cleanup func() error, err error)
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
func (hostBase) name() string { return "container" }

// withState is a no-op: the host base persists no per-session scratch of its own
// (the container's durable state rides Container.sessionStateMounts).
func (hostBase) withState(SessionState) containerBase { return hostBase{} }

// prepareBase mounts the LIVE project dir: the managed-config overlays shadow the
// engine's config writers off the host project, and a pointer-file .git gets its
// common dir mirrored so in-container git resolves. Failure returns the error
// (the caller removes the scratch); nothing host-side is created to unwind.
func (hostBase) prepareBase(ctx context.Context, rt Runtime, projectDir, _, scratchRoot string, profile containerProfile, g git.Git) (string, []Mount, func() error, error) {
	overlays, err := containerConfigOverlay(rt, projectDir, scratchRoot, profile.overlayDirs)
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
		return "", nil, nil, gerr
	} else if ok {
		overlays = append(overlays, gitMount)
	}
	return projectDir, overlays, func() error { return nil }, nil
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
	info, err := os.Stat(filepath.Join(projectDir, ".git"))
	if err != nil || info.IsDir() {
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
// resolvable engine auth (the profile's resolver) — then provisions the host
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
	// The image is now locally present, so the shared-filesystem probe is one
	// cheap scratch container. A mismatch means every identical-path mount
	// below (project dir, socket scratch, auth, gitdir mirror) would resolve
	// against a DIFFERENT filesystem and the plugin handshake would hang —
	// erroring HERE turns that hang into the caller's clean per-axis degrade.
	if perr := sharedFSCheck(ctx, c.runtime, c.image); perr != nil {
		hint := "bind mounts of this process's paths cannot resolve through the daemon"
		if InContainer() {
			hint += "; this looks like a dev container using the host's daemon (docker-outside-of-docker) — enable the docker-in-docker feature, or drop `runtime: container`"
		}
		return containerScratch{}, fmt.Errorf("container runtime %s does not share this process's filesystem (%s): %w", runtimeName(c.runtime), hint, perr)
	}
	// The host-side scratch root is created BEFORE auth resolution (not after,
	// as before this comment) because a resolver may need to WRITE into it — a
	// credential COPY a resolver mounts read-write (claude's token-refresh
	// case, auth.go's claudeCredentialCopyMounts) has to land somewhere the
	// container run can bind-mount from and that gets discarded at teardown;
	// the scratch root already serves exactly that role for the socket dir.
	root, err := os.MkdirTemp(containerScratchBase(), "ctxloom-iso-")
	if err != nil {
		// root is normally "" here (MkdirTemp itself failed) — defensive
		// against a mutant flipping this check and discarding a dir MkdirTemp
		// actually created (matches the socketDir guard just below, which
		// already does this).
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
	}
	auth, ok := c.profile.resolveAuth(c.home, root)
	if !ok {
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container auth: %s", c.profile.authHint)
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

// loopbackPluginPort decides the plugin transport for a container run and, when
// TCP is required, RESERVES the host loopback port that bridges it. On Linux the
// host and container share one kernel, so the unix-socket-over-bind-mount
// transport works and this returns 0 (unix — the default, unchanged path). Off
// Linux (macOS/Windows Docker Desktop) the container runs inside a Linux VM
// where a unix socket in a bind-mounted dir is NOT a live endpoint on the host
// kernel, so the plugin must speak TCP over host loopback: reserve a free
// 127.0.0.1 port to BOTH publish (-p 127.0.0.1:P:P) and pin the in-container
// listener to (PLUGIN_MIN_PORT/PLUGIN_MAX_PORT), so publish and listen agree on
// one port. PLUGIN_LISTEN_TCP=1 in the environment forces this path on Linux too
// — the integration-test hook that exercises the identical loopback transport on
// the same kernel. (The reserve-then-close leaves a small window before the
// container binds the port; acceptable, and a bind clash would surface as a
// clean plugin-handshake failure, not a silent wrong-context run.)
func loopbackPluginPort() (int, error) {
	if runtime.GOOS == "linux" && os.Getenv("PLUGIN_LISTEN_TCP") != "1" {
		return 0, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback plugin port: %w", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// containerConfigOverlay builds one bind mount per managed-config directory
// (the profile's overlayDirs — project-relative DIRECTORIES the engine's
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
func containerConfigOverlay(rt Runtime, projectDir, scratchRoot string, overlayDirs []string) ([]Mount, error) {
	mounts := make([]Mount, 0, len(overlayDirs))
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
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, fmt.Errorf("container config overlay target: %w", err)
		}
		mounts = append(mounts, rt.Expose(host, target, false))
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
// isolation_images override, or an explicit image on a profile with no local
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
// WRONG identity under the given runtime ("" = the contract holds). 2da1e34
// replaced rootful docker's blanket `--user uid:gid` (which owned ALL images)
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
// callers discard the returned error by contract). The base teardown is itself
// WIP-safe / noop and never contributes an error.
func (w *containerWorkspace) Cleanup() error {
	var scratchErr error
	if w.scratchRoot != "" {
		dir := w.scratchRoot
		w.scratchRoot = ""
		if err := os.RemoveAll(dir); err != nil {
			warnCleanupResidue("container scratch", dir, err)
			scratchErr = fmt.Errorf("remove container scratch: %w", err)
		}
	}
	if w.baseCleanup != nil {
		_ = w.baseCleanup()
	}
	return scratchErr
}

// warnCleanupResidue surfaces a workspace-teardown removal failure: the
// residue is typically root-owned files a wrong-identity container wrote —
// the CONSEQUENCE DETECTOR for every identity hole — which the launching user
// cannot delete. Streamed only, deliberately never recorded as a strictness
// finding: cleanup runs AFTER the run, outside any checkpoint→gate window,
// and a recorded finding would land inside a CONCURRENT member's window
// (isolationGateMu serializes gate windows, not end-of-run cleanups) and fail
// that member spuriously.
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
	id := containerNameSafe.ReplaceAllString(agentID, "-")
	id = strings.Trim(id, "-._")
	if id == "" {
		id = "agent"
	}
	return fmt.Sprintf("ctxloom-iso-%s-%s", id, randToken())
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
