// Package containercell is a HERMETIC container cell for tests: it runs a real
// ctxloom binary inside a real container, against a bind-mounted host
// directory, and lets the test observe the result from the HOST side of that
// mount.
//
// WHY IT EXISTS. Every container-runtime claim in the acceptance suite was
// unassertable, for a stack of reasons that had to be removed in order:
//
//   - the suite is daemon-free by design, so nothing in it launched a
//     container at all;
//   - what did exist was the DEGRADE contract (j9's "mock-container" agent
//     proves a requested container that cannot launch ABORTS) — a different
//     claim from "a container run delivered these bytes";
//   - the fixture image had no engine, and the mock backend is COMPILED INTO
//     ctxloom, so a container had nothing that could deliver;
//   - and until mock started materialising through the shared cells seam
//     (MOCK_CONTEXT.md + .mock/skills/), a container cell would have had
//     nothing to OBSERVE even with a daemon, an image and a mount.
//
// The alternative — deliver on the host and assert a host path — passes every
// container row without crossing the process boundary the rows name. That is a
// vacuous green, which in a suite whose subject is the silent no-op is worse
// than red.
//
// # THE THREE DESIGN DECISIONS
//
// 1. THE IMAGE IS `FROM scratch`. Hermetic means no network, and every other
// supply route fetches: an alpine/debian base is a registry pull, and an
// in-container `go build` needs a module cache. A statically linked
// (CGO_ENABLED=0) ctxloom needs no libc, no certs, no /etc/passwd and no
// shell, so the whole image is one COPY over an empty base and `docker build`
// touches the network zero times. It also makes the cell identical under
// docker and podman, which have separate image stores and would otherwise each
// need their own warm cache.
//
// 2. MOUNTS ARE IDENTICAL-PATH. The host directory is bind-mounted at exactly
// its own absolute path, which is the shape ctxloom's own container isolation
// uses (a workspace mounted where the editor already looks). It also means the
// test can hand the in-container ctxloom the SAME absolute paths it would use
// on the host — no path translation layer to get wrong, and an assertion that
// reads the host side is reading the very bytes the container wrote.
//
// 3. THE IN-CONTAINER UID IS CHOSEN PER RUNTIME, and this is the part that
// actually differs across the matrix. Under ROOTLESS docker/podman, container
// uid 0 is the invoking user on the host filesystem, so running as root writes
// host files owned by the invoker. Under ROOTFUL docker, container uid 0 is
// host root, so the same run leaves root-owned droppings in the user's tree —
// the exact bug class the PUID/PGID entrypoint remap exists for. The cell
// therefore runs as root under a rootless runtime and as --user <hostuid>:
// <hostgid> under a rootful one, and callers ASSERT ownership afterwards, so
// the rule is checked rather than trusted.
package containercell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The runtime vocabulary. These are the names a lane declares in
// CTXLOOM_REQUIRE_RUNTIMES (dockergate.EnvRequireRuntimes), so they are a
// stable contract with CI config, not an internal label.
//
// docker-rootful and docker-rootless are DIFFERENT runtimes here, not one
// runtime with a flag, because they differ in the property this cell is about:
// who owns the files that come out of the mount. A host is one or the other and
// never both, which is why the matrix is asymmetric by environment — each
// runner covers what it has, and no runner covers everything.
const (
	DockerRootful  = "docker-rootful"
	DockerRootless = "docker-rootless"
	Podman         = "podman"
)

// Names returns the probe vocabulary, sorted. Pass it to
// dockergate.ValidateRequiredRuntimes so a typo in CTXLOOM_REQUIRE_RUNTIMES is
// loud instead of silently covering nothing.
func Names() []string { return []string{DockerRootful, DockerRootless, Podman} }

