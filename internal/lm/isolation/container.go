package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Default container-policy parameters. The image is the REQUIRED agent image: the
// policy degrades to None when it is absent (the graceful rollout — until the
// production image ships, defaults.isolation:container becomes today's behaviour).
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
// below). Any inability to launch degrades to None via an error the caller
// catches — the LLM never blocks (CLAUDE.md).
//
// AUTH crosses the boundary deliberately and scoped (PrepareWorkspace → the
// profile's resolveAuth): the container gets the engine's scoped env passthrough
// (claude: ANTHROPIC_* when ANTHROPIC_API_KEY is set; kiro: KIRO_API_KEY) or the
// engine's credentials bind-mounted READ-ONLY into the fresh HOME (claude
// subscription OAuth). No resolvable auth → PrepareWorkspace errors → the caller
// degrades to None (the host session is already authenticated). Only the TRUSTED
// top-level run reaches this; low-trust fan-out auth (per-agent keys/budgets,
// T1.5) is a later concern.
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
		extraMounts: append(append([]Mount(nil), sc.auth.mounts...), overlays...),
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
}

// runEnv composes the per-run env threaded into the container spec: the scoped
// auth passthrough plus the host terminal description (TERM/COLORTERM), which
// the curated handshake env deliberately drops. Returns a fresh slice so
// callers never alias the scratch's fields.
func (sc containerScratch) runEnv() []string {
	return append(append([]string(nil), sc.auth.env...), sc.termEnv...)
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
	root, err := os.MkdirTemp("", "ctxloom-iso-")
	if err != nil {
		return containerScratch{}, fmt.Errorf("container scratch: %w", err)
	}
	socketDir := filepath.Join(root, "sock")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return containerScratch{}, fmt.Errorf("container socket scratch: %w", err)
	}
	return containerScratch{root: root, socketDir: socketDir, auth: auth, termEnv: hostTerminalEnv(os.Getenv)}, nil
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
	for _, key := range []string{"TERM", "COLORTERM"} {
		if v := getenv(key); v != "" {
			out = append(out, key+"="+v)
		}
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

	name := containerName(agentID)
	runnerFunc := containerRunnerFunc(c.runtime, c.image, name, workDir, c.home, command, c.socketDir, extraEnv, extraMounts)
	return pb.NewContainerClient(backendName, label, verbosity, runnerFunc, hostSocketDir)
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
// run's client is killed.
func (w *containerWorkspace) Cleanup() error {
	if w.scratchRoot == "" {
		return nil
	}
	dir := w.scratchRoot
	w.scratchRoot = ""
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove container scratch: %w", err)
	}
	return nil
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
