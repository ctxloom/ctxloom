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
	runtime    ContainerRuntime
	image      string
	profile    containerProfile // backend-keyed knobs: auth, overlays, local-build recipe
	binaryPath string           // the container's ctxloom path (runs `llm serve <backend>`)
	home       string           // fresh $HOME inside the container
	socketDir  string           // fixed in-container unix-socket dir (bind-mount target)
	// baseContainerfile is the user-provided base Containerfile a local build
	// layers the agent stage onto (config isolation_base_containerfile;
	// "" = the embedded default base).
	baseContainerfile string
	// state is the run's session identity (harp + project id), stamped by
	// Prepare (withSessionState); it scopes the read-write state mounts that
	// keep transcripts/session artifacts/task writes durable across teardown
	// (sessionStateMounts). Zero on paths without session accounting.
	state SessionState
}

// Ensure Container satisfies the Policy interface.
var _ Policy = Container{}

// NewContainer builds a container policy bound to a runtime + an EXPLICIT image
// over the default (claude-oriented) profile, with no local-build recipe — the
// image the caller names either exists or the gate degrades. Exposed for the
// docker-gated integration test and callers with a resolved image;
// Resolve("container", backend) goes through NewContainerFor instead.
func NewContainer(rt ContainerRuntime, image string) Container {
	c := NewContainerFor(rt, "")
	c.image = image
	return c
}