// Runtime is one probed container runtime.
type Runtime struct {
	// Name is one of the three constants above.
	Name string
	// Command is the CLI that drives it ("docker" or "podman").
	Command string
	// Available reports whether this runtime is reachable on this host.
	Available bool
	// Detail says what was found — the daemon version when available, the
	// reason when not. It goes into skip messages, so "what did not run" is
	// always accompanied by "and why".
	Detail string
	// RootMapsToInvoker reports whether uid 0 INSIDE the container is the
	// invoking host user on the shared filesystem. True for rootless
	// docker/podman, false for a rootful daemon. It decides the --user flag;
	// see the package doc's decision 3.
	RootMapsToInvoker bool
}

// Detect probes all three runtimes. It never returns an error: an unreachable
// runtime is data (Available=false with a Detail), because "podman is missing"
// is a normal outcome the gate decides about, not a test failure.
//
// Probing is by the runtime's OWN `info` output rather than by socket paths:
// DOCKER_HOST / DOCKER_CONTEXT can point the CLI anywhere, and a socket that
// exists is not a daemon that answers.
func Detect(ctx context.Context) []Runtime {
	docker := probeDocker(ctx)
	return []Runtime{docker.asRootful(), docker.asRootless(), probePodman(ctx)}
}

// Lookup returns the probe result for one runtime name.
func Lookup(ctx context.Context, name string) (Runtime, error) {
	for _, r := range Detect(ctx) {
		if r.Name == name {
			return r, nil
		}
	}
	return Runtime{}, fmt.Errorf("unknown container runtime %q; known: %s", name, strings.Join(Names(), ", "))
}

// dockerProbe is the single `docker info` result, split into the two named
// runtimes. One probe, two answers: a reachable rootless daemon means
// docker-rootless is available AND docker-rootful is not, and asserting that
// exclusivity in one place stops the two from ever both reporting true.
type dockerProbe struct {
	reachable bool
	rootless  bool
	detail    string
}

func (p dockerProbe) asRootful() Runtime {
	r := Runtime{Name: DockerRootful, Command: "docker", RootMapsToInvoker: false}
	switch {
	case !p.reachable:
		r.Detail = p.detail
	case p.rootless:
		r.Detail = "the reachable docker daemon is ROOTLESS (" + p.detail + "), so this host cannot exercise the rootful ownership axis"
	default:
		r.Available, r.Detail = true, p.detail
	}
	return r
}

func (p dockerProbe) asRootless() Runtime {
	r := Runtime{Name: DockerRootless, Command: "docker", RootMapsToInvoker: true}
	switch {
	case !p.reachable:
		r.Detail = p.detail
	case !p.rootless:
		r.Detail = "the reachable docker daemon is ROOTFUL (" + p.detail + "), so this host cannot exercise the rootless mapping"
	default:
		r.Available, r.Detail = true, p.detail
	}
	return r
}

