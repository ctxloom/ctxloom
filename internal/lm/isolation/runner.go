package isolation

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin/runner"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// containerRunner implements go-plugin's runner.Runner by launching the plugin
// server INSIDE a container via a ContainerRuntime. It mirrors go-plugin's own
// cmdrunner.CmdRunner shape — a thin lifecycle wrapper over an *exec.Cmd — except
// the process it manages is `docker/podman run …` and Kill force-removes the
// container. Stdout() is the CONTAINER's stdout, from which go-plugin reads the
// plugin handshake line; the embedded containerAddrTranslator maps the plugin's
// announced unix-socket path from the container namespace to the host bind mount.
type containerRunner struct {
	runtime ContainerRuntime
	name    string
	cmd     *exec.Cmd

	stdout io.ReadCloser
	stderr io.ReadCloser

	containerAddrTranslator
}

// Ensure containerRunner satisfies the go-plugin runner interface.
var _ runner.Runner = (*containerRunner)(nil)

// newContainerRunner builds the runner for one plugin container. hostSocketDir is
// the host directory go-plugin created for the unix socket (passed to the
// RunnerFunc); containerSocketDir is the fixed in-container path it is bind-mounted
// to — the two seed the AddrTranslator's container↔host path swap.
func newContainerRunner(rt ContainerRuntime, spec RunSpec, hostSocketDir, containerSocketDir string) (*containerRunner, error) {
	cmd := exec.Command(rt.Binary(), rt.RunArgs(spec)...)
	// No Stdin: go-plugin (this version) does not watch stdin for parent-death, so
	// the plugin runs until the container is removed by Kill. Leaving Stdin nil
	// keeps `docker run` from holding the host's terminal.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("container stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("container stderr pipe: %w", err)
	}
	return &containerRunner{
		runtime: rt,
		name:    spec.Name,
		cmd:     cmd,
		stdout:  stdout,
		stderr:  stderr,
		containerAddrTranslator: containerAddrTranslator{
			hostSocketDir:      hostSocketDir,
			containerSocketDir: containerSocketDir,
		},
	}, nil
}

// Start launches the container process (the plugin comes up inside it).
func (r *containerRunner) Start(_ context.Context) error {
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start %s container: %w", r.runtime.Name(), err)
	}
	return nil
}

// Wait blocks until the container process exits.
func (r *containerRunner) Wait(_ context.Context) error { return r.cmd.Wait() }

// Kill tears the container down: force-remove by name (stops + removes it, which
// also ends the --rm `run` process), then best-effort kill the run process. Both
// errors are swallowed — teardown must never panic a run, and a racing --rm makes
// the remove report an already-gone container (CLAUDE.md fault tolerance).
func (r *containerRunner) Kill(ctx context.Context) error {
	if r.name != "" && r.runtime.Binary() != "" {
		_ = exec.CommandContext(ctx, r.runtime.Binary(), r.runtime.RemoveArgs(r.name)...).Run()
	}
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	return nil
}

// Stdout is the container's stdout — go-plugin negotiates the handshake here.
func (r *containerRunner) Stdout() io.ReadCloser { return r.stdout }

// Stderr forwards container/plugin logs to the host logger.
func (r *containerRunner) Stderr() io.ReadCloser { return r.stderr }

// Name is a human-friendly identifier (the container name).
func (r *containerRunner) Name() string { return r.name }

// ID is the unique running-plugin identifier (the container name; teardown targets
// it via --name).
func (r *containerRunner) ID() string { return r.name }

// Diagnose returns best-effort help when the plugin failed to negotiate — almost
// always a missing image, an unrunnable in-container binary, or a socket-mount
// permission problem.
func (r *containerRunner) Diagnose(_ context.Context) string {
	return fmt.Sprintf("plugin container %q (%s) did not negotiate the go-plugin handshake: "+
		"check the image exists, its ctxloom is executable, and the socket dir is bind-mounted",
		r.name, r.runtime.Name())
}

// containerAddrTranslator maps unix-socket paths between the container namespace
// (where the plugin creates and announces its socket, under containerSocketDir)
// and the host namespace (the bind-mounted hostSocketDir the host process dials).
// This is the go-plugin AddrTranslator that solves the socket-across-container
// problem WITHOUT identical-path socket mounts: the two dirs are the same bind
// mount, so translation is a prefix swap and the socket file is visible on both.
type containerAddrTranslator struct {
	hostSocketDir      string
	containerSocketDir string
}

// PluginToHost maps an address the plugin announced (container namespace) to the
// host namespace before the host connects to it.
func (t containerAddrTranslator) PluginToHost(network, addr string) (string, string, error) {
	return network, swapPrefix(addr, t.containerSocketDir, t.hostSocketDir), nil
}

