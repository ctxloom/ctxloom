package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ContainerRuntime is the pluggable container launcher — proper polymorphism, NOT
// an if/else over runtime names. Each implementation (Docker | Podman | Host)
// knows how to build the `run` argv (image, --rm, --name, -v mounts, -e env, -w
// workdir), how to tear a container down (rm -f), and whether it can actually
// launch right now. It is the Phase-0 SpawnClient seam made runtime-swappable: a
// new runtime is one more implementation, selected by detection/config.
type ContainerRuntime interface {
	// Name identifies the runtime ("docker" | "podman" | "host") for diagnostics.
	Name() string
	// Binary is the runtime CLI resolved for exec (e.g. "docker"). Empty for Host.
	Binary() string
	// Available reports whether this runtime can launch a container NOW: the CLI
	// is on PATH and its daemon is reachable. Used to degrade (never blocks).
	Available() bool
	// RunArgs builds the full argv (after Binary) that starts the container in the
	// FOREGROUND with stdout/stderr attached — go-plugin reads the plugin's
	// handshake line from the container's stdout, so no -d and no -t (a pty would
	// mangle the handshake).
	RunArgs(spec RunSpec) []string
	// RemoveArgs builds the argv that force-removes the named container (teardown).
	RemoveArgs(name string) []string
}

// RunSpec is the runtime-agnostic description of one plugin container: which image
// to run, the in-container argv, the identical-path project mount + the
// host↔container socket-dir mount, a fresh HOME, and the curated go-plugin
// handshake env. A ContainerRuntime renders it into its own `run` argv.
type RunSpec struct {
	Image   string   // image reference to run
	Name    string   // --name, so teardown can target this exact container
	WorkDir string   // -w and the identical-path project bind-mount target
	Home    string   // fresh $HOME inside the container (engine global state isolated)
	Command []string // in-container argv (the container's ctxloom + "llm serve …")
	Env     []string // -e KEY=VAL, the curated go-plugin handshake env
	Mounts  []Mount  // --mount type=bind bind mounts
}

// Mount is one bind mount rendered as `--mount type=bind,source=,target=[,readonly]`.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// Docker launches containers via the docker CLI. rootless records whether the
// daemon is rootless — the axis that decides the run's identity mapping:
// under rootless docker the container's ROOT user maps to the invoking host
// user (the only uid that does — a non-root container user would map to a
// subuid and wreck bind-mount ownership), so the run stays container-root and
// no PUID is passed. Under a rootful daemon the container starts as root and
// PUID/PGID tell the image entrypoint to remap its `ctxloom` user to the
// launching uid/gid and drop to it — named non-root identity with correct
// host-side ownership (socket and project files land launching-user-owned).
type Docker struct{ rootless bool }

// Name identifies the runtime.
func (Docker) Name() string { return "docker" }

// Binary is the docker CLI.
func (Docker) Binary() string { return "docker" }

// Available reports docker CLI on PATH + a reachable daemon.
func (Docker) Available() bool { return runtimeReachable("docker") }

// RunArgs renders the spec into a `docker run` argv.
func (d Docker) RunArgs(spec RunSpec) []string {
	args := []string{"run", "--rm", "--name", spec.Name}
	if !d.rootless {
		// Rootful daemon: the entrypoint remaps ctxloom to the launching
		// uid/gid and drops privileges. Rootless already maps root→host user.
		args = append(args, identityEnvArgs()...)
	}
	args = append(args, renderRunSpec(spec)...)
	return args
}

// identityEnvArgs renders the PUID/PGID env that tells the agent image's
// entrypoint to remap its generic ctxloom user to the launching uid/gid and
// drop privileges to it.
func identityEnvArgs() []string {
	return []string{
		"-e", fmt.Sprintf("PUID=%d", os.Getuid()),
		"-e", fmt.Sprintf("PGID=%d", os.Getgid()),
	}
}