func probeDocker(ctx context.Context) dockerProbe {
	if _, err := exec.LookPath("docker"); err != nil {
		return dockerProbe{detail: "no `docker` on PATH"}
	}
	// SecurityOptions carries "name=rootless" on a rootless daemon. Asking the
	// daemon (rather than inspecting DOCKER_HOST) is what makes this correct
	// under a docker context, a DOCKER_HOST override, or a socket-forwarding
	// devcontainer.
	out, err := runProbe(ctx, "docker", "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return dockerProbe{detail: fmt.Sprintf("`docker info` failed (daemon unreachable): %v", err)}
	}
	ver, verr := runProbe(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if verr != nil {
		ver = "unknown version"
	}
	return dockerProbe{
		reachable: true,
		rootless:  strings.Contains(out, "name=rootless"),
		detail:    "docker " + strings.TrimSpace(ver),
	}
}

func probePodman(ctx context.Context) Runtime {
	r := Runtime{Name: Podman, Command: "podman"}
	if _, err := exec.LookPath("podman"); err != nil {
		r.Detail = "no `podman` on PATH"
		return r
	}
	rootless, err := runProbe(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		r.Detail = fmt.Sprintf("`podman info` failed: %v", err)
		return r
	}
	ver, verr := runProbe(ctx, "podman", "version", "--format", "{{.Version}}")
	if verr != nil {
		ver = "unknown version"
	}
	r.Available = true
	r.RootMapsToInvoker = strings.TrimSpace(rootless) == "true"
	r.Detail = "podman " + strings.TrimSpace(ver)
	if r.RootMapsToInvoker {
		r.Detail += " (rootless)"
	} else {
		r.Detail += " (rootful)"
	}
	return r
}

// probeTimeout bounds a probe. An unreachable daemon usually fails fast, but a
// hung socket must not wedge the whole suite behind a gate that has not decided
// anything yet.
const probeTimeout = 20 * time.Second

func runProbe(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// --- the binary and the image ----------------------------------------------

// EnvBinary points the cell at a prebuilt static linux/amd64 ctxloom instead of
// building one. It exists for a CI lane that already produced the artifact; a
// developer never sets it.
const EnvBinary = "CTXLOOM_CELL_BINARY"

// prebuiltBinary is captured at package init for the reason dockergate's
// `required` is: it is in testsupport.EnvKeys, so Isolate (and the acceptance
// suite's TestMain) CLEARS it, and a lazy read would silently rebuild a binary
// the lane had already produced — or, worse, read a half-cleared environment
// and disagree with itself between two calls.
var prebuiltBinary = os.Getenv(EnvBinary)

var (
	binOnce sync.Once
	binPath string
	binErr  error

	imgMu   sync.Mutex
	imgDone = map[string]error{}
)

// ImageTag is the cell's image. One tag per runtime COMMAND, because docker and
// podman have separate image stores and a build into one is invisible to the
// other.
const ImageTag = "ctxloom-cell:latest"

// RunPolicyImageTag is the cell image built for CTXLOOM'S OWN container
// isolation to launch, rather than for the cell to launch itself. It is a
// SEPARATE tag from ImageTag for one reason: the isolation policy runs
// `<defaultContainerBinary> llm serve <backend>`, a fixed in-image path
// (/usr/local/bin/ctxloom) that is not the cell's own /ctxloom — so the two
// images differ in exactly one COPY destination and must not share a tag that
// would make whichever built last win.
const RunPolicyImageTag = "ctxloom-cell-runpolicy:latest"

// RunPolicyBinary is where RunPolicyImageTag carries ctxloom: the path
// internal/lm/isolation's container policy hardcodes as the in-container
// ctxloom (its defaultContainerBinary). The literal is duplicated here rather
// than imported because the isolation package must not depend on testsupport
// in the other direction; the isolation-side test asserts the two agree, so a
// drift is loud rather than a container that starts and does nothing.
const RunPolicyBinary = "/usr/local/bin/ctxloom"

// Binary builds (once per process) the static linux/amd64 ctxloom the cell runs
// in-container, and returns its path.
//
// CGO_ENABLED=0 is not an optimisation: the `FROM scratch` image has no libc,
// so a dynamically linked binary would not start, and the failure would look
// like the container silently doing nothing — this codebase's characteristic
// bug, reproduced inside the machinery built to catch it. GOWORK=off matches
// the release build (github.com/ctxloom/* resolve from the go.mod pins, never
// from sibling checkouts).
//
// The output path is stable across processes so a repeat run reuses it, and the
// write is a rename onto that path so two concurrent test binaries cannot
// observe a half-written file.
func Binary(ctx context.Context) (string, error) {
	binOnce.Do(func() { binPath, binErr = buildBinary(ctx) })
	return binPath, binErr
}

func buildBinary(ctx context.Context) (string, error) {
	if p := prebuiltBinary; p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q does not exist: %w", EnvBinary, p, err)
		}
		return p, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	outDir := filepath.Join(os.TempDir(), "ctxloom-cell-bin")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create cell binary dir: %w", err)
	}
	final := filepath.Join(outDir, "ctxloom")
	staging, err := os.MkdirTemp(outDir, "build-*")
	if err != nil {
		return "", fmt.Errorf("create cell build staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	tmp := filepath.Join(staging, "ctxloom")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tmp, "./cmd/ctxloom")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOWORK=off")
	if out, berr := cmd.CombinedOutput(); berr != nil {
		return "", fmt.Errorf("building the in-container ctxloom (CGO_ENABLED=0 GOOS=linux GOARCH=amd64) failed: %w\n%s", berr, out)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("install cell binary: %w", err)
	}
	return final, nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod. Tests run with their package as cwd, so the module root is never the
// cwd and always an ancestor.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %q: the cell cannot find the module to build ctxloom from", dir)
		}
		dir = parent
	}
}

