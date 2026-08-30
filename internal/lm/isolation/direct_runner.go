package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// StartRunner launches the engine runner INSIDE a container via a plain
// docker/podman `run` — the queer-shrug Phase 1 spawn half that removes
// go-plugin from the delegated container spawn. The in-container command is
// `ctxloom llm host <backend>` (NOT `llm serve`): it hosts the engine
// in-process via EngineHost and never runs plugin.Serve, so the container
// opens NO plugin listener, publishes NO port, and gets NO socket mount or
// PLUGIN_*/magic-cookie env — closing the mauve-state peer-container hole for
// this path. Everything the workspace prepared that DOES matter — the
// session-state/auth/overlay/gitdir mounts (cw.extraMounts) and the scoped
// auth env (cw.extraEnv) — is preserved. The per-spawn runner env crosses as
// bare `-e NAME` with values on the run PROCESS env (never the world-readable
// argv). Readiness is the coordinator's awaitRunner, not observed here.
func (c Container) StartRunner(_ context.Context, backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (*RunnerHandle, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return nil, fmt.Errorf("container start-runner: unexpected workspace %T (expected a container workspace)", ws)
	}
	if verbosity > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container runner (docker-direct, no plugin transport) auth via %s\n", cw.authMode)
	}
	name := containerName(cw.agentID)
	// Same reason as the plugin-transport spawn in runtime.go: this name is
	// the no-tmux fallback's only handle, is randomly suffixed, and dies with
	// the container. Unconditional on purpose.
	fmt.Fprintf(os.Stderr, "ctxloom: container %s (watch: docker logs -f %s)\n", name, name)
	spec := c.buildRunnerSpec(backendName, label, name, cw, spawnEnv)
	return startDirectRunner(c.runtime, spec, spawnEnv)
}

// buildRunnerSpec assembles the RunSpec for one docker-direct engine runner —
// the buildRunSpec sibling with the plugin-transport pieces removed. Env = the
// fixed container base env (IS_SANDBOX) + the workspace's scoped auth/TERM/
// git-identity env (cw.extraEnv) + the per-spawn runner env as BARE NAMES.
// Mounts = the identical-path project mount + the workspace's own mounts
// (auth credential mounts, config overlays, gitdir mirror, and — critically —
// the session-state mounts, §6.4). NO socket-dir mount, NO published port, NO
// curated handshake env. Pure and deterministic so the render is unit-testable
// without a container.
func (c Container) buildRunnerSpec(backendName, label, name string, cw *containerWorkspace, spawnEnv map[string]string) RunSpec {
	command := []string{c.binaryPath, "llm", "host", backendName}
	if label != "" {
		command = append(command, "--label", label)
	}
	workDir := c.runtime.mapper().toContainer(cw.dir)

	env := append([]string(nil), containerBaseEnv...)
	env = append(env, cw.extraEnv...)
	// The per-spawn runner env crosses as bare names (renderRunSpec's `-e
	// <name>` form); the values ride the run-process env (startDirectRunner),
	// never this argv.
	names := make([]string, 0, len(spawnEnv))
	for k := range spawnEnv {
		names = append(names, k)
	}
	sort.Strings(names)
	env = append(env, names...)

	mounts := append([]Mount{
		// Project mount: cwd + .git resolve unchanged under the identity mapper.
		{Host: cw.dir, Container: workDir},
	}, cw.extraMounts...)

	// No socket-dir mount, no published port, no curated handshake env — the
	// transport-free spec that removes go-plugin from this spawn.
	return RunSpec{
		Image:   c.image,
		Name:    name,
		WorkDir: workDir,
		Home:    c.home,
		Command: command,
		Env:     env,
		Mounts:  mounts,
		// nil on every production run — set only when the isolation probe's
		// dedicated env var is present (traceProbeFromEnv). This is the sole
		// env→field bridge that turns a run into a read-observing probe run;
		// the SYS_PTRACE grant it enables is gated on this field in
		// renderRunSpec, unreachable otherwise. See TraceProbe.
		Trace: traceProbeFromEnv(),
	}
}

// runnerWaitDelay bounds the delay Wait will tolerate between the `run`
// process exiting and its stderr pipe closing. Without it, a container
// descendant that inherits and holds that pipe wedges Wait FOREVER, and a
// wedged Wait is an unreaped (defunct) child for this process's whole
// lifetime — see reapRunProcess.
const runnerWaitDelay = 10 * time.Second