// HostToPlugin maps a host address to the container namespace before it is sent to
// the plugin (e.g. go-plugin broker sockets the plugin dials back).
func (t containerAddrTranslator) HostToPlugin(network, addr string) (string, string, error) {
	return network, swapPrefix(addr, t.hostSocketDir, t.containerSocketDir), nil
}

// swapPrefix replaces a leading directory prefix, preserving the socket basename.
// A path outside the mounted dir is returned unchanged (defensive: only the
// plugin's own socket paths live under the mount).
func swapPrefix(path, from, to string) string {
	if from == "" || to == "" {
		return path
	}
	rel, err := filepath.Rel(from, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.Join(to, rel)
}

// containerRunnerFunc builds the go-plugin RunnerFunc that launches the plugin in
// a container. go-plugin calls it with the host socket dir it created (hostTmpDir);
// we bind-mount that to containerSocketDir, override PLUGIN_UNIX_SOCKET_DIR to the
// container path (go-plugin set it to the host path on the `run` process env,
// which the plugin inside the container never sees), and hand back a runner whose
// AddrTranslator maps the socket path back. extraEnv (scoped auth passthrough) and
// extraMounts (auth credential mounts + config overlays) are threaded into the run
// spec on top of the handshake env + project/socket mounts.
//
// go-plugin RunnerFunc+AddrTranslator: canonical plugin-in-container transport; do
// not hand-roll docker-exec + socket plumbing.
func containerRunnerFunc(rt ContainerRuntime, image, name, projectDir, home string, command []string, containerSocketDir string, extraEnv []string, extraMounts []Mount) pb.ContainerRunnerFunc {
	return func(_ hclog.Logger, cmd *exec.Cmd, hostSocketDir string) (runner.Runner, error) {
		spec := buildRunSpec(image, name, projectDir, home, command, containerSocketDir, hostSocketDir, cmd.Env, extraEnv, extraMounts)
		return newContainerRunner(rt, spec, hostSocketDir, containerSocketDir)
	}
}

// containerBaseEnv is the fixed env every plugin container gets, independent of
// auth. IS_SANDBOX=1 tells the engine it runs inside a real sandbox so it permits
// approval-bypass as root — the container runs as (mapped) root, and claude
// otherwise REFUSES `--dangerously-skip-permissions` under uid 0 ("cannot be used
// with root/sudo privileges"). This is the runtime-side complement of the policy's
// ApprovalsBypass: the same "the container is the boundary" decision. Harmless to
// engines that ignore it.
var containerBaseEnv = []string{"IS_SANDBOX=1"}

// buildRunSpec assembles the RunSpec for one plugin container from the launch
// parameters. Env = the fixed container base env + the curated go-plugin handshake
// vars (from hostEnv, socket dir overridden to the container path) + the scoped
// auth extraEnv. Mounts = the identical-path project mount (cwd + .git resolve
// unchanged) + the socket-dir mount + extraMounts (auth credential mounts + config
// overlays). Pure and deterministic so the mount/env wiring is unit-testable
// without a container.
func buildRunSpec(image, name, projectDir, home string, command []string, containerSocketDir, hostSocketDir string, hostEnv, extraEnv []string, extraMounts []Mount) RunSpec {
	env := append(append([]string(nil), containerBaseEnv...), containerHandshakeEnv(hostEnv, containerSocketDir)...)
	env = append(env, extraEnv...)
	mounts := append([]Mount{
		// Identical-path project mount: cwd + .git gitdir resolve unchanged.
		{Host: projectDir, Container: projectDir},
		// The unix-socket dir go-plugin created, mounted to the container path.
		{Host: hostSocketDir, Container: containerSocketDir},
	}, extraMounts...)
	return RunSpec{
		Image:   image,
		Name:    name,
		WorkDir: projectDir,
		Home:    home,
		Command: command,
		Env:     env,
		Mounts:  mounts,
	}
}

// containerHandshakeEnv curates the environment that crosses into the container:
// ONLY the go-plugin handshake vars (the magic cookie + PLUGIN_* — protocol
// versions, ports, mTLS cert, mux flag), never the host's full environment. It
// OVERRIDES PLUGIN_UNIX_SOCKET_DIR to the container path so the plugin creates its
// socket in the bind-mounted dir. Anything else (host paths, secrets) is dropped;
// a real engine's auth is a separate, deliberate follow-up (see the container
// policy docs).
func containerHandshakeEnv(cmdEnv []string, containerSocketDir string) []string {
	var out []string
	for _, kv := range cmdEnv {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case key == pb.HandshakeConfig.MagicCookieKey:
			out = append(out, kv)
		case key == "PLUGIN_UNIX_SOCKET_DIR":
			// Override: the host path go-plugin set is meaningless in the container.
			out = append(out, "PLUGIN_UNIX_SOCKET_DIR="+containerSocketDir)
		case strings.HasPrefix(key, "PLUGIN_"):
			out = append(out, kv)
		}
	}
	return out
}