// EnsureImage builds the cell image for this runtime's command, once per
// process per command. The build context is a temp dir holding the static
// binary and a two-line Containerfile, so it pulls nothing.
func (r Runtime) EnsureImage(ctx context.Context) error {
	return r.ensureImage(ctx, ImageTag, InContainerBinary, false)
}

// EnsureRunPolicyImage builds the cell image CTXLOOM'S OWN container isolation
// can launch — the one a test needs when the process that builds and starts the
// container is the product's Container policy rather than the cell itself.
//
// It differs from EnsureImage in exactly two ways, both of them requirements
// the policy imposes on any image it is handed:
//
//  1. ctxloom sits at RunPolicyBinary, the fixed in-image path the policy
//     execs (`<binary> llm serve <backend>`). An image carrying ctxloom
//     anywhere else produces a container that starts and immediately dies with
//     no such file — indistinguishable, from the host, from a plugin that came
//     up and answered nothing.
//  2. A `cat` sits at /bin/cat, because the policy REFUSES to prepare a
//     workspace until its own shared-filesystem probe has run `cat
//     /probe/marker` in this same image and read the marker back
//     (isolation.sharedFSProbe). `FROM scratch` has no cat, so without this the
//     gate fails with "the probe container did not run" and nothing under test
//     ever executes. It is a ~10-line static Go program built here rather than
//     a busybox layer: a busybox base is a registry pull, and the cell's
//     hermeticity (package doc, decision 1) is what makes it usable on a runner
//     with no network.
func (r Runtime) EnsureRunPolicyImage(ctx context.Context) error {
	return r.ensureImage(ctx, RunPolicyImageTag, RunPolicyBinary, true)
}

// ensureImage builds one image variant, once per process per (command, tag,
// binary path, probe-cat): docker and podman have separate image stores, and
// two tags off one store are two images.
func (r Runtime) ensureImage(ctx context.Context, tag, binaryPath string, withProbeCat bool) error {
	imgMu.Lock()
	defer imgMu.Unlock()
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%t", r.Command, tag, binaryPath, withProbeCat)
	if err, ok := imgDone[key]; ok {
		return err
	}
	err := r.buildImage(ctx, tag, binaryPath, withProbeCat)
	imgDone[key] = err
	return err
}