// NewContainerFor builds the container policy for a REGISTERED backend name: the
// backend's container profile picks the agent image, the auth resolver, the
// managed-config overlay set, and the build sources that let ensureImage build
// the image locally when it is absent. Unknown/empty names get the default
// profile (the generic agent image + claude auth, no local build).
func NewContainerFor(rt ContainerRuntime, backend string) Container {
	p := containerProfileFor(backend)
	return Container{
		runtime:    rt,
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
// Containerfile (config isolation_base_containerfile) makes the on-the-fly
// build layer the agent stage onto the user's base instead of the default.
func containerFor(rt ContainerRuntime, backend string, img ImageConfig) Container {
	c := NewContainerFor(rt, backend)
	c.baseContainerfile = img.BaseContainerfile
	if img.Image != "" {
		c.image = img.Image
		c.profile.officialImage = ""
		c.profile.containerfile = nil
	}
	return c
}

// Name identifies the policy.
func (Container) Name() string { return "container" }

// Approvals bypasses the engine's in-tool prompt: the container is the boundary,
// so isolated runs launch with approvals off (better UX, blast radius contained).
func (Container) Approvals() Approvals { return ApprovalsBypass }

// PrepareWorkspace provisions the container run's host-side scratch and doubles as
// the degrade gate. It fails (→ caller falls back to None) when no runtime can
// launch, the required image is absent, OR no engine auth can be resolved
// (resolveContainerAuth). Otherwise it returns a workspace whose Dir() is the
// identical-path project directory and whose Cleanup() removes the host scratch
// tree (socket dir + config overlays); the container itself is torn down via the
// client's Kill. agentID scopes the container name.
func (c Container) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	sc, err := c.prepareContainerScratch(ctx)
	if err != nil {
		return nil, err
	}
	// The top-level run bind-mounts the LIVE project rw, so ctxloom's managed-config
	// writers would land in the host project; shadow each with a scratch overlay to
	// keep the host clean. The overlay set is the PROFILE's (claude: .claude;
	// kiro: .kiro; plus the shared .ctxloom/cache). (The worktree-in-container
	// composition skips this — its mounted workspace is an ephemeral worktree, so
	// writes there are torn down.)
	overlays, err := containerConfigOverlay(projectDir, sc.root, c.profile.overlayDirs)
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	return &containerWorkspace{
		dir:         projectDir,
		scratchRoot: sc.root,
		socketDir:   sc.socketDir,
		extraEnv:    sc.runEnv(),
		extraMounts: append(append(append([]Mount(nil), sc.auth.mounts...), overlays...), sc.stateMounts...),
		authMode:    sc.auth.mode,
		agentID:     agentID,
	}, nil
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
	auth, ok := c.profile.resolveAuth(c.home)
	if !ok {
		return containerScratch{}, fmt.Errorf("container auth: %s", c.profile.authHint)
	}
	// Session-state persistence is part of the container gate: a run whose
	// state dirs cannot be prepared errors here so the caller's degrade chain
	// raises the fatal-unless-degraded ClassIsolation finding, exactly like an
	// absent image or unresolvable auth — never a silent state-losing launch.
	stateMounts, err := c.sessionStateMounts()
	if err != nil {
		return containerScratch{}, err
	}
	root, err := os.MkdirTemp(containerScratchBase(), "ctxloom-iso-")
	if err != nil {
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
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
// an *LLMRunner (pb.Client) whose Kill force-removes the container. The workspace's
// scoped auth env + credential/overlay mounts are threaded into the run spec.
func (c Container) SpawnClient(backendName, label string, verbosity int, ws Workspace) (pb.Client, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return nil, fmt.Errorf("container spawn: unexpected workspace %T (expected a container workspace)", ws)
	}
	return c.spawnInContainer(backendName, label, verbosity, cw.agentID, cw.dir, cw.socketDir, cw.extraEnv, cw.extraMounts, cw.authMode)
}

// spawnInContainer launches the plugin inside a container for a prepared run and
// returns its client (Kill force-removes the container). It is shared by the
// top-level Container workspace (workDir = the live project) and the
// worktree-in-container composition (workDir = the per-member worktree): the two
// differ only in the workDir mounted as cwd and the extra mounts (config overlay
// vs the .git gitdir mirror), both passed in by the caller. hostSocketDir is the
// host scratch dir go-plugin created the unix socket under (bind-mounted to the
// fixed in-container c.socketDir).
func (c Container) spawnInContainer(backendName, label string, verbosity int, agentID, workDir, hostSocketDir string, extraEnv []string, extraMounts []Mount, authMode containerAuthMode) (pb.Client, error) {
	command := []string{c.binaryPath, "llm", "serve", backendName}
	if label != "" {
		command = append(command, "--label", label)
	}

	// Diagnostic only (never secrets): how the in-container engine is authenticated.
	if verbosity > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container auth via %s\n", authMode)
	}

	// Pick the plugin transport: unix socket on Linux (fast, shared kernel), TCP
	// over host loopback off Linux where a bind-mounted unix socket cannot cross
	// the Docker Desktop VM boundary. A reservation failure is fatal to this run
	// (the caller degrades to None) — better than launching a container the host
	// could never reach.
	loopbackPort, err := loopbackPluginPort()
	if err != nil {
		return nil, err
	}
	if verbosity > 0 && loopbackPort > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container plugin transport: TCP loopback 127.0.0.1:%d\n", loopbackPort)
	}

	name := containerName(agentID)
	runnerFunc := containerRunnerFunc(c.runtime, c.image, name, workDir, c.home, command, c.socketDir, extraEnv, extraMounts, loopbackPort)
	return pb.NewContainerClient(backendName, label, verbosity, runnerFunc, hostSocketDir)
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
func containerConfigOverlay(projectDir, scratchRoot string, overlayDirs []string) ([]Mount, error) {
	mounts := make([]Mount, 0, len(overlayDirs))
	for i, rel := range overlayDirs {
		host := filepath.Join(scratchRoot, fmt.Sprintf("cfg%d", i))
		if err := os.MkdirAll(host, 0o755); err != nil {
			return nil, fmt.Errorf("container config overlay scratch: %w", err)
		}
		seedOverlay(filepath.Join(projectDir, rel), host)
		mounts = append(mounts, Mount{Host: host, Container: filepath.Join(projectDir, rel)})
	}
	return mounts, nil
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
	return len(buildSources(c.profile, "", c.baseContainerfile)) == 0
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
func runAsIsIdentityProblem(rt ContainerRuntime, id imageIdentity) string {
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

// containerWorkspace is the container policy's workspace: Dir() is the live
// project directory (identical-path mounted into the container) and Cleanup()
// removes the host-side scratch tree (socket dir + config overlays). The container
// is killed via the client. extraEnv/extraMounts carry the resolved auth env +
// credential/overlay mounts threaded into the run spec at SpawnClient.
type containerWorkspace struct {
	dir         string            // identical-path project dir (cwd + bind-mount target)
	scratchRoot string            // host scratch tree removed by Cleanup
	socketDir   string            // scratchRoot/sock — go-plugin's unix-socket temp dir
	extraEnv    []string          // scoped auth env (passthrough)
	extraMounts []Mount           // auth credential mounts + config overlays
	authMode    containerAuthMode // how auth was resolved (diagnostics; no secrets)
	agentID     string
}

// Dir returns the identical-path project directory (the container mounts it there
// so cwd + .git resolve unchanged; the caller threads it into RunOptions.WorkDir).
func (w *containerWorkspace) Dir() string { return w.dir }

// Cleanup removes the host scratch tree. Idempotent — safe to call once after the
// run's client is killed. A removal failure is surfaced by warnCleanupResidue
// (callers discard the returned error by contract).
func (w *containerWorkspace) Cleanup() error {
	if w.scratchRoot == "" {
		return nil
	}
	dir := w.scratchRoot
	w.scratchRoot = ""
	if err := os.RemoveAll(dir); err != nil {
		warnCleanupResidue("container scratch", dir, err)
		return fmt.Errorf("remove container scratch: %w", err)
	}
	return nil
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
func runtimeName(rt ContainerRuntime) string {
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