// startDirectRunner starts `rt.Binary() rt.RunArgs(spec)…` as a foreground
// process (NO go-plugin handshake), capturing stderr into a bounded ring, and
// returns a RunnerHandle. The per-spawn env values ride the run PROCESS env so
// they never enter the world-readable argv. Kill force-removes the container
// (reusing the same remove-with-timeout + removeReportsGone logic
// containerRunner.Kill uses) then signals our own `run` CLI; the reaper
// goroutine started here Waits it exactly once, and RunnerHandle.Wait reads
// that one outcome (surfacing the stderr tail on failure).
func startDirectRunner(rt Runtime, spec RunSpec, spawnEnv map[string]string) (*RunnerHandle, error) {
	cmd := exec.Command(rt.Binary(), rt.RunArgs(spec)...)
	if len(spawnEnv) > 0 {
		kv := make([]string, 0, len(spawnEnv))
		for k, v := range spawnEnv {
			kv = append(kv, k+"="+v)
		}
		sort.Strings(kv)
		cmd.Env = append(os.Environ(), kv...)
	}
	// `docker run` here is ATTACHED (no -d), so this ring is fed by the
	// CONTAINER's own stderr AS IT STREAMS — the in-container `ctxloom llm
	// host` runner's stderr, and through it the engine adapter's. That
	// streaming is what makes the capture survive teardown: Kill force-removes
	// the container, and `docker logs` after a force-remove is too late.
	ring := stderrtail.New(stderrtail.DefaultBytes)
	cmd.Stderr = ring
	cmd.WaitDelay = runnerWaitDelay
	// No stdin (keeps `docker run` off the host terminal); the container's
	// stdout carries no handshake on this path, so nothing reads it.
	if err := cmd.Start(); err != nil {
		if tail := ring.Tail(); tail != "" {
			return nil, fmt.Errorf("start %s runner container %q: %w (stderr tail: %s)", rt.Name(), spec.Name, err, tail)
		}
		return nil, fmt.Errorf("start %s runner container %q: %w", rt.Name(), spec.Name, err)
	}
	reaped := reapRunProcess(cmd)
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			removeContainer(context.Background(), rt, spec.Name)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		// Deliberately does NOT block on the reap: teardown must stay
		// bounded even against a wedged daemon, and the reaper goroutine
		// finishes on its own (cmd.WaitDelay bounds it).
	}
	wait := func() error {
		err := reaped()
		if err != nil {
			if tail := ring.Tail(); tail != "" {
				return fmt.Errorf("runner container %q exited: %w (stderr tail: %s)", spec.Name, err, tail)
			}
			return fmt.Errorf("runner container %q exited: %w", spec.Name, err)
		}
		return nil
	}
	return &RunnerHandle{Name: spec.Name, Kill: kill, Wait: wait, StderrTail: ring.Tail}, nil
}

// reapRunProcess Waits a started *exec.Cmd exactly once, in the background,
// and returns an accessor that blocks for that one outcome.
//
// This is the fix for a PID leak (defunct `[docker]` zombie children
// accumulating by the hundreds within under an hour). A Start()ed process is
// only released from the
// kernel process table by Wait, and NOTHING in the production call graph ever
// called RunnerHandle.Wait — Kill force-removed the container and signalled
// the `run` CLI, then dropped it. Every launch attempt therefore leaked one
// zombie, and a retry loop turned a container-launch bug into slow PID
// exhaustion.
//
// A background Wait, not a SIGCHLD reaper: os/exec keeps its own per-Cmd
// bookkeeping and REQUIRES Cmd.Wait to collect a child's status. A
// process-wide SIGCHLD handler calling wait4 would race every other
// exec.Cmd in this process (the fs probe, `docker rm`, every host runner)
// and steal their exit statuses, turning one leak into a class of
// "wait: no child processes" failures. The surrounding code is already
// Cmd-per-process (HostRunner, containerRunner, probeExec) so per-Cmd
// reaping is also the shape that fits.
func reapRunProcess(cmd *exec.Cmd) func() error {
	done := make(chan struct{})
	var waitErr error
	go func() {
		defer close(done)
		waitErr = cmd.Wait()
	}()
	return func() error {
		<-done
		return waitErr
	}
}

// removeContainer force-removes a named container under our OWN bounded timeout
// (a wedged daemon must never hang teardown), surfacing a real leak LOUDLY —
// the shared remove-with-timeout + removeReportsGone logic containerRunner.Kill
// and the docker-direct RunnerHandle.Kill both use. A missing name/binary
// (a host-style runner) is a no-op. A racing --rm reporting already-gone is
// teardown success, not a leak.
func removeContainer(ctx context.Context, rt Runtime, name string) {
	if name == "" || rt == nil || rt.Binary() == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, containerRemoveTimeout)
	defer cancel()
	if _, err := probeExec(cctx, rt.Binary(), rt.RemoveArgs(name)); err != nil && !removeReportsGone(err) {
		clidiag.Warn("ctxloom",
			"container %q may still be running after teardown (%v) — the %s daemon did not confirm removal; it holds this run's workspace, remove it manually with `%s %s`",
			name, err, rt.Name(), rt.Binary(), strings.Join(rt.RemoveArgs(name), " "))
	}
}