func (r Runtime) buildImage(ctx context.Context, tag, binaryPath string, withProbeCat bool) error {
	bin, err := Binary(ctx)
	if err != nil {
		return err
	}
	buildCtx, err := os.MkdirTemp("", "ctxloom-cell-img-*")
	if err != nil {
		return fmt.Errorf("create image build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(buildCtx) }()

	src, err := os.ReadFile(bin) //nolint:gosec // a path this package just built
	if err != nil {
		return fmt.Errorf("read cell binary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildCtx, "ctxloom"), src, 0o755); err != nil { //nolint:gosec // must be executable in-image
		return fmt.Errorf("stage cell binary: %w", err)
	}
	containerfile := "FROM scratch\nCOPY ctxloom " + binaryPath + "\n"
	if withProbeCat {
		catBin, cerr := probeCat(ctx)
		if cerr != nil {
			return cerr
		}
		catSrc, rerr := os.ReadFile(catBin) //nolint:gosec // a path this package just built
		if rerr != nil {
			return fmt.Errorf("read probe cat: %w", rerr)
		}
		if werr := os.WriteFile(filepath.Join(buildCtx, "cat"), catSrc, 0o755); werr != nil { //nolint:gosec // must be executable in-image
			return fmt.Errorf("stage probe cat: %w", werr)
		}
		containerfile += "COPY cat " + probeCatPath + "\n"
	}
	if err := os.WriteFile(filepath.Join(buildCtx, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		return fmt.Errorf("stage Containerfile: %w", err)
	}
	cmd := exec.CommandContext(ctx, r.Command, "build", "-t", tag, "-f", filepath.Join(buildCtx, "Containerfile"), buildCtx)
	if out, berr := cmd.CombinedOutput(); berr != nil {
		return fmt.Errorf("%s build of the cell image failed: %w\n%s", r.Command, berr, out)
	}
	return nil
}

// probeCatPath is where the run-policy image carries its `cat`. /bin is on the
// PATH docker/podman default an image with no ENV of its own to, which is how a
// `FROM scratch` image resolves a bare `cat` at all.
const probeCatPath = "/bin/cat"

// probeCatSource is the whole of that cat: read the named file, write it to
// stdout, and fail loudly otherwise. It is deliberately NOT a general cat — the
// only caller is isolation.probeOneRoot's `cat /probe/marker`, and a stand-in
// that silently printed nothing for a missing file would turn a real sharing
// mismatch into an empty read the probe already treats as the
// docker-outside-of-docker signature, i.e. the right verdict for the wrong
// reason.
const probeCatSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "probe cat: want exactly one file argument")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe cat:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(b); err != nil {
		fmt.Fprintln(os.Stderr, "probe cat:", err)
		os.Exit(1)
	}
}
`

var (
	catOnce sync.Once
	catPath string
	catErr  error
)

// probeCat builds (once per process) the static linux/amd64 cat the run-policy
// image carries. It compiles in a temp module of its own — no imports beyond
// the standard library, so the build reaches no proxy and no network — with the
// same CGO_ENABLED=0 static settings the ctxloom binary uses, for the same
// reason: `FROM scratch` has no libc to link against.
func probeCat(ctx context.Context) (string, error) {
	catOnce.Do(func() { catPath, catErr = buildProbeCat(ctx) })
	return catPath, catErr
}

func buildProbeCat(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "ctxloom-cell-cat-*")
	if err != nil {
		return "", fmt.Errorf("create probe cat dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(probeCatSource), 0o644); err != nil {
		return "", fmt.Errorf("stage probe cat source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ctxloomcellcat\n\ngo 1.21\n"), 0o644); err != nil {
		return "", fmt.Errorf("stage probe cat go.mod: %w", err)
	}
	out := filepath.Join(dir, "cat")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOWORK=off", "GOFLAGS=", "GOPROXY=off")
	if b, berr := cmd.CombinedOutput(); berr != nil {
		return "", fmt.Errorf("building the probe cat failed: %w\n%s", berr, b)
	}
	return out, nil
}

// InContainerBinary is where the cell image carries ctxloom. Absolute and
// short: `FROM scratch` has no PATH and no shell to resolve one.
const InContainerBinary = "/ctxloom"

// --- running ----------------------------------------------------------------

// Spec is one in-container ctxloom invocation.
type Spec struct {
	// Mounts are host directories bind-mounted READ-WRITE at their own
	// absolute paths (see the package doc's decision 2). Delivery targets must
	// be under one of them, or the container writes into its own ephemeral
	// layer and the host observes nothing — a silent no-op the caller's
	// payload assertion is what catches.
	Mounts []string
	// WorkDir is the in-container working directory, i.e. a host path under
	// one of Mounts.
	WorkDir string
	// Env is the in-container environment. `FROM scratch` inherits nothing, so
	// anything ctxloom reads (HOME above all) must be listed here.
	Env map[string]string
	// Args is the ctxloom argv, without the binary.
	Args []string
}

// Result is what the run did. Output is combined stdout+stderr: a caller
// asserting on delivered payload still needs the diagnostic when the payload is
// missing.
type Result struct {
	Argv     []string
	Output   string
	ExitCode int
}

// Run executes ctxloom inside a container of this runtime and returns what it
// did. A non-zero exit is reported in Result, not as an error — the error
// return is for the cell itself failing (no image, no daemon), which is a
// different thing from the command under test failing.
//
// --network=none is deliberate: the cell claims hermeticity, and a cell that
// could reach the network would let a test pass on something it fetched.
func (r Runtime) Run(ctx context.Context, spec Spec) (Result, error) {
	if !r.Available {
		return Result{}, fmt.Errorf("runtime %q is not available: %s", r.Name, r.Detail)
	}
	// Spec validation precedes the image build, and the order is load-bearing
	// twice over: a malformed spec must fail on its own terms rather than after
	// a minute of compiling, and these checks stay assertable on a machine with
	// no container runtime at all.
	if spec.WorkDir == "" {
		return Result{}, errors.New("cell spec has no WorkDir; a container with no working directory would resolve the project from nowhere")
	}
	if !underAny(spec.WorkDir, spec.Mounts) {
		return Result{}, fmt.Errorf("cell WorkDir %q is not under any mount %v: the container would run against an empty ephemeral layer and the host would observe nothing",
			spec.WorkDir, spec.Mounts)
	}
	if err := r.EnsureImage(ctx); err != nil {
		return Result{}, err
	}

	argv := []string{"run", "--rm", "--network=none"}
	if u := r.UserFlag(); u != "" {
		argv = append(argv, "--user", u)
	}
	for _, m := range spec.Mounts {
		argv = append(argv, "-v", m+":"+m)
	}
	argv = append(argv, "-w", spec.WorkDir)
	for _, k := range sortedKeys(spec.Env) {
		argv = append(argv, "-e", k+"="+spec.Env[k])
	}
	argv = append(argv, ImageTag, InContainerBinary)
	argv = append(argv, spec.Args...)

	cmd := exec.CommandContext(ctx, r.Command, argv...)
	out, err := cmd.CombinedOutput()
	res := Result{
		Argv:     append([]string{r.Command}, argv...),
		Output:   string(out),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return res, fmt.Errorf("%s run failed to start: %w\n%s", r.Command, err, out)
	}
	return res, nil
}

// UserFlag is the --user value this runtime needs so files written through the
// mount end up owned by the invoking host user, or "" to run as the image's
// default (root).
//
// Under a ROOTLESS runtime container-root already IS the invoker on the host
// filesystem, and passing --user <hostuid> would instead select an unmapped
// subuid that cannot even write the mount. Under a ROOTFUL daemon container-root
// is host root, and omitting the flag is precisely how a container run leaves
// root-owned files in a user's tree.
//
// This is the cell mirroring what the product's container entrypoint does with
// PUID/PGID. Callers still assert ownership afterwards: the rule is stated here
// and CHECKED there, because a rule that is only stated is how the ownership
// axis came to be untested in the first place.
func (r Runtime) UserFlag() string {
	if r.RootMapsToInvoker {
		return ""
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

func underAny(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- observing --------------------------------------------------------------

// AssertOwnedByInvoker returns an error unless path is owned by the user
// running the test. what names the artifact in the message.
func AssertOwnedByInvoker(path, what string) error {
	uid, gid, err := Owner(path)
	if err != nil {
		return fmt.Errorf("stat %s at %q: %w", what, path, err)
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		return fmt.Errorf("%s at %q is owned by %d:%d, but the host user is %d:%d — "+
			"a container wrote through the mount without mapping to the invoking user, "+
			"so these files are root-owned droppings in the user's tree (the PUID/PGID remap this axis exists to check)",
			what, path, uid, gid, os.Getuid(), os.Getgid())
	}
	return nil
}