// RemoveArgs force-removes the container (SIGKILL + rm; idempotent enough that a
// racing --rm auto-remove just reports "no such container", which callers ignore).
func (Docker) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// Podman launches containers via the podman CLI. podman's run/rm argv is
// docker-CLI-compatible. rootless records whether the engine is rootless:
// rootless podman needs --userns=keep-id so the launching uid maps to ITSELF
// in-container instead of a subuid — then the entrypoint's PUID/PGID remap
// yields a run that is genuinely non-root in-container AND correctly owned on
// the host, something rootless docker cannot express. Rootful podman behaves
// like rootful docker (identity mapping; entrypoint remap only). (Built but
// not daemon-tested on this host — no podman installed.)
type Podman struct{ rootless bool }

// Name identifies the runtime.
func (Podman) Name() string { return "podman" }

// Binary is the podman CLI.
func (Podman) Binary() string { return "podman" }

// Available reports podman CLI on PATH + a reachable engine.
func (Podman) Available() bool { return runtimeReachable("podman") }

// RunArgs renders the spec into a `podman run` argv (docker-compatible). Both
// modes start as container-root and let the image entrypoint remap ctxloom to
// the launching uid/gid (PUID/PGID) and drop to it; rootless additionally
// needs keep-id so that uid maps to itself on the host instead of a subuid.
func (p Podman) RunArgs(spec RunSpec) []string {
	args := []string{"run", "--rm", "--name", spec.Name}
	if p.rootless {
		// keep-id's DEFAULT user is the host uid (not root), which couldn't
		// remap; enter as namespaced root so the entrypoint can usermod+drop.
		args = append(args, "--userns=keep-id", "--user", "0:0")
	}
	args = append(args, identityEnvArgs()...)
	return append(args, renderRunSpec(spec)...)
}

// RemoveArgs force-removes the container.
func (Podman) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// podmanIsRootless reports whether the podman engine is rootless (`podman info`
// security flag). Best-effort: on any error it returns true — podman is
// rootless by default, and keep-id under a rootful engine errors loudly at
// launch (degrading the run) while a missing keep-id under rootless silently
// wrecks bind-mount ownership, the worse failure.
func podmanIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}}").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != "false"
}

// Host is the non-container runtime: the plugin runs as a bare host subprocess
// (the None and — Phase 2 — worktree policies). It satisfies ContainerRuntime so
// runtime selection is uniform, but it launches nothing itself: the host path
// spawns via pb.NewSelfInvokingClientForLabel, so RunArgs/RemoveArgs are unused.
type Host struct{}

// Name identifies the runtime.
func (Host) Name() string { return "host" }

// Binary is empty — Host execs the plugin directly, not via a container CLI.
func (Host) Binary() string { return "" }

// Available is always true — the host can always run a subprocess (None never
// fails; it is the fault-tolerant floor).
func (Host) Available() bool { return true }

// RunArgs is a noop — Host does not launch a container.
func (Host) RunArgs(RunSpec) []string { return nil }

// RemoveArgs is a noop — Host has nothing to tear down.
func (Host) RemoveArgs(string) []string { return nil }

// renderRunSpec renders the runtime-agnostic tail of a run argv (env, mounts,
// workdir, image, in-container command) shared by Docker and Podman. The
// runtime-specific head (--rm/--name/--user) is prepended by each RunArgs.
func renderRunSpec(spec RunSpec) []string {
	var args []string
	if spec.Home != "" {
		args = append(args, "-e", "HOME="+spec.Home)
	}
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	for _, m := range spec.Mounts {
		// --mount (not -v host:container[:ro]): the colon-delimited -v grammar is
		// ambiguous on Windows, where a host path carries a drive-letter colon
		// (C:\...) that mis-splits. --mount type=bind,source=,target=[,readonly]
		// is colon-free and renders identically on docker + podman + Linux. Every
		// mount in this package funnels through here, so this is the single site.
		// (--mount requires the source to already exist; every Mount.Host in this
		// package is a path we created or verified before the run, so that holds.)
		opt := "type=bind,source=" + m.Host + ",target=" + m.Container
		if m.ReadOnly {
			opt += ",readonly"
		}
		args = append(args, "--mount", opt)
	}
	if spec.WorkDir != "" {
		args = append(args, "-w", spec.WorkDir)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

// runtimeReachable reports whether a container runtime CLI is on PATH and its
// daemon answers `<bin> info`. Fault tolerance: any failure (missing binary,
// daemon down) → false → the caller degrades to None; it never blocks the LLM.
func runtimeReachable(bin string) bool {
	if _, err := exec.LookPath(bin); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// `info` succeeds only when the daemon/engine is reachable; discard its output.
	cmd := exec.CommandContext(ctx, bin, "info")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// dockerIsRootless reports whether the docker daemon is rootless (its `info`
// SecurityOptions list "rootless"). Best-effort: on any error it returns false
// (assume rootful → we pass --user), which is the safe default.
func dockerIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "rootless")
}

// InContainer reports whether THIS process is already running inside a
// container (dev container, CI job, pod). Markers: the docker/podman sentinel
// files, the well-known dev-container/k8s env vars, and container-runtime
// signatures in the init cgroup. It gates the devcontainer-specific wording on
// degrade warnings and feeds `container check`; the actual can-containers-
// launch decision is behavior-based (runtime reachability + the shared-fs
// probe), never this heuristic alone.
func InContainer() bool { return len(containerMarkers()) > 0 }

// containerMarkers returns the matched in-container markers (empty = none),
// for InContainer and the `container check` diagnosis.
func containerMarkers() []string {
	return inContainerFrom(
		func(p string) error { _, err := os.Stat(p); return err },
		os.ReadFile,
		os.Getenv,
	)
}

// inContainerFrom is the seam-injected core of the in-container detection:
// stat/readFile/getenv arrive as functions so tests never touch the real
// /proc, sentinel files, or process env (CI itself runs inside containers,
// and the hostile-env suite junks the environment). Returns every marker that
// matched, named for diagnostics.
func inContainerFrom(stat func(string) error, readFile func(string) ([]byte, error), getenv func(string) string) []string {
	var markers []string
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if stat(f) == nil {
			markers = append(markers, f)
		}
	}
	for _, e := range []string{"REMOTE_CONTAINERS", "CODESPACES", "DEVCONTAINER", "KUBERNETES_SERVICE_HOST"} {
		if getenv(e) != "" {
			markers = append(markers, "$"+e)
		}
	}
	// cgroup v1 runtime signatures — BEST-EFFORT ONLY: cgroup v2 exposes a
	// bare "0::/" with no runtime marker, which is why the sentinel-file and
	// env probes lead and this never stands alone as a negative signal.
	if b, err := readFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "containerd", "kubepods", "/lxc/"} {
			if strings.Contains(s, marker) {
				markers = append(markers, "cgroup:"+marker)
			}
		}
	}
	return markers
}

// SelectRuntime picks the container runtime by config preference then detection.
// prefer is an explicit runtime name ("docker" | "podman"); empty means auto. It
// returns the first launchable runtime (prefer, else docker, else podman) with
// rootless detected for docker, or Host{} when none can launch — the caller then
// degrades to None. It never errors: a runtime that cannot launch is simply not
// selected (CLAUDE.md fault tolerance).
func SelectRuntime(prefer string) ContainerRuntime {
	newDocker := func() ContainerRuntime { return Docker{rootless: dockerIsRootless()} }
	byName := map[string]func() ContainerRuntime{
		"docker": newDocker,
		"podman": func() ContainerRuntime { return Podman{rootless: podmanIsRootless()} },
	}
	if prefer != "" {
		if mk, ok := byName[prefer]; ok {
			if rt := mk(); rt.Available() {
				return rt
			}
		}
		// An unknown or unavailable preference falls through to auto-detection.
	}
	for _, mk := range []func() ContainerRuntime{newDocker, byName["podman"]} {
		if rt := mk(); rt.Available() {
			return rt
		}
	}
	return Host{}
}
